package aws_platform_service_location

import (
	"context"
	"time"

	"github.com/davecgh/go-spew/spew"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"

	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
)

type AWSPlatformServiceLocationController struct {
	operatorClient operatorv1helpers.OperatorClient
	infraClient    configv1client.InfrastructuresGetter
}

func NewController(operatorClient operatorv1helpers.OperatorClient, infraClient configv1client.InfrastructuresGetter, infraInformer cache.SharedIndexInformer, recorder events.Recorder) factory.Controller {
	c := &AWSPlatformServiceLocationController{
		operatorClient: operatorClient,
		infraClient:    infraClient,
	}
	return factory.New().
		WithInformers(
			operatorClient.Informer(),
			infraInformer,
		).
		WithSync(c.sync).
		WithSyncDegradedOnError(operatorClient).
		ResyncEvery(time.Minute).
		ToController("AWSPlatformServiceLocationController", recorder)
}

func (c AWSPlatformServiceLocationController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	currentInfra, err := c.infraClient.Infrastructures().Get(ctx, "cluster", metav1.GetOptions{})
	if errors.IsNotFound(err) {
		// log not found errors, but don't degrade?
		klog.Warningf("%w", err)
		return nil
	}
	klog.Infof(spew.Sprintf("current state: %s", currentInfra.Status))
	return err
}
