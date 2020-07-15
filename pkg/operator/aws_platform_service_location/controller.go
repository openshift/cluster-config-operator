package aws_platform_service_location

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/tools/cache"

	configv1 "github.com/openshift/api/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
)

// AWSPlatformServiceLocationController is responsible for syncing and validating the service endpoints for AWS APIs
// provided by the user using the infrastructure.config.openshift.io/cluster object.
type AWSPlatformServiceLocationController struct {
	infraClient configv1client.InfrastructureInterface
	infraLister configlistersv1.InfrastructureLister
}

// NewController returns a new AWSPlatformServiceLocationController.
func NewController(operatorClient operatorv1helpers.OperatorClient,
	infraClient configv1client.InfrastructuresGetter, infraLister configlistersv1.InfrastructureLister, infraInformer cache.SharedIndexInformer,
	recorder events.Recorder) factory.Controller {
	c := &AWSPlatformServiceLocationController{
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
		ToController("AWSPlatformServiceLocationController", recorder)
}

func (c AWSPlatformServiceLocationController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	obj, err := c.infraLister.Get("cluster")
	if errors.IsNotFound(err) {
		syncCtx.Recorder().Warningf("AWSPlatformServiceLocationController", "Required infrastructures.%s/cluster not found", configv1.GroupName)
		return nil
	}
	if err != nil {
		return err
	}

	currentInfra := obj.DeepCopy()
	var platformName configv1.PlatformType
	if pstatus := currentInfra.Status.PlatformStatus; pstatus != nil {
		platformName = pstatus.Type
	}
	if len(platformName) == 0 {
		syncCtx.Recorder().Warningf("AWSPlatformServiceLocationController", "Falling back to deprecated status.platform because infrastructures.%s/cluster status.platformStatus.type is empty", configv1.GroupName)
		platformName = currentInfra.Status.Platform
	}
	if platformName != configv1.AWSPlatformType {
		return nil // nothing to do here.
	}

	if currentInfra.Spec.PlatformSpec.Type != "" && currentInfra.Spec.PlatformSpec.Type != platformName {
		return field.Invalid(field.NewPath("spec", "platformSpec", "type"), currentInfra.Spec.PlatformSpec.Type, fmt.Sprint("non AWS platform type set in specification"))
	}

	var services []configv1.AWSServiceEndpoint
	if currentInfra.Spec.PlatformSpec.AWS != nil {
		services = append(services, currentInfra.Spec.PlatformSpec.AWS.ServiceEndpoints...)
	}

	if err := validateServiceEndpoints(services); err != nil {
		syncCtx.Recorder().Warningf("AWSPlatformServiceLocationController", "Invalid spec.platformSpec.aws.serviceEndpoints provided for infrastructures.%s/cluster", configv1.GroupName)
		return err
	}
	sort.Slice(services, func(i, j int) bool {
		return services[i].Name < services[j].Name
	})

	var existingServices []configv1.AWSServiceEndpoint
	if currentInfra.Status.PlatformStatus != nil && currentInfra.Status.PlatformStatus.AWS != nil {
		existingServices = append(existingServices, currentInfra.Status.PlatformStatus.AWS.ServiceEndpoints...)
	}
	if equality.Semantic.DeepEqual(existingServices, services) {
		return nil // nothing to do now
	}

	if currentInfra.Status.PlatformStatus == nil {
		currentInfra.Status.PlatformStatus = &configv1.PlatformStatus{}
	}
	if currentInfra.Status.PlatformStatus.AWS == nil {
		currentInfra.Status.PlatformStatus.AWS = &configv1.AWSPlatformStatus{}
	}
	currentInfra.Status.PlatformStatus.AWS.ServiceEndpoints = services
	_, err = c.infraClient.UpdateStatus(ctx, currentInfra, metav1.UpdateOptions{})
	return err
}

func validateServiceEndpoints(endpoints []configv1.AWSServiceEndpoint) error {
	fldPath := field.NewPath("spec", "platformSpec", "aws", "serviceEndpoints")

	allErrs := field.ErrorList{}
	tracker := map[string]int{}
	for idx, e := range endpoints {
		fldp := fldPath.Index(idx)
		if eidx, ok := tracker[e.Name]; ok {
			allErrs = append(allErrs, field.Invalid(fldp.Child("name"), e.Name, fmt.Sprintf("duplicate service endpoint not allowed for %s, service endpoint already defined at %s", e.Name, fldPath.Index(eidx))))
		} else {
			tracker[e.Name] = idx
		}

		if err := validateServiceURL(e.URL); err != nil {
			allErrs = append(allErrs, field.Invalid(fldp.Child("url"), e.URL, err.Error()))
		}
	}
	return allErrs.ToAggregate()
}

var schemeRE = regexp.MustCompile("^([^:]+)://")

func validateServiceURL(uri string) error {
	u, err := url.Parse(uri)
	if err != nil {
		return err
	}
	if u.Hostname() == "" {
		return fmt.Errorf("host cannot be empty, empty host provided")
	}
	if s := u.Scheme; s != "https" {
		return fmt.Errorf("invalid scheme %s, only https allowed", s)
	}
	if r := u.RequestURI(); r != "/" {
		return fmt.Errorf("no path or request parameters must be provided, %q was provided", r)
	}

	return nil
}
