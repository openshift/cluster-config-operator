package operator

import (
	"context"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned"
	configv1informers "github.com/openshift/client-go/config/informers/externalversions"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/openshift/library-go/pkg/operator/genericoperatorclient"

	"github.com/openshift/cluster-config-operator/pkg/operator/aws_platform_service_location"
)

func RunOperator(ctx context.Context, controllerContext *controllercmd.ControllerContext) error {
	configClient, err := configv1client.NewForConfig(controllerContext.KubeConfig)
	if err != nil {
		return err
	}
	configInformers := configv1informers.NewSharedInformerFactory(configClient, 10*time.Minute)
	operatorClient, _, err := genericoperatorclient.NewStaticPodOperatorClient(controllerContext.KubeConfig, operatorv1.GroupVersion.WithResource("configoperators"))
	if err != nil {
		return err
	}

	infraController := aws_platform_service_location.NewController(operatorClient, configClient.ConfigV1(), configInformers.Config().V1().Infrastructures().Informer(), controllerContext.EventRecorder)

	go infraController.Run(ctx, 1)

	<-ctx.Done()
	return nil
}
