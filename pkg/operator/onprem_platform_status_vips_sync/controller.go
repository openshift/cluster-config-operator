package onprem_platform_status_vips_sync

import (
	"context"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	configv1 "github.com/openshift/api/config/v1"
	configv1client "github.com/openshift/client-go/config/clientset/versioned/typed/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
	utilslice "k8s.io/utils/strings/slices"
)

// OnPremPlatformStatusVIPsSyncController is responsible for syncing the new API
// & Ingress VIPs fields with the deprecated API & Ingress VIP fields in the
// infrastructure.config.openshift.io/cluster object to have a consistent API
// between versions
type OnPremPlatformStatusVIPsSyncController struct {
	infraClient configv1client.InfrastructureInterface
	infraLister configlistersv1.InfrastructureLister
}

// NewController returns a new OnPremPlatformStatusVIPsSyncController
func NewController(operatorClient operatorv1helpers.OperatorClient,
	infraClient configv1client.InfrastructuresGetter, infraLister configlistersv1.InfrastructureLister, infraInformer cache.SharedIndexInformer,
	recorder events.Recorder) factory.Controller {
	c := &OnPremPlatformStatusVIPsSyncController{
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
		ToController("OnPremPlatformStatusVIPsSyncController", recorder)
}

func (c OnPremPlatformStatusVIPsSyncController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	obj, err := c.infraLister.Get("cluster")
	if errors.IsNotFound(err) {
		syncCtx.Recorder().Warningf("OnPremPlatformStatusVIPsSyncController", "Required infrastructures.%s/cluster not found", configv1.GroupName)
		return nil
	}
	if err != nil {
		return err
	}

	var apiVIPs, ingressVIPs *[]string // new fields
	var apiVIP, ingressVIP *string     // old/deprecated fields

	currentInfra := obj.DeepCopy()
	switch currentInfra.Status.Platform {
	case configv1.BareMetalPlatformType:
		apiVIPs = &currentInfra.Status.PlatformStatus.BareMetal.APIServerInternalIPs
		apiVIP = &currentInfra.Status.PlatformStatus.BareMetal.APIServerInternalIP
		ingressVIPs = &currentInfra.Status.PlatformStatus.BareMetal.IngressIPs
		ingressVIP = &currentInfra.Status.PlatformStatus.BareMetal.IngressIP

	case configv1.VSpherePlatformType:
		apiVIPs = &currentInfra.Status.PlatformStatus.VSphere.APIServerInternalIPs
		apiVIP = &currentInfra.Status.PlatformStatus.VSphere.APIServerInternalIP
		ingressVIPs = &currentInfra.Status.PlatformStatus.VSphere.IngressIPs
		ingressVIP = &currentInfra.Status.PlatformStatus.VSphere.IngressIP

	case configv1.OpenStackPlatformType:
		apiVIPs = &currentInfra.Status.PlatformStatus.OpenStack.APIServerInternalIPs
		apiVIP = &currentInfra.Status.PlatformStatus.OpenStack.APIServerInternalIP
		ingressVIPs = &currentInfra.Status.PlatformStatus.OpenStack.IngressIPs
		ingressVIP = &currentInfra.Status.PlatformStatus.OpenStack.IngressIP

	case configv1.OvirtPlatformType:
		apiVIPs = &currentInfra.Status.PlatformStatus.Ovirt.APIServerInternalIPs
		apiVIP = &currentInfra.Status.PlatformStatus.Ovirt.APIServerInternalIP
		ingressVIPs = &currentInfra.Status.PlatformStatus.Ovirt.IngressIPs
		ingressVIP = &currentInfra.Status.PlatformStatus.Ovirt.IngressIP

	case configv1.NutanixPlatformType:
		apiVIPs = &currentInfra.Status.PlatformStatus.Nutanix.APIServerInternalIPs
		apiVIP = &currentInfra.Status.PlatformStatus.Nutanix.APIServerInternalIP
		ingressVIPs = &currentInfra.Status.PlatformStatus.Nutanix.IngressIPs
		ingressVIP = &currentInfra.Status.PlatformStatus.Nutanix.IngressIP

	default:
		// nothing to do for this platform type
		return nil
	}

	apiFieldsUpdated, err := syncVIPs(apiVIPs, apiVIP)
	if err != nil {
		syncCtx.Recorder().Warningf("OnPremPlatformStatusVIPsSyncController", "error on syncing api VIP fields: %v", err)
	}

	ingressFieldsUpdated, err := syncVIPs(ingressVIPs, ingressVIP)
	if err != nil {
		syncCtx.Recorder().Warningf("OnPremPlatformStatusVIPsSyncController", "error on syncing ingress VIP fields: %v", err)
	}

	if apiFieldsUpdated || ingressFieldsUpdated {
		_, err = c.infraClient.UpdateStatus(ctx, currentInfra, metav1.UpdateOptions{})
		return err
	}
	return nil
}

// syncVIPs syncs the VIPs according to the following rules:
// | # | Initial value of new field | Initial value of old field | Resulting value of new field | Resulting value of old field | Description |
// | - | -------------------------- | -------------------------- | ---------------------------- | ---------------------------- | ----------- |
// | 1 | empty                      | foo                        | [0]: foo                     | foo                          | `new` is empty, `old` with value: set `new[0]` to value from `old` |
// | 2 | [0]: foo, [1]: bar         | empty                      | [0]: foo, [1]: bar           | foo                          | `new` contains values, `old` is empty: set `old` to value from `new[0]` |
// | 3 | [0]: foo, [1]: bar         | foo                        | [0]: foo, [1]: bar           | foo                          | `new` contains values, `old` contains `new[0]`: we are fine, as `old` is part of `new` |
// | 4 | [0]: foo, [1]: bar         | bar                        | [0]: foo, [1]: bar           | bar                          | `new` contains values, `old` contains `new[1]`: we are fine, as `old` is part of `new` |
// | 5 | [0]: foo, [1]: bar         | baz                        | [0]: foo, [1]: bar           | foo                          | `new` contains values, `old` contains a value which is not included in `new`: new values take precedence over old values, so set `old` to value from `new[0]` (and return a warning) |
//
// it returns if the fields have been changed and in case 5 the error.
func syncVIPs(newVIPs *[]string, oldVIP *string) (bool, error) {
	fieldsChanged := false
	if len(*newVIPs) == 0 {
		if *oldVIP != "" {
			// case 1
			// -> `new` is empty, `old` with value: set `new[0]` to value from `old`
			*newVIPs = []string{*oldVIP}
			fieldsChanged = true
		}
	} else {
		if *oldVIP == "" {
			// case 2
			// -> `new` contains values, `old` is empty: set `old` to value from `new[0]`
			*oldVIP = (*newVIPs)[0]
			fieldsChanged = true
		} else {
			if utilslice.Contains(*newVIPs, *oldVIP) {
				// case 3 & 4
				// -> `new` contains values, `old` contains `new[0]` or `new[1]`
				// -> we are fine, as `old` is part of `new`
			} else {
				// case 5
				// -> `new` contains values, `old` contains a value which is not
				// included in `new`: new values take precedence over old
				// values, so set `old` to value from `new[0]` (and return a
				// warning)
				err := fmt.Errorf("old (%s) and new VIPs (%s) were both set and differed. New VIPs field will take precedence.", *oldVIP, strings.Join(*newVIPs, ", "))
				*oldVIP = (*newVIPs)[0]
				return true, err
			}
		}
	}

	return fieldsChanged, nil
}
