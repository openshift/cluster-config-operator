package kube_cloud_config

import (
	"context"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	configv1listers "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/cluster-config-operator/pkg/operator/operatorclient"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
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

	// transformers stores per platform tranformer
	cloudConfigTransformers map[configv1.PlatformType]cloudConfigTransformer
}

// NewController returns a KubeCloudConfigController
func NewController(operatorClient operatorv1helpers.OperatorClient,
	infraClient configv1client.InfrastructuresGetter, infraLister configv1listers.InfrastructureLister, infraInformer cache.SharedIndexInformer,
	configMapClient corev1client.ConfigMapsGetter,
	openshiftConfigConfigMapInformer cache.SharedIndexInformer, openshiftConfigManagedConfigMapInformer cache.SharedIndexInformer,
	recorder events.Recorder) factory.Controller {
	c := &KubeCloudConfigController{
		infraClient:     infraClient.Infrastructures(),
		infraLister:     infraLister,
		configMapClient: configMapClient,
		cloudConfigTransformers: map[configv1.PlatformType]cloudConfigTransformer{
			configv1.AWSPlatformType: awsTransformer,
		},
	}
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

func (c KubeCloudConfigController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	obj, err := c.infraLister.Get("cluster")
	if errors.IsNotFound(err) {
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
		if err != nil && !errors.IsNotFound(err) {
			return err
		}
		if err == nil {
			syncCtx.Recorder().Eventf("KubeCloudConfigController", "%s/%s ConfigMap was deleted as no longer required", operatorclient.GlobalMachineSpecifiedConfigNamespace, targetCloudConfigMap)
		}
	} else { // apply the target
		target.Name = targetCloudConfigMap
		target.Namespace = operatorclient.GlobalMachineSpecifiedConfigNamespace
		_, updated, err := resourceapply.ApplyConfigMap(c.configMapClient, syncCtx.Recorder(), target)
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
