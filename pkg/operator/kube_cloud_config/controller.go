package kubecloudconfig

import (
	"context"
	"sync"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/api/features"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	configv1listers "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/cluster-config-operator/pkg/operator/operatorclient"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

const (
	targetConfigName = "kube-cloud-config"
	targetConfigKey  = "cloud.conf"
)

// cloudConfigTransformer function transforms the input config map using the input infrastructure.congfig.openshift.io object.
// only the data and binaryData field of the output ConfigMap will be respected by consumer of the transformer.
type cloudConfigTransformer func(input *corev1.ConfigMap, key string, obj *configv1.Infrastructure) (output *corev1.ConfigMap, err error)

// KubeCloudConfigController is responsible for managing the kube-cloud-config used by various Kubernetes components
// as source for configuration on clouds/platform.
// The controller uses the ConfigMap `openshift-config/<infrastructure.spec.cloudConfig.name>` and other platform specific
// user specifications from `infrastructure.spec.platformSpec` to stitch together a new ConfigMap for kube cloud config.
// The stitched ConfigMap is stored at `openshift-config-managed/kube-cloud-confg`.
type KubeCloudConfigController struct {
	infraClient     configv1client.InfrastructureInterface
	infraLister     configv1listers.InfrastructureLister
	configMapClient corev1client.ConfigMapsGetter

	// currentFeatureGates stores the most recently observed feature gates
	// Protected by featureGatesMu
	currentFeatureGates featuregates.FeatureGate
	featureGatesMu      sync.RWMutex

	// transformers stores per platform tranformer
	cloudConfigTransformers map[configv1.PlatformType]cloudConfigTransformer
}

// NewController returns a KubeCloudConfigController
func NewController(operatorClient operatorv1helpers.OperatorClient,
	infraClient configv1client.InfrastructuresGetter, infraLister configv1listers.InfrastructureLister, infraInformer cache.SharedIndexInformer,
	configMapClient corev1client.ConfigMapsGetter,
	openshiftConfigConfigMapInformer cache.SharedIndexInformer, openshiftConfigManagedConfigMapInformer cache.SharedIndexInformer,
	featureGateAccess featuregates.FeatureGateAccess,
	recorder events.Recorder) factory.Controller {
	c := &KubeCloudConfigController{
		infraClient:             infraClient.Infrastructures(),
		infraLister:             infraLister,
		configMapClient:         configMapClient,
		cloudConfigTransformers: cloudConfigTransformers(),
	}

	// Initialize current feature gates if available
	if featureGateAccess.AreInitialFeatureGatesObserved() {
		currentFeatures, err := featureGateAccess.CurrentFeatureGates()
		if err != nil {
			klog.Warningf("unable to get current feature gates during controller initialization: %v", err)
		} else {
			c.featureGatesMu.Lock()
			c.currentFeatureGates = currentFeatures
			c.featureGatesMu.Unlock()
		}
	}

	// Create change handler for when feature settings change
	featureGateAccess.SetChangeHandler(func(featureChange featuregates.FeatureChange) {
		currentFeatures, err := featureGateAccess.CurrentFeatureGates()
		if err != nil {
			klog.Warningf("unable to get current feature gates during change event: %v", err)
		} else {
			c.featureGatesMu.Lock()
			c.currentFeatureGates = currentFeatures
			c.featureGatesMu.Unlock()
		}
	})

	return factory.New().
		WithInformers(
			operatorClient.Informer(),
			infraInformer,
			openshiftConfigConfigMapInformer,
			openshiftConfigManagedConfigMapInformer,
		).
		WithSync(c.sync).
		WithSyncDegradedOnError(operatorClient).
		ResyncEvery(time.Minute).
		ToController("KubeCloudConfigController", recorder)
}

func (c *KubeCloudConfigController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	obj, err := c.infraLister.Get("cluster")
	if apierrors.IsNotFound(err) {
		syncCtx.Recorder().Warningf("KubeCloudConfigController", "Required infrastructures.%s/cluster not found", configv1.GroupName)
		return nil
	}
	if err != nil {
		return err
	}

	currentInfra := obj.DeepCopy()
	var platformName configv1.PlatformType
	if pstatus := currentInfra.Status.PlatformStatus; pstatus != nil {
		platformName = pstatus.Type
	}
	if len(platformName) == 0 {
		syncCtx.Recorder().Warningf("KubeCloudConfigController", "Falling back to deprecated status.platform because infrastructures.%s/cluster status.platformStatus.type is empty", configv1.GroupName)
		platformName = currentInfra.Status.Platform
	}

	// Check if this controller should manage the kube-cloud-config for this platform
	shouldManage, err := c.shouldManageCloudConfig(platformName)
	if err != nil {
		return err
	}
	if !shouldManage {
		syncCtx.Recorder().Eventf("KubeCloudConfigController", "Skipping kube-cloud-config management for platform %s", platformName)
		return nil
	}

	sourceCloudConfigMap := currentInfra.Spec.CloudConfig.Name
	sourceCloudConfigKey := currentInfra.Spec.CloudConfig.Key

	source := &corev1.ConfigMap{}
	if len(sourceCloudConfigMap) > 0 {
		obj, err := c.configMapClient.ConfigMaps(operatorclient.GlobalUserSpecifiedConfigNamespace).Get(ctx, sourceCloudConfigMap, metav1.GetOptions{})
		if err != nil {
			return err
		}
		obj.DeepCopyInto(source)
		source.ObjectMeta = metav1.ObjectMeta{}
	}

	cloudConfigTransformerFn, ok := c.cloudConfigTransformers[platformName]
	if !ok {
		cloudConfigTransformerFn = asIsTransformer
	}

	target, err := cloudConfigTransformerFn(source, sourceCloudConfigKey, currentInfra)
	if err != nil {
		return err
	}

	targetCloudConfigMap := targetConfigName
	if len(target.Data) == 0 && len(target.BinaryData) == 0 { // delete if exists
		err := c.configMapClient.ConfigMaps(operatorclient.GlobalMachineSpecifiedConfigNamespace).Delete(ctx, targetCloudConfigMap, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		if err == nil {
			syncCtx.Recorder().Eventf("KubeCloudConfigController", "%s/%s ConfigMap was deleted as no longer required", operatorclient.GlobalMachineSpecifiedConfigNamespace, targetCloudConfigMap)
		}
	} else { // apply the target
		target.Name = targetCloudConfigMap
		target.Namespace = operatorclient.GlobalMachineSpecifiedConfigNamespace
		_, updated, err := resourceapply.ApplyConfigMap(ctx, c.configMapClient, syncCtx.Recorder(), target)
		if err != nil {
			return err
		}
		if updated {
			syncCtx.Recorder().Eventf("KubeCloudConfigController", "%s/%s ConfigMap was updated", operatorclient.GlobalMachineSpecifiedConfigNamespace, targetCloudConfigMap)
		}
	}

	return nil
}

// asIsTransformer implements cloudConfigTransformer and copies the input ConfigMap as-is to the output ConfigMap.
// this ensure that the input cloud conf is stored at `targetConfigKey` for the output.
func asIsTransformer(input *corev1.ConfigMap, sourceKey string, _ *configv1.Infrastructure) (*corev1.ConfigMap, error) {
	output := input.DeepCopy()
	output.Namespace = operatorclient.GlobalMachineSpecifiedConfigNamespace
	output.Name = targetConfigName
	delete(output.Data, sourceKey)
	delete(output.BinaryData, sourceKey)

	if vd, ok := input.Data[sourceKey]; ok {
		output.Data[targetConfigKey] = vd // store the config to same as input
	} else if vbd, ok := input.BinaryData[sourceKey]; ok {
		output.BinaryData[targetConfigKey] = vbd // store the config to same as input
	}

	return output, nil
}

// cloudConfigTransformers returns all configured cloud transformers
func cloudConfigTransformers() map[configv1.PlatformType]cloudConfigTransformer {
	cloudConfigTransformers := map[configv1.PlatformType]cloudConfigTransformer{
		configv1.AWSPlatformType:   awsTransformer,
		configv1.AzurePlatformType: azureTransformer,
	}
	return cloudConfigTransformers
}

// shouldManageCloudConfig determines whether this controller should manage the kube-cloud-config
// ConfigMap for the given platform type. This allows for platform-specific logic to determine
// when ownership should transfer to another operator.
func (c *KubeCloudConfigController) shouldManageCloudConfig(platformType configv1.PlatformType) (bool, error) {
	switch platformType {
	case configv1.VSpherePlatformType:
		// For vSphere, check if VSphereMultiVCenterDay2 feature gate is enabled
		// When enabled, ownership transfers to cluster-cloud-controller-manager-operator
		enabled := c.isFeatureGateEnabled(features.FeatureGateVSphereMultiVCenterDay2)
		// Return false (do not manage) if enabled, true (manage) if not enabled
		return !enabled, nil

	case configv1.AWSPlatformType,
		configv1.AzurePlatformType,
		configv1.GCPPlatformType,
		configv1.OpenStackPlatformType,
		configv1.OvirtPlatformType,
		configv1.KubevirtPlatformType,
		configv1.IBMCloudPlatformType,
		configv1.PowerVSPlatformType,
		configv1.AlibabaCloudPlatformType,
		configv1.NutanixPlatformType,
		configv1.ExternalPlatformType,
		configv1.NonePlatformType,
		configv1.EquinixMetalPlatformType,
		configv1.BareMetalPlatformType:
		// For all other platforms, this controller manages the cloud config
		return true, nil

	default:
		// Unknown platform type, default to managing
		return true, nil
	}
}

// isFeatureGateEnabled checks if the specified feature gate is enabled in the cluster.
// It uses the feature gates that were retrieved during controller initialization.
// If feature gates weren't available at initialization, it returns false as a safe fallback.
func (c *KubeCloudConfigController) isFeatureGateEnabled(gateName configv1.FeatureGateName) bool {
	c.featureGatesMu.RLock()
	defer c.featureGatesMu.RUnlock()

	if c.currentFeatureGates == nil {
		// Feature gates weren't initialized, return safe fallback
		klog.Warningf("unable to check featuregate %v due to currentFeatureGates == nil", gateName)
		return false
	}

	klog.V(4).Infof("is featuregate %v enabled?  %v", gateName, c.currentFeatureGates.Enabled(gateName))
	return c.currentFeatureGates.Enabled(gateName)
}
