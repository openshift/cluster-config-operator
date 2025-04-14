package featuregates

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	v1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/status"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
)

const FeatureVersionName = "feature-gates"

// FeatureGateController is responsible for setting usable FeatureGates on features.config.openshift.io/cluster
type FeatureGateController struct {
	processVersion       string
	featureGatesClient   configv1client.FeatureGatesGetter
	featureGatesLister   configlistersv1.FeatureGateLister
	nodeClient           configv1client.NodesGetter
	nodeLister           configlistersv1.NodeLister
	clusterVersionLister configlistersv1.ClusterVersionLister
	// for unit testing
	featureSetMap map[configv1.FeatureSet]*features.FeatureGateEnabledDisabled

	versionRecorder status.VersionGetter
	eventRecorder   events.Recorder
}

// NewController returns a new FeatureGateController.
func NewFeatureGateController(
	featureGateDetails map[configv1.FeatureSet]*features.FeatureGateEnabledDisabled,
	operatorClient operatorv1helpers.OperatorClient,
	processVersion string,
	featureGatesClient configv1client.FeatureGatesGetter, featureGatesInformer v1.FeatureGateInformer,
	nodeClient configv1client.NodesGetter, nodeInformer v1.NodeInformer,
	clusterVersionInformer v1.ClusterVersionInformer,
	versionRecorder status.VersionGetter,
	eventRecorder events.Recorder) factory.Controller {
	c := &FeatureGateController{
		processVersion:       processVersion,
		featureGatesClient:   featureGatesClient,
		featureGatesLister:   featureGatesInformer.Lister(),
		nodeClient:           nodeClient,
		nodeLister:           nodeInformer.Lister(),
		clusterVersionLister: clusterVersionInformer.Lister(),
		featureSetMap:        featureGateDetails,
		versionRecorder:      versionRecorder,
		eventRecorder:        eventRecorder,
	}

	return factory.New().
		WithInformers(
			operatorClient.Informer(),
			featureGatesInformer.Informer(),
			clusterVersionInformer.Informer(),
			nodeInformer.Informer(),
		).
		WithSync(c.sync).
		WithSyncDegradedOnError(operatorClient).
		ResyncEvery(time.Minute).
		ToController("FeatureGateController", eventRecorder)
}

func (c FeatureGateController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	featureGates, err := c.featureGatesLister.Get("cluster")
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("unable to get FeatureGate: %w", err)
	}

	clusterVersion, err := c.clusterVersionLister.Get("version")
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("unable to get ClusterVersion: %w", err)
	}

	knownVersions := sets.NewString(c.processVersion)
	for _, cvoVersion := range clusterVersion.Status.History {
		knownVersions.Insert(cvoVersion.Version)
	}

	nodesConfig, err := c.nodeLister.Get("cluster")
	// ignore not found errors, as we can treat that as minimum kubelet version isn't populated yet.
	// When it is, we'll get an update and rollout an update to the features
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("unable to get NodesConfig: %w", err)
	}
	currentMinimumVersions := map[configv1.MinimumComponent]string{}
	if nodesConfig != nil && nodesConfig.Spec.MinimumKubeletVersion != "" {
		currentMinimumVersions[configv1.MinimumComponentKubelet] = nodesConfig.Spec.MinimumKubeletVersion
	}

	currentDetails, err := FeaturesGateDetailsFromFeatureSets(c.featureSetMap, featureGates, c.processVersion, currentMinimumVersions)
	if err != nil {
		return fmt.Errorf("unable to determine FeatureGateDetails from FeatureSets: %w", err)
	}
	// desiredFeatureGates will include first, the current version's feature gates
	// then all the historical featuregates in order, removing those for versions not in the CVO history.
	desiredFeatureGates := []configv1.FeatureGateDetails{*currentDetails}

	for i := range featureGates.Status.FeatureGates {
		featureGateValues := featureGates.Status.FeatureGates[i]
		if featureGateValues.Version == c.processVersion {
			// we already added our processVersion
			continue
		}
		if !knownVersions.Has(featureGateValues.Version) {
			continue
		}
		desiredFeatureGates = append(desiredFeatureGates, featureGateValues)
	}

	if reflect.DeepEqual(desiredFeatureGates, featureGates.Status.FeatureGates) {
		// no update, confirm in the clusteroperator that the version has been achieved.
		c.versionRecorder.SetVersion(
			FeatureVersionName,
			c.processVersion,
		)

		return nil
	}

	// TODO, this looks ripe for SSA.
	toWrite := featureGates.DeepCopy()
	toWrite.Status.FeatureGates = desiredFeatureGates
	if _, err := c.featureGatesClient.FeatureGates().UpdateStatus(ctx, toWrite, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("unable to update FeatureGate status: %w", err)
	}

	enabled, disabled := []string{}, []string{}
	for _, curr := range currentDetails.Enabled {
		enabled = append(enabled, string(curr.Name))
	}
	for _, curr := range currentDetails.Disabled {
		disabled = append(disabled, string(curr.Name))
	}
	c.eventRecorder.Eventf(
		"FeatureGateUpdate", "FeatureSet=%q, Version=%q, Enabled=%q, Disabled=%q",
		toWrite.Spec.FeatureSet, c.processVersion, strings.Join(enabled, ","), strings.Join(disabled, ","))
	// on successful write, we're at the correct level
	c.versionRecorder.SetVersion(
		FeatureVersionName,
		c.processVersion,
	)

	return nil
}

func featuresGatesFromFeatureSets(knownFeatureSets map[configv1.FeatureSet]*features.FeatureGateEnabledDisabled, featureGates *configv1.FeatureGate) ([]configv1.FeatureGateName, []configv1.FeatureGateName, map[configv1.FeatureGateName][]configv1.MinimumComponentVersion, error) {
	if featureGates.Spec.FeatureSet == configv1.CustomNoUpgrade {
		if featureGates.Spec.FeatureGateSelection.CustomNoUpgrade != nil {
			return completeFeatureGatesForCustom(
				knownFeatureSets[configv1.Default],
				featureGates.Spec.FeatureGateSelection.CustomNoUpgrade.Enabled,
				featureGates.Spec.FeatureGateSelection.CustomNoUpgrade.Disabled,
			)
		}
		return []configv1.FeatureGateName{}, []configv1.FeatureGateName{}, nil, nil
	}

	featureSet, ok := knownFeatureSets[featureGates.Spec.FeatureSet]
	if !ok {
		return []configv1.FeatureGateName{}, []configv1.FeatureGateName{}, nil, fmt.Errorf(".spec.featureSet %q not found", featureSet)
	}

	completeEnabled, completeDisabled, minimumVersions := completeFeatureGates(knownFeatureSets, toFeatureGateNames(featureSet.Enabled), toFeatureGateNames(featureSet.Disabled))
	return completeEnabled, completeDisabled, minimumVersions, nil
}

func toFeatureGateNames(in []features.FeatureGateDescription) []configv1.FeatureGateName {
	out := []configv1.FeatureGateName{}
	for _, curr := range in {
		out = append(out, curr.FeatureGateAttributes.Name)
	}

	return out
}

// completeFeatureGates identifies every known feature and ensures that is explicitly on or explicitly off
func completeFeatureGates(knownFeatureSets map[configv1.FeatureSet]*features.FeatureGateEnabledDisabled, enabled, disabled []configv1.FeatureGateName) ([]configv1.FeatureGateName, []configv1.FeatureGateName, map[configv1.FeatureGateName][]configv1.MinimumComponentVersion) {
	specificallyEnabledFeatureGates := sets.New[configv1.FeatureGateName]()
	specificallyEnabledFeatureGates.Insert(enabled...)

	knownFeatureGates := sets.New[configv1.FeatureGateName]()
	knownFeatureGates.Insert(enabled...)
	knownFeatureGates.Insert(disabled...)
	minimumVersionMap := map[configv1.FeatureGateName][]configv1.MinimumComponentVersion{}
	for _, known := range knownFeatureSets {
		for _, curr := range known.Disabled {
			knownFeatureGates.Insert(curr.FeatureGateAttributes.Name)
			if len(curr.FeatureGateAttributes.RequiredMinimumComponentVersions) != 0 {
				cpy := make([]configv1.MinimumComponentVersion, 0, len(curr.FeatureGateAttributes.RequiredMinimumComponentVersions))
				for _, cv := range curr.FeatureGateAttributes.RequiredMinimumComponentVersions {
					cpy = append(cpy, cv)
				}
				minimumVersionMap[curr.FeatureGateAttributes.Name] = cpy
			}
		}
		for _, curr := range known.Enabled {
			knownFeatureGates.Insert(curr.FeatureGateAttributes.Name)
			if len(curr.FeatureGateAttributes.RequiredMinimumComponentVersions) != 0 {
				cpy := make([]configv1.MinimumComponentVersion, 0, len(curr.FeatureGateAttributes.RequiredMinimumComponentVersions))
				for _, cv := range curr.FeatureGateAttributes.RequiredMinimumComponentVersions {
					cpy = append(cpy, cv)
				}
				minimumVersionMap[curr.FeatureGateAttributes.Name] = cpy
			}
		}
	}

	return enabled, knownFeatureGates.Difference(specificallyEnabledFeatureGates).UnsortedList(), minimumVersionMap
}

func completeFeatureGatesForCustom(defaultFeatureGates *features.FeatureGateEnabledDisabled, forceEnabledList, forceDisabledList []configv1.FeatureGateName) ([]configv1.FeatureGateName, []configv1.FeatureGateName, map[configv1.FeatureGateName][]configv1.MinimumComponentVersion, error) {
	for _, forceEnabled := range forceEnabledList {
		if inListOfNames(forceDisabledList, forceEnabled) {
			return nil, nil, nil, fmt.Errorf("trying to enable and disable %q", forceEnabled)
		}
	}

	minimumVersionMap := map[configv1.FeatureGateName][]configv1.MinimumComponentVersion{}

	enabled := []configv1.FeatureGateName{}
	for _, forceEnabled := range forceEnabledList {
		enabled = append(enabled, forceEnabled)
	}
	for _, defaultEnabled := range defaultFeatureGates.Enabled {
		if !inListOfNames(forceDisabledList, defaultEnabled.FeatureGateAttributes.Name) {
			enabled = append(enabled, defaultEnabled.FeatureGateAttributes.Name)
		}
		if len(defaultEnabled.FeatureGateAttributes.RequiredMinimumComponentVersions) != 0 {
			cpy := make([]configv1.MinimumComponentVersion, 0, len(defaultEnabled.FeatureGateAttributes.RequiredMinimumComponentVersions))
			for _, cv := range defaultEnabled.FeatureGateAttributes.RequiredMinimumComponentVersions {
				cpy = append(cpy, cv)
			}
			klog.Infof("XXX %+v", cpy)
			minimumVersionMap[defaultEnabled.FeatureGateAttributes.Name] = cpy
		}
	}

	disabled := []configv1.FeatureGateName{}
	for _, forceDisabled := range forceDisabledList {
		disabled = append(disabled, forceDisabled)
	}
	for _, defaultDisabled := range defaultFeatureGates.Disabled {
		if !inListOfNames(forceEnabledList, defaultDisabled.FeatureGateAttributes.Name) {
			disabled = append(disabled, defaultDisabled.FeatureGateAttributes.Name)
		}
		if len(defaultDisabled.FeatureGateAttributes.RequiredMinimumComponentVersions) != 0 {
			cpy := make([]configv1.MinimumComponentVersion, 0, len(defaultDisabled.FeatureGateAttributes.RequiredMinimumComponentVersions))
			for _, cv := range defaultDisabled.FeatureGateAttributes.RequiredMinimumComponentVersions {
				cpy = append(cpy, cv)
			}
			klog.Infof("XXX %+v", cpy)
			minimumVersionMap[defaultDisabled.FeatureGateAttributes.Name] = cpy
		}
	}

	return enabled, disabled, minimumVersionMap, nil
}

func inListOfNames(haystack []configv1.FeatureGateName, needle configv1.FeatureGateName) bool {
	for _, curr := range haystack {
		if curr == needle {
			return true
		}
	}
	return false
}

func FeaturesGateDetailsFromFeatureSets(featureSetMap map[configv1.FeatureSet]*features.FeatureGateEnabledDisabled, featureGates *configv1.FeatureGate, currentVersion string, currentMinimumVersions map[configv1.MinimumComponent]string) (*configv1.FeatureGateDetails, error) {
	enabled, disabled, minimumVersionsOfGates, err := featuresGatesFromFeatureSets(featureSetMap, featureGates)
	if err != nil {
		return nil, err
	}
	currentDetails := configv1.FeatureGateDetails{
		Version: currentVersion,
	}
	for _, gateName := range enabled {
		// If the API defines a current minimum version...
		requiredComponentVersions, _ := minimumVersionsOfGates[gateName]
		// ... we may need to skip it...
		skip := false
		for _, requiredComponentVersion := range requiredComponentVersions {
			// ... are any minimum versions defined in the API?
			currentMinVersionStr, ok := currentMinimumVersions[requiredComponentVersion.Component]
			if !ok || currentMinVersionStr == "" {
				// disable gates that don't have a minimum version currently enabled in the API
				disabled = append(disabled, gateName)
				skip = true
				continue
			}
			requiredMinVersion, err := semver.Parse(requiredComponentVersion.Version)
			if err != nil {
				// This shouldn't be possible, so we should panic
				panic(fmt.Errorf("Programming error: specified required minimum kubelet version for %s did not parse %w", gateName, err))
			}
			currentMinVersion, err := semver.Parse(currentMinVersionStr)
			if err != nil {
				// This shouldn't be possible (faulty min versions should be filtered from API) so we should panic
				panic(fmt.Errorf("Programming error: configured minimum kubelet version for %s did not parse %w", gateName, err))
			}
			if requiredMinVersion.LT(currentMinVersion) {
				// disable gates that don't have a new enough minimum version
				disabled = append(disabled, gateName)
				skip = true
			}
		}
		// If there's no minimum version enabled for this feature, or the minimum version is new enough, keep it in enabled
		if !skip {
			currentDetails.Enabled = append(currentDetails.Enabled, configv1.FeatureGateAttributes{
				Name:                             gateName,
				RequiredMinimumComponentVersions: requiredComponentVersions,
			})
		}
	}
	for _, gateName := range disabled {
		minVersions, _ := minimumVersionsOfGates[gateName]
		currentDetails.Disabled = append(currentDetails.Disabled, configv1.FeatureGateAttributes{
			Name:                             gateName,
			RequiredMinimumComponentVersions: minVersions,
		})
	}

	// sort for stability
	sort.Sort(byName(currentDetails.Enabled))
	sort.Sort(byName(currentDetails.Disabled))

	return &currentDetails, nil
}

type byName []configv1.FeatureGateAttributes

func (a byName) Len() int      { return len(a) }
func (a byName) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a byName) Less(i, j int) bool {
	if strings.Compare(string(a[i].Name), string(a[j].Name)) < 0 {
		return true
	}
	return false
}
