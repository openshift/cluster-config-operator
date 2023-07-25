package removelatencysensitive

import (
	"context"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	applyconfigurationsconfigv1 "github.com/openshift/client-go/config/applyconfigurations/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	v1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LatencySensitiveRemovalController clears the LatencySensitive featuregate value
type LatencySensitiveRemovalController struct {
	featureGatesClient configv1client.FeatureGatesGetter
	featureGatesLister configlistersv1.FeatureGateLister

	eventRecorder events.Recorder
}

func NewLatencySensitiveRemovalController(operatorClient operatorv1helpers.OperatorClient,
	featureGatesClient configv1client.FeatureGatesGetter, featureGatesInformer v1.FeatureGateInformer,
	eventRecorder events.Recorder) factory.Controller {
	c := &LatencySensitiveRemovalController{
		featureGatesClient: featureGatesClient,
		featureGatesLister: featureGatesInformer.Lister(),
		eventRecorder:      eventRecorder,
	}

	return factory.New().
		WithSync(c.sync).
		WithSyncDegradedOnError(operatorClient).
		ResyncEvery(time.Minute).
		ToController("LatencySensitiveRemovalController", eventRecorder)
}

func (c LatencySensitiveRemovalController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	featureGates, err := c.featureGatesLister.Get("cluster")
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("unable to get FeatureGate: %w", err)
	}

	return c.syncFeatureGate(ctx, featureGates)
}

func (c LatencySensitiveRemovalController) syncFeatureGate(ctx context.Context, featureGates *configv1.FeatureGate) error {
	if featureGates.Spec.FeatureSet != "LatencySensitive" {
		return nil
	}

	desiredFeatureGate := applyconfigurationsconfigv1.FeatureGate("cluster").
		WithSpec(
			applyconfigurationsconfigv1.FeatureGateSpec().
				WithFeatureSet(configv1.Default),
		)
	applyOptions := metav1.ApplyOptions{
		Force:        true,
		FieldManager: "LatencySensitiveRemovalController",
	}

	if _, err := c.featureGatesClient.FeatureGates().Apply(ctx, desiredFeatureGate, applyOptions); err != nil {
		return fmt.Errorf("unable to remove LatencySensitive: %w", err)
	}

	return nil
}
