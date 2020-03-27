package operator

import (
	"context"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1 "github.com/openshift/api/operator/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned"
	configv1informers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/operator/genericoperatorclient"
	"github.com/openshift/library-go/pkg/operator/status"

	"github.com/openshift/cluster-config-operator/pkg/operator/aws_platform_service_location"
	"github.com/openshift/library-go/pkg/operator/loglevel"
)

func RunOperator(ctx context.Context, controllerContext *controllercmd.ControllerContext) error {
	configClient, err := configv1client.NewForConfig(controllerContext.KubeConfig)
	if err != nil {
		return err
	}
	configInformers := configv1informers.NewSharedInformerFactory(configClient, 10*time.Minute)

	operatorClient, dynamicInformers, err := genericoperatorclient.NewClusterScopedOperatorClient(controllerContext.KubeConfig, operatorv1.GroupVersion.WithResource("configs"))
	if err != nil {
		return err
	}

	infraController := aws_platform_service_location.NewController(
		operatorClient,
		configClient.ConfigV1(),
		configInformers.Config().V1().Infrastructures().Informer(),
		controllerContext.EventRecorder,
	)

	// don't change any versions until we sync
	versionRecorder := status.NewVersionGetter()
	clusterOperator, err := configClient.ConfigV1().ClusterOperators().Get(ctx, "config-operator", metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	for _, version := range clusterOperator.Status.Versions {
		versionRecorder.SetVersion(version.Name, version.Version)
	}
	versionRecorder.SetVersion("operator", os.Getenv("OPERATOR_IMAGE_VERSION"))

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

	go dynamicInformers.Start(ctx.Done())
	go configInformers.Start(ctx.Done())

	go infraController.Run(ctx, 1)
	go logLevelController.Run(ctx, 1)
	go statusController.Run(ctx, 1)

	<-ctx.Done()
	return nil
}
