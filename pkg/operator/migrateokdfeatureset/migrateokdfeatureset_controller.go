package migrateokdfeatureset

import (
	"context"
	"fmt"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	applyconfigurationsconfigv1 "github.com/openshift/client-go/config/applyconfigurations/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	v1 "github.com/openshift/client-go/config/informers/externalversions/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/cluster-config-operator/pkg/version"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// OKDFeatureSetMigrationController migrates Default featureset to OKD for OKD builds
type OKDFeatureSetMigrationController struct {
	featureGatesClient configv1client.FeatureGatesGetter
	featureGatesLister configlistersv1.FeatureGateLister

	eventRecorder events.Recorder
}

func NewOKDFeatureSetMigrationController(operatorClient operatorv1helpers.OperatorClient,
	featureGatesClient configv1client.FeatureGatesGetter, featureGatesInformer v1.FeatureGateInformer,
	eventRecorder events.Recorder) factory.Controller {
	c := &OKDFeatureSetMigrationController{
		featureGatesClient: featureGatesClient,
		featureGatesLister: featureGatesInformer.Lister(),
		eventRecorder:      eventRecorder,
	}

	return factory.New().
		WithSync(c.sync).
		WithSyncDegradedOnError(operatorClient).
		ResyncEvery(time.Minute).
		ToController("OKDFeatureSetMigrationController", eventRecorder)
}

func (c OKDFeatureSetMigrationController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	// Only run this controller for OKD builds
	if !version.IsSCOS() {
		return nil
	}

	featureGates, err := c.featureGatesLister.Get("cluster")
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("unable to get FeatureGate: %w", err)
	}

	return c.syncFeatureGate(ctx, featureGates)
}

func (c OKDFeatureSetMigrationController) syncFeatureGate(ctx context.Context, featureGates *configv1.FeatureGate) error {
	// Only migrate if the current featureset is Default (empty string or explicit "Default")
	// The installer creates FeatureGate with OKD featureset for new installations.
	// This controller migrates existing clusters upgraded to OKD.
	if featureGates.Spec.FeatureSet != "" && featureGates.Spec.FeatureSet != configv1.Default {
		return nil
	}

	desiredFeatureGate := applyconfigurationsconfigv1.FeatureGate("cluster").
		WithSpec(
			applyconfigurationsconfigv1.FeatureGateSpec().
				WithFeatureSet(configv1.OKD),
		)
	applyOptions := metav1.ApplyOptions{
		Force:        true,
		FieldManager: "OKDFeatureSetMigrationController",
	}

	if _, err := c.featureGatesClient.FeatureGates().Apply(ctx, desiredFeatureGate, applyOptions); err != nil {
		return fmt.Errorf("unable to migrate FeatureGate to OKD: %w", err)
	}

	return nil
}
