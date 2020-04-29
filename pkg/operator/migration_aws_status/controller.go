package migration_aws_status

import (
	"context"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	configv1listers "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/yaml"
)

const (
	clusterConfigNamespace = "kube-system"
	clusterConfigName      = "cluster-config-v1"
	clusterConfigKey       = "install-config"
)

// MigrationAWSStatusController migrates the existing `infrastructure.config.openshift.io/v1` `cluster` objects
// for AWS to include the `AWSPlatformStatus` based on the InstallConfig stored in the ConfigMap `kube-system/cluster-config-v1`.
// BZ: https://bugzilla.redhat.com/show_bug.cgi?id=1814332
// The controller reads the configmap for the `install-config.yaml` and then creates a `AWSPlatformStatus` and updates the infrastructure object with these values.
// Here are the values that are migrated by the controller:
// - `.status.platformStatus.type` = `.status.platformType`
// - `.status.platformStatus.aws.region` = AWS region from InstallConfig's `.platform.aws.region`
//
// The controller does no-op for non-AWS platforms.
// It uses the `.status.platformType` from infrastructure object to identify the platform.
type MigrationAWSStatusController struct {
	infraClient     configv1client.InfrastructureInterface
	infraLister     configv1listers.InfrastructureLister
	configMapClient corev1client.ConfigMapsGetter
}

// NewController returns a MigrationAWSStatusController
func NewController(operatorClient operatorv1helpers.OperatorClient,
	infraClient configv1client.InfrastructuresGetter, infraLister configv1listers.InfrastructureLister, infraInformer cache.SharedIndexInformer,
	configMapClient corev1client.ConfigMapsGetter,
	kubeSystemInformer cache.SharedIndexInformer,
	recorder events.Recorder) factory.Controller {
	c := &MigrationAWSStatusController{
		infraClient:     infraClient.Infrastructures(),
		infraLister:     infraLister,
		configMapClient: configMapClient,
	}
	return factory.New().
		WithInformers(
			operatorClient.Informer(),
			infraInformer,
			kubeSystemInformer,
		).
		WithSync(c.sync).
		WithSyncDegradedOnError(operatorClient).
		ResyncEvery(time.Minute).
		ToController("MigrationAWSStatusController", recorder)
}

func (c MigrationAWSStatusController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	obji, err := c.infraLister.Get("cluster")
	if errors.IsNotFound(err) {
		syncCtx.Recorder().Warningf("MigrationAWSStatusController", "Required infrastructures.%s/cluster not found", configv1.GroupName)
		return nil
	}
	if err != nil {
		return err
	}

	currentInfra := obji.DeepCopy()

	if currentInfra.Status.Platform != configv1.AWSPlatformType || // not aws
		(currentInfra.Status.PlatformStatus != nil && currentInfra.Status.PlatformStatus.Type != configv1.AWSPlatformType) || // not aws
		(currentInfra.Status.PlatformStatus != nil && currentInfra.Status.PlatformStatus.Type == configv1.AWSPlatformType &&
			currentInfra.Status.PlatformStatus.AWS != nil && len(currentInfra.Status.PlatformStatus.AWS.Region) > 0) { // aws, but the region is already set. so no need to migrate
		return nil // no action
	}

	cc, err := loadClusterConfig(ctx, c.configMapClient)
	if err != nil {
		syncCtx.Recorder().Warningf("MigrationAWSStatusController", "Unable to load the cluster-config-v1")
		return err
	}
	if cc.Platform.AWS == nil {
		return fmt.Errorf("no AWS configuration found in cluster-config-v1")
	}
	if len(cc.Platform.AWS.Region) == 0 {
		return fmt.Errorf("empty region set in cluster-config-v1")
	}

	currentInfra.Status.PlatformStatus = &configv1.PlatformStatus{
		Type: configv1.AWSPlatformType,
		AWS:  &configv1.AWSPlatformStatus{Region: cc.Platform.AWS.Region},
	}
	_, err = c.infraClient.UpdateStatus(ctx, currentInfra, metav1.UpdateOptions{})
	if err != nil {
		syncCtx.Recorder().Warningf("MigrationAWSStatusController", "Unable to update the infrastructure status")
		return err
	}
	return nil
}

func loadClusterConfig(ctx context.Context, client corev1client.ConfigMapsGetter) (installConfig, error) {
	obj, err := client.ConfigMaps(clusterConfigNamespace).Get(ctx, clusterConfigName, metav1.GetOptions{})
	if err != nil {
		return installConfig{}, err
	}
	configRaw, ok := obj.Data[clusterConfigKey]
	if !ok {
		return installConfig{}, fmt.Errorf("%s key doesn't exist in ConfigMap %s/%s", clusterConfigKey, clusterConfigNamespace, clusterConfigName)
	}

	config := installConfig{}
	if err := yaml.Unmarshal([]byte(configRaw), &config); err != nil {
		return installConfig{}, fmt.Errorf("unable to parse install-config.yaml: %s", err)
	}
	return config, nil
}

type installConfig struct {
	Platform struct {
		AWS *struct {
			Region string `json:"region"`
		} `json:"aws,omitempty"`
	} `json:"platform"`
}
