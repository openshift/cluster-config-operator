package aws_resource_tags

import (
	"context"
	"fmt"
	"regexp"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	configv1listers "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/cache"
)

// tagRegex is used to check that the keys and values of a tag contain only valid characters
var tagRegex = regexp.MustCompile(`^[0-9A-Za-z_.:/=+-@]*$`)
// kubernetesNamespaceRegex is used to check that a tag key is not in the kubernetes.io namespace
var kubernetesNamespaceRegex = regexp.MustCompile(`^([^/]*\.)?kubernetes.io/`)
// openshiftNamespaceRegex is used to check that a tag key is not in the openshift.io namespace
var openshiftNamespaceRegex = regexp.MustCompile(`^([^/]*\.)?openshift.io/`)

// AWSResourceTagsController syncs the AWS resource tags from the spec of the `infrastructure.config.openshift.io/v1`
// `cluster` object to the status.
type AWSResourceTagsController struct {
	infraClient configv1client.InfrastructureInterface
	infraLister configv1listers.InfrastructureLister
}

// NewController returns a AWSResourceTagsController
func NewController(operatorClient operatorv1helpers.OperatorClient,
	infraClient configv1client.InfrastructuresGetter, infraLister configv1listers.InfrastructureLister, infraInformer cache.SharedIndexInformer,
	recorder events.Recorder) factory.Controller {
	c := &AWSResourceTagsController{
		infraClient: infraClient.Infrastructures(),
		infraLister: infraLister,
	}
	return factory.New().
		WithInformers(
			operatorClient.Informer(),
			infraInformer,
		).
		WithSync(c.sync).
		WithSyncDegradedOnError(operatorClient).
		ResyncEvery(time.Minute).
		ToController("AWSResourceTagsController", recorder)
}

func (c AWSResourceTagsController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	obji, err := c.infraLister.Get("cluster")
	if errors.IsNotFound(err) {
		syncCtx.Recorder().Warningf("AWSResourceTagsController", "Required infrastructures.%s/cluster not found", configv1.GroupName)
		return nil
	}
	if err != nil {
		return err
	}

	currentInfra := obji.DeepCopy()

	var desiredResourceTags []configv1.AWSResourceTag
	if awsSpec := currentInfra.Spec.PlatformSpec.AWS; awsSpec != nil {
		keys := sets.NewString()
		desiredResourceTags = make([]configv1.AWSResourceTag, 0, len(awsSpec.ResourceTags))
		for _, tag := range awsSpec.ResourceTags {
			if err := validateTag(tag); err != nil {
				syncCtx.Recorder().Warningf("AWSResourceTagsController", "The resource tag with key=%q and value=%q is invalid: %v", tag.Key, tag.Value, err)
				continue
			}
			if keys.Has(tag.Key) {
				syncCtx.Recorder().Warningf("AWSResourceTagsController", "The resource tag with key=%q and value=%q is a duplicate", tag.Key, tag.Value)
				continue
			}
			keys.Insert(tag.Key)
			desiredResourceTags = append(desiredResourceTags, tag)
		}
	}

	var currentResourceTags []configv1.AWSResourceTag
	if currentInfra.Status.PlatformStatus != nil &&
		currentInfra.Status.PlatformStatus.AWS != nil {
		currentResourceTags = currentInfra.Status.PlatformStatus.AWS.ResourceTags
	}

	if len(desiredResourceTags) == 0 && len(currentResourceTags) == 0 {
		return nil
	}

	if equality.Semantic.DeepEqual(desiredResourceTags, currentResourceTags) {
		return nil
	}

	if currentInfra.Status.PlatformStatus == nil {
		currentInfra.Status.PlatformStatus = &configv1.PlatformStatus{}
	}
	if currentInfra.Status.PlatformStatus.AWS == nil {
		currentInfra.Status.PlatformStatus.AWS = &configv1.AWSPlatformStatus{}
	}
	currentInfra.Status.PlatformStatus.AWS.ResourceTags = desiredResourceTags

	_, err = c.infraClient.UpdateStatus(ctx, currentInfra, metav1.UpdateOptions{})
	if err != nil {
		syncCtx.Recorder().Warningf("AWSResourceTagsController", "Unable to update the infrastructure status")
		return err
	}
	return nil
}

// validateTag checks the following things to ensure that the tag is acceptable as an additional tag.
// * The key and value contain only valid characters.
// * The key is not empty and at most 128 characters.
// * The value is not empty and at most 256 characters. Note that, while many AWS services accept empty tag values,
//   the additional tags may be applied to resources in services that do not accept empty tag values. Consequently,
//   OpenShift cannot accept empty tag values.
// * The key is not in the kubernetes.io namespace.
// * The key is not in the openshift.io namespace.
func validateTag(tag configv1.AWSResourceTag) error {
	if !tagRegex.MatchString(tag.Key) {
		return fmt.Errorf("key contains invalid characters")
	}
	if !tagRegex.MatchString(tag.Value) {
		return fmt.Errorf("value contains invalid characters")
	}
	if len(tag.Key) == 0 {
		return fmt.Errorf("key is empty")
	}
	if len(tag.Key) > 128 {
		return fmt.Errorf("key is too long")
	}
	if len(tag.Value) == 0 {
		return fmt.Errorf("value is empty")
	}
	if len(tag.Value) > 256 {
		return fmt.Errorf("value is too long")
	}
	if kubernetesNamespaceRegex.MatchString(tag.Key) {
		return fmt.Errorf("key is in the kubernetes.io namespace")
	}
	if openshiftNamespaceRegex.MatchString(tag.Key) {
		return fmt.Errorf("key is in the openshift.io namespace")
	}
	return nil
}
