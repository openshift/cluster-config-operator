package operator

import (
	"context"
	"os"
	"time"

	"github.com/davecgh/go-spew/spew"
	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned"
	configv1informers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/cluster-config-operator/pkg/operator/aws_platform_service_location"
	"github.com/openshift/cluster-config-operator/pkg/operator/featuregates"
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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func RunOperator(ctx context.Context, controllerContext *controllercmd.ControllerContext) error {
	operatorVersion := os.Getenv("OPERATOR_IMAGE_VERSION")

	// This kube client use protobuf, do not use it for CR
	kubeClient, err := kubernetes.NewForConfig(controllerContext.ProtoKubeConfig)
	if err != nil {
		return err
	}
	configClient, err := configv1client.NewForConfig(controllerContext.KubeConfig)
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
	operatorClient, dynamicInformers, err := genericoperatorclient.NewClusterScopedOperatorClient(controllerContext.KubeConfig, operatorv1.GroupVersion.WithResource("configs"))
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
	versionRecorder.SetVersion("operator", operatorVersion)

	featureGateController := featuregates.NewFeatureGateController(
		operatorClient,
		operatorVersion,
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

	<-ctx.Done()
	return nil
}
