package operator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openshift/api/features"

	"github.com/davecgh/go-spew/spew"
	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned"
	configv1informers "github.com/openshift/client-go/config/informers/externalversions"
	applyoperatorv1 "github.com/openshift/client-go/operator/applyconfigurations/operator/v1"
	"github.com/openshift/cluster-config-operator/pkg/cmd/render"
	"github.com/openshift/cluster-config-operator/pkg/operator/aws_platform_service_location"
	"github.com/openshift/cluster-config-operator/pkg/operator/featuregates"
	"github.com/openshift/cluster-config-operator/pkg/operator/featureupgradablecontroller"
	kubecloudconfig "github.com/openshift/cluster-config-operator/pkg/operator/kube_cloud_config"
	"github.com/openshift/cluster-config-operator/pkg/operator/migration_platform_status"
	"github.com/openshift/cluster-config-operator/pkg/operator/operatorclient"
	"github.com/openshift/cluster-config-operator/pkg/operator/removelatencysensitive"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/genericoperatorclient"
	"github.com/openshift/library-go/pkg/operator/loglevel"
	"github.com/openshift/library-go/pkg/operator/staleconditions"
	"github.com/openshift/library-go/pkg/operator/status"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/clock"
)

type OperatorOptions struct {
	OperatorVersion             string
	AuthoritativeFeatureGateDir string
}

func NewOperatorOptions() *OperatorOptions {
	return &OperatorOptions{}
}

func (o *OperatorOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.OperatorVersion, "operator-version", o.OperatorVersion, "version of the operator that is running")
	fs.StringVar(&o.AuthoritativeFeatureGateDir, "authoritative-feature-gate-dir", o.AuthoritativeFeatureGateDir, "directory containing each possible featuregate manifest.")
}

func (o *OperatorOptions) RunOperator(ctx context.Context, controllerContext *controllercmd.ControllerContext) error {
	// This kube client use protobuf, do not use it for CR
	kubeClient, err := kubernetes.NewForConfig(controllerContext.ProtoKubeConfig)
	if err != nil {
		return err
	}
	configClient, err := configv1client.NewForConfig(controllerContext.KubeConfig)
	if err != nil {
		return err
	}

	featureGateDetails, err := o.getFeatureGateMappingFromDisk(ctx, configClient)
	if err != nil {
		return err
	}

	configInformers := configv1informers.NewSharedInformerFactory(configClient, 10*time.Minute)
	kubeInformersForNamespaces := v1helpers.NewKubeInformersForNamespaces(kubeClient,
		"",
		operatorclient.GlobalUserSpecifiedConfigNamespace,
		operatorclient.GlobalMachineSpecifiedConfigNamespace,
		"kube-system",
	)
	operatorClient, dynamicInformers, err := genericoperatorclient.NewClusterScopedOperatorClient(
		clock.RealClock{},
		controllerContext.KubeConfig,
		operatorv1.GroupVersion.WithResource("configs"),
		operatorv1.GroupVersion.WithKind("Config"),
		extractOperatorSpec,
		extractOperatorStatus,
	)
	if err != nil {
		return err
	}

	// don't change any versions until we sync
	versionRecorder := status.NewVersionGetter()
	clusterOperator, err := configClient.ConfigV1().ClusterOperators().Get(ctx, "config-operator", metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	for _, version := range clusterOperator.Status.Versions {
		versionRecorder.SetVersion(version.Name, version.Version)
	}
	// this prevents marking the feature-gates as upgraded during the upgrade that brings the new operand name into the version array.
	if _, ok := versionRecorder.GetVersions()[featuregates.FeatureVersionName]; !ok {
		versionRecorder.SetVersion(featuregates.FeatureVersionName, "")
	}
	versionRecorder.SetVersion("operator", o.OperatorVersion)

	featureGateController := featuregates.NewFeatureGateController(
		featureGateDetails,
		operatorClient,
		o.OperatorVersion,
		configClient.ConfigV1(),
		configInformers.Config().V1().FeatureGates(),
		configInformers.Config().V1().ClusterVersions(),
		versionRecorder,
		controllerContext.EventRecorder,
	)

	// to be removed a release after we block upgrades
	latencySensitiveRemover := removelatencysensitive.NewLatencySensitiveRemovalController(
		operatorClient,
		configClient.ConfigV1(),
		configInformers.Config().V1().FeatureGates(),
		controllerContext.EventRecorder,
	)

	featureUpgradeableController := featureupgradablecontroller.NewFeatureUpgradeableController(
		operatorClient,
		configInformers,
		controllerContext.EventRecorder,
	)

	infraController := aws_platform_service_location.NewController(
		operatorClient,
		configClient.ConfigV1(),
		configInformers.Config().V1().Infrastructures().Lister(),
		configInformers.Config().V1().Infrastructures().Informer(),
		controllerContext.EventRecorder,
	)

	kubeCloudConfigController := kubecloudconfig.NewController(
		operatorClient,
		configClient.ConfigV1(),
		configInformers.Config().V1().Infrastructures().Lister(),
		configInformers.Config().V1().Infrastructures().Informer(),
		v1helpers.CachedConfigMapGetter(kubeClient.CoreV1(), kubeInformersForNamespaces),
		kubeInformersForNamespaces.InformersFor(operatorclient.GlobalUserSpecifiedConfigNamespace).Core().V1().ConfigMaps().Informer(),
		kubeInformersForNamespaces.InformersFor(operatorclient.GlobalMachineSpecifiedConfigNamespace).Core().V1().ConfigMaps().Informer(),
		controllerContext.EventRecorder,
	)

	migrationPlatformStatusController := migration_platform_status.NewController(
		operatorClient,
		configClient.ConfigV1(),
		configInformers.Config().V1().Infrastructures().Lister(),
		configInformers.Config().V1().Infrastructures().Informer(),
		v1helpers.CachedConfigMapGetter(kubeClient.CoreV1(), kubeInformersForNamespaces),
		kubeInformersForNamespaces.InformersFor("kube-system").Core().V1().ConfigMaps().Informer(),
		controllerContext.EventRecorder,
	)

	statusController := status.NewClusterOperatorStatusController(
		"config-operator",
		[]configv1.ObjectReference{
			{Group: "operator.openshift.io", Resource: "configs", Name: "cluster"},
			{Group: "", Resource: "namespaces", Name: "openshift-config"},
			{Group: "", Resource: "namespaces", Name: "openshift-config-operator"},
		},
		configClient.ConfigV1(),
		configInformers.Config().V1().ClusterOperators(),
		operatorClient,
		versionRecorder,
		controllerContext.EventRecorder,
		controllerContext.Clock,
	)

	logLevelController := loglevel.NewClusterOperatorLoggingController(operatorClient, controllerContext.EventRecorder)

	// As this operator does not manage any component/workload, report this operator as available and not progressing by default.
	// TODO: Revisit this with full controller at some point.
	operatorController := factory.New().ResyncEvery(10*time.Second).WithSync(func(ctx context.Context, controllerContext factory.SyncContext) error {
		operatorStatus, updated, updateErr := v1helpers.UpdateStatus(ctx, operatorClient,
			v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
				Type:   "OperatorAvailable",
				Status: operatorv1.ConditionTrue,
				Reason: "AsExpected",
			}),
			v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
				Type:   "OperatorProgressing",
				Status: operatorv1.ConditionFalse,
				Reason: "AsExpected",
			}),
			v1helpers.UpdateConditionFn(operatorv1.OperatorCondition{
				Type:   "OperatorUpgradeable",
				Status: operatorv1.ConditionTrue,
				Reason: "AsExpected",
			}),
		)
		if updated && operatorStatus != nil {
			controllerContext.Recorder().Eventf("ConfigOperatorStatusChanged", "Operator conditions defaulted: %s", spew.Sprint(operatorStatus.Conditions))
		}
		return updateErr
	}).ToController("ConfigOperatorController", controllerContext.EventRecorder)

	// The MigrationAWSStatus controller has been renamed to MigrationPlatformStatus. Consequently, the
	// MigrationAWSStatusControllerDegraded conditions has been replaced with the
	// MigrationPlatformStatusControllerDegraded condition. The old condition is stale and should be removed.
	staleConditionsController := staleconditions.NewRemoveStaleConditionsController(
		"StaleConditionController",
		[]string{"MigrationAWSStatusControllerDegraded"},
		operatorClient,
		controllerContext.EventRecorder,
	)

	go dynamicInformers.Start(ctx.Done())
	go configInformers.Start(ctx.Done())
	go kubeInformersForNamespaces.Start(ctx.Done())

	go infraController.Run(ctx, 1)
	go kubeCloudConfigController.Run(ctx, 1)
	go logLevelController.Run(ctx, 1)
	go statusController.Run(ctx, 1)
	go operatorController.Run(ctx, 1)
	go migrationPlatformStatusController.Run(ctx, 1)
	go staleConditionsController.Run(ctx, 1)
	go featureGateController.Run(ctx, 1)
	go latencySensitiveRemover.Run(ctx, 1)
	go featureUpgradeableController.Run(ctx, 1)

	<-ctx.Done()
	return nil
}

func (o *OperatorOptions) getFeatureGateMappingFromDisk(ctx context.Context, configClient configv1client.Interface) (map[configv1.FeatureSet]*features.FeatureGateEnabledDisabled, error) {
	// TODO get the cluster profile in a better way, this isn't strictly correct
	infrastructure, err := configClient.ConfigV1().Infrastructures().Get(ctx, "cluster", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("unable to get clusterprofile: %w", err)
	}
	clusterProfileAnnotation := ""
	switch infrastructure.Status.ControlPlaneTopology {
	case configv1.ExternalTopologyMode:
		clusterProfileAnnotation = "include.release.openshift.io/ibm-cloud-managed"
	default:
		clusterProfileAnnotation = "include.release.openshift.io/self-managed-high-availability"
	}

	ret := map[configv1.FeatureSet]*features.FeatureGateEnabledDisabled{}

	err = filepath.Walk(o.AuthoritativeFeatureGateDir,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}

			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			featureGate, err := render.ReadFeatureGateV1(content)
			if err != nil {
				return fmt.Errorf("%q is not a featuregate: %v", path, featureGate)
			}

			// older versions of openshift/api did not write manifests with clusterprofiles preferences, but new ones do
			if hasClusterProfilePreference(featureGate.Annotations) {
				// if the manifest has a clusterprofile preference and it's not the one we're installing with
				// skip the manifest.
				if featureGate.Annotations[clusterProfileAnnotation] != "false-except-for-the-config-operator" {
					return nil
				}
			}

			featureGateValues := &features.FeatureGateEnabledDisabled{}
			for _, possibleGates := range featureGate.Status.FeatureGates {
				if possibleGates.Version != o.OperatorVersion {
					continue
				}
				for _, curr := range possibleGates.Enabled {
					featureGateValues.Enabled = append(featureGateValues.Enabled, features.FeatureGateDescription{
						FeatureGateAttributes: configv1.FeatureGateAttributes{
							Name: curr.Name,
						},
					})
				}
				for _, curr := range possibleGates.Disabled {
					featureGateValues.Disabled = append(featureGateValues.Disabled, features.FeatureGateDescription{
						FeatureGateAttributes: configv1.FeatureGateAttributes{
							Name: curr.Name,
						},
					})
				}

				break
			}
			ret[featureGate.Spec.FeatureGateSelection.FeatureSet] = featureGateValues

			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	if len(ret) == 0 {
		return nil, fmt.Errorf("featuregates not located")
	}

	return ret, nil
}

func hasClusterProfilePreference(annotations map[string]string) bool {
	for k := range annotations {
		if strings.HasPrefix(k, "include.release.openshift.io/") {
			return true
		}
	}

	return false
}

func extractOperatorSpec(obj *unstructured.Unstructured, fieldManager string) (*applyoperatorv1.OperatorSpecApplyConfiguration, error) {
	castObj := &operatorv1.Config{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, castObj); err != nil {
		return nil, fmt.Errorf("unable to convert to Config: %w", err)
	}
	ret, err := applyoperatorv1.ExtractConfig(castObj, fieldManager)
	if err != nil {
		return nil, fmt.Errorf("unable to extract fields for %q: %w", fieldManager, err)
	}
	if ret.Spec == nil {
		return nil, nil
	}
	return &ret.Spec.OperatorSpecApplyConfiguration, nil
}

func extractOperatorStatus(obj *unstructured.Unstructured, fieldManager string) (*applyoperatorv1.OperatorStatusApplyConfiguration, error) {
	castObj := &operatorv1.Config{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, castObj); err != nil {
		return nil, fmt.Errorf("unable to convert to Config: %w", err)
	}
	ret, err := applyoperatorv1.ExtractConfigStatus(castObj, fieldManager)
	if err != nil {
		return nil, fmt.Errorf("unable to extract fields for %q: %w", fieldManager, err)
	}

	if ret.Status == nil {
		return nil, nil
	}
	return &ret.Status.OperatorStatusApplyConfiguration, nil
}
