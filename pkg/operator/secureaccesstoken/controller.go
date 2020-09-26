package secureaccesstoken

import (
	"context"
	"time"

	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const secureTokenStorageAnnotationKey = "oauth-apiserver.openshift.io/secure-token-storage"

// Controller removes the "oauth-apiserver.openshift.io/secure-token-storage"
// annotation from the APIServer config/v1 object after a potential downgrade from 4.6.
type Controller struct {
	apiserverClient configv1client.APIServerInterface
	apiserverLister configlistersv1.APIServerLister
}

// NewController returns a new secure access token annotation removal controller.
func NewController(operatorClient operatorv1helpers.OperatorClient,
	apiserverClient configv1client.APIServerInterface, apiserverLister configlistersv1.APIServerLister, apiserverInformer factory.Informer,
	recorder events.Recorder) factory.Controller {
	c := &Controller{
		apiserverClient: apiserverClient,
		apiserverLister: apiserverLister,
	}
	return factory.New().
		WithInformers(apiserverInformer).
		WithSync(c.sync).
		WithSyncDegradedOnError(operatorClient).
		ResyncEvery(time.Minute).
		ToController("SecureAccessTokenAnnotationController", recorder)
}

func (c Controller) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	obj, err := c.apiserverLister.Get("cluster")
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	if _, ok := obj.Annotations[secureTokenStorageAnnotationKey]; !ok {
		return nil
	}

	updated := obj.DeepCopy()
	delete(updated.Annotations, secureTokenStorageAnnotationKey)

	_, err = c.apiserverClient.Update(ctx, updated, metav1.UpdateOptions{})
	return err
}
