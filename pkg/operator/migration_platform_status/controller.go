package migration_platform_status

import (
	"context"
	"errors"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	configv1listers "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
	"k8s.io/apimachinery/pkg/api/equality"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
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

// MigrationPlatformStatusController migrates the existing `infrastructure.config.openshift.io/v1` `cluster` objects
// to include the `PlatformStatus` based on the InstallConfig stored in the ConfigMap `kube-system/cluster-config-v1`.
// BZ: https://bugzilla.redhat.com/show_bug.cgi?id=1814332
// The controller reads the configmap for the `install-config.yaml` and then creates a `PlatformStatus` and updates the infrastructure object with these values.
//
// It uses the `.status.platformType` from infrastructure object to identify the platform.
type MigrationPlatformStatusController struct {
	infraClient     configv1client.InfrastructureInterface
	infraLister     configv1listers.InfrastructureLister
	configMapClient corev1client.ConfigMapsGetter
}

// NewController returns a MigrationPlatformStatusController
func NewController(operatorClient operatorv1helpers.OperatorClient,
	infraClient configv1client.InfrastructuresGetter, infraLister configv1listers.InfrastructureLister, infraInformer cache.SharedIndexInformer,
	configMapClient corev1client.ConfigMapsGetter,
	kubeSystemInformer cache.SharedIndexInformer,
	recorder events.Recorder) factory.Controller {
	c := &MigrationPlatformStatusController{
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
		ToController("MigrationPlatformStatusController", recorder)
}

func (c MigrationPlatformStatusController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	obji, err := c.infraLister.Get("cluster")
	if kerrors.IsNotFound(err) {
		syncCtx.Recorder().Warningf("MigrationPlatformStatusController", "Required infrastructures.%s/cluster not found", configv1.GroupName)
		return nil
	}
	if err != nil {
		return err
	}

	currentInfra := obji.DeepCopy()

	if currentInfra.Status.PlatformStatus == nil {
		currentInfra.Status.PlatformStatus = &configv1.PlatformStatus{}
	}
	if currentInfra.Status.PlatformStatus.Type == "" {
		currentInfra.Status.PlatformStatus.Type = currentInfra.Status.Platform
	}
	if old, new := currentInfra.Status.Platform, currentInfra.Status.PlatformStatus.Type; old != "" && new != "" && old != new {
		message := fmt.Sprintf("Mis-match between status.platform (%s) and status.platformStatus.type (%s) in infrastructures.%s/cluster", old, new, configv1.GroupName)
		syncCtx.Recorder().Warningf("MigrationPlatformStatusController", message)
		return errors.New(message)
	}

	if err := c.migratePlatformSpecificFields(ctx, currentInfra); err != nil {
		syncCtx.Recorder().Warningf("MigrationPlatformStatusController", err.Error())
		return err
	}

	if equality.Semantic.DeepEqual(obji.Status.PlatformStatus, currentInfra.Status.PlatformStatus) {
		// no changes made to platform status
		return nil
	}

	_, err = c.infraClient.UpdateStatus(ctx, currentInfra, metav1.UpdateOptions{})
	if err != nil {
		syncCtx.Recorder().Warningf("MigrationPlatformStatusController", "Unable to update the infrastructure status")
		return err
	}
	return nil
}

func (c MigrationPlatformStatusController) migratePlatformSpecificFields(ctx context.Context, currentInfra *configv1.Infrastructure) error {
	if currentInfra.Status.PlatformStatus.Type != configv1.AWSPlatformType {
		// only AWS has platform-specific fields to migrate
		return nil
	}

	if currentInfra.Status.PlatformStatus.AWS == nil {
		currentInfra.Status.PlatformStatus.AWS = &configv1.AWSPlatformStatus{}
	}

	if currentInfra.Status.PlatformStatus.AWS.Region != "" {
		// region is already set, so no need to migrate
		return nil
	}

	cc, err := loadClusterConfig(ctx, c.configMapClient)
	if err != nil {
		return err
	}

	if cc.Platform.AWS == nil {
		return fmt.Errorf("no AWS configuration found in cluster-config-v1")
	}
	if len(cc.Platform.AWS.Region) == 0 {
		return fmt.Errorf("empty region set in cluster-config-v1")
	}

	currentInfra.Status.PlatformStatus.AWS.Region = cc.Platform.AWS.Region

	return nil
}

func loadClusterConfig(ctx context.Context, client corev1client.ConfigMapsGetter) (installConfig, error) {
	obj, err := client.ConfigMaps(clusterConfigNamespace).Get(ctx, clusterConfigName, metav1.GetOptions{})
	if err != nil {
		return installConfig{}, fmt.Errorf("Unable to load the cluster-config-v1: %w", err)
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
