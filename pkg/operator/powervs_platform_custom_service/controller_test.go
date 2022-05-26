package powervs_platform_custom_service

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	configv1 "github.com/openshift/api/config/v1"
	configfakeclient "github.com/openshift/client-go/config/clientset/versioned/fake"
	configv1listers "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
)

func modifier(orig *configv1.Infrastructure, modFn func(*configv1.Infrastructure)) *configv1.Infrastructure {
	copy := orig.DeepCopy()
	modFn(copy)
	return copy
}

func Test_sync(t *testing.T) {
	basicObj := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec:       configv1.InfrastructureSpec{PlatformSpec: configv1.PlatformSpec{Type: configv1.PowerVSPlatformType}},
		Status: configv1.InfrastructureStatus{
			Platform: configv1.PowerVSPlatformType,
			PlatformStatus: &configv1.PlatformStatus{
				Type:    configv1.PowerVSPlatformType,
				PowerVS: &configv1.PowerVSPlatformStatus{Region: "us-south"},
			},
		},
	}
	validEndpoints := modifier(basicObj, func(i *configv1.Infrastructure) {
		i.Spec.PlatformSpec.PowerVS = &configv1.PowerVSPlatformSpec{
			ServiceEndpoints: []configv1.PowerVSServiceEndpoint{
				{
					Name: "iam",
					URL:  "https://iam.test.cloud.ibm.com",
				},
				{
					Name: "pe",
					URL:  "https://dal.power-iaas.test.cloud.ibm.com",
				},
			},
		}
	})
	cases := []struct {
		obj              *configv1.Infrastructure
		expectedActions  int
		expectedServices []configv1.PowerVSServiceEndpoint
		expectedErr      string
	}{
		{
			obj:              basicObj,
			expectedActions:  0,
			expectedServices: nil,
			expectedErr:      "",
		}, {
			obj: modifier(basicObj, func(i *configv1.Infrastructure) {
				i.ObjectMeta.Name = "something else"
			}),
			expectedActions:  0,
			expectedServices: nil,
			expectedErr:      "",
		}, {
			obj: modifier(basicObj, func(i *configv1.Infrastructure) {
				i.Spec.PlatformSpec.Type = configv1.NonePlatformType
			}),
			expectedActions:  0,
			expectedServices: nil,
			expectedErr:      `spec\.platformSpec\.type: Invalid value: "None": non Power VS platform type set in specification`,
		}, {
			obj: modifier(basicObj, func(i *configv1.Infrastructure) {
				i.Spec.PlatformSpec.PowerVS = &configv1.PowerVSPlatformSpec{}
			}),
			expectedActions:  0,
			expectedServices: nil,
			expectedErr:      "",
		}, {
			obj: modifier(basicObj, func(i *configv1.Infrastructure) {
				i.Status.PlatformStatus = nil
			}),
			expectedActions:  0,
			expectedServices: nil,
			expectedErr:      "",
		}, {
			obj: modifier(basicObj, func(i *configv1.Infrastructure) {
				i.Status.PlatformStatus.Type = configv1.NonePlatformType
			}),
			expectedActions:  0,
			expectedServices: nil,
			expectedErr:      "",
		}, {
			obj:              validEndpoints,
			expectedActions:  1,
			expectedServices: []configv1.PowerVSServiceEndpoint{{Name: "iam", URL: "https://iam.test.cloud.ibm.com"}, {Name: "pe", URL: "https://dal.power-iaas.test.cloud.ibm.com"}},
			expectedErr:      "",
		}, {
			obj: modifier(validEndpoints, func(i *configv1.Infrastructure) {
				sort.Slice(i.Spec.PlatformSpec.PowerVS.ServiceEndpoints, func(x, y int) bool {
					return i.Spec.PlatformSpec.PowerVS.ServiceEndpoints[x].Name > i.Spec.PlatformSpec.PowerVS.ServiceEndpoints[y].Name
				})
			}),
			expectedActions:  1,
			expectedServices: []configv1.PowerVSServiceEndpoint{{Name: "iam", URL: "https://iam.test.cloud.ibm.com"}, {Name: "pe", URL: "https://dal.power-iaas.test.cloud.ibm.com"}},
			expectedErr:      "",
		}, {
			obj: modifier(validEndpoints, func(i *configv1.Infrastructure) {
				i.Spec.PlatformSpec.PowerVS.ServiceEndpoints = append(i.Spec.PlatformSpec.PowerVS.ServiceEndpoints, configv1.PowerVSServiceEndpoint{Name: "rc", URL: "https://resource-controller.test.cloud.ibm.com/something"})
			}),
			expectedActions:  0,
			expectedServices: nil,
			expectedErr:      `^spec\.platformSpec\.powervs.serviceEndpoints\[2\]\.url: Invalid value: "https://resource-controller.test.cloud.ibm.com/something": no path or request parameters must be provided, "/something" was provided$`,
		}, {
			obj: modifier(validEndpoints, func(i *configv1.Infrastructure) {
				i.Spec.PlatformSpec.PowerVS.ServiceEndpoints = append(i.Spec.PlatformSpec.PowerVS.ServiceEndpoints, configv1.PowerVSServiceEndpoint{Name: "iam", URL: "https://iam.test.cloud.ibm.com"})
			}),
			expectedActions:  0,
			expectedServices: nil,
			expectedErr:      `^spec\.platformSpec\.powervs\.serviceEndpoints\[2\]\.name: Invalid value: "iam": duplicate service endpoint not allowed for iam, service endpoint already defined at spec\.platformSpec\.powervs\.serviceEndpoints\[0\]$`,
		}, {
			obj: modifier(validEndpoints, func(i *configv1.Infrastructure) {
				i.Status.PlatformStatus.PowerVS.ServiceEndpoints = i.Spec.PlatformSpec.PowerVS.ServiceEndpoints
			}),
			expectedActions:  0,
			expectedServices: []configv1.PowerVSServiceEndpoint{{Name: "iam", URL: "https://iam.test.cloud.ibm.com"}, {Name: "pe", URL: "https://dal.power-iaas.test.cloud.ibm.com"}},
			expectedErr:      "",
		}, {
			obj: modifier(validEndpoints, func(i *configv1.Infrastructure) {
				i.Status.PlatformStatus.PowerVS.ServiceEndpoints = i.Spec.PlatformSpec.PowerVS.ServiceEndpoints
				i.Spec.PlatformSpec.PowerVS.ServiceEndpoints = append(i.Spec.PlatformSpec.PowerVS.ServiceEndpoints, configv1.PowerVSServiceEndpoint{Name: "rc", URL: "https://resource-controller.test.cloud.ibm.com/something"})
			}),
			expectedActions:  0,
			expectedServices: []configv1.PowerVSServiceEndpoint{{Name: "iam", URL: "https://iam.test.cloud.ibm.com"}, {Name: "pe", URL: "https://dal.power-iaas.test.cloud.ibm.com"}},
			expectedErr:      `^spec\.platformSpec\.powervs.serviceEndpoints\[2\]\.url: Invalid value: "https://resource-controller.test.cloud.ibm.com/something": no path or request parameters must be provided, "/something" was provided$`,
		}, {
			obj: modifier(validEndpoints, func(i *configv1.Infrastructure) {
				i.Status.PlatformStatus.PowerVS.ServiceEndpoints = i.Spec.PlatformSpec.PowerVS.ServiceEndpoints
				i.Spec.PlatformSpec.PowerVS.ServiceEndpoints = append(i.Spec.PlatformSpec.PowerVS.ServiceEndpoints, configv1.PowerVSServiceEndpoint{Name: "iam", URL: "https://iam.test.cloud.ibm.com"})
			}),
			expectedActions:  0,
			expectedServices: []configv1.PowerVSServiceEndpoint{{Name: "iam", URL: "https://iam.test.cloud.ibm.com"}, {Name: "pe", URL: "https://dal.power-iaas.test.cloud.ibm.com"}},
			expectedErr:      `^spec\.platformSpec\.powervs\.serviceEndpoints\[2\]\.name: Invalid value: "iam": duplicate service endpoint not allowed for iam, service endpoint already defined at spec\.platformSpec\.powervs\.serviceEndpoints\[0\]$`,
		}, {
			obj: modifier(validEndpoints, func(i *configv1.Infrastructure) {
				i.Status.PlatformStatus.PowerVS.ServiceEndpoints = []configv1.PowerVSServiceEndpoint{{Name: "iam", URL: "https://iam.test.cloud.ibm.com"}}
				i.Spec.PlatformSpec.PowerVS.ServiceEndpoints = nil
			}),
			expectedActions:  1,
			expectedServices: nil,
			expectedErr:      ``,
		}, {
			obj: modifier(validEndpoints, func(i *configv1.Infrastructure) {
				i.Status.PlatformStatus.PowerVS.ServiceEndpoints = []configv1.PowerVSServiceEndpoint{{Name: "iam", URL: "https://iam.test.cloud.ibm.com"}}
				i.Spec.PlatformSpec.PowerVS.ServiceEndpoints = []configv1.PowerVSServiceEndpoint{}
			}),
			expectedActions:  1,
			expectedServices: nil,
			expectedErr:      ``,
		}, {
			obj: modifier(validEndpoints, func(i *configv1.Infrastructure) {
				i.Status.PlatformStatus.PowerVS.ServiceEndpoints = []configv1.PowerVSServiceEndpoint{{Name: "iam", URL: "https://iam.test.cloud.ibm.com"}}
				i.Spec.PlatformSpec.PowerVS = nil
			}),
			expectedActions:  1,
			expectedServices: nil,
			expectedErr:      ``,
		}}
	for _, tc := range cases {
		t.Run("test_sync", func(t *testing.T) {
			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			if err := indexer.Add(tc.obj); err != nil {
				t.Fatal(err.Error())
			}
			fake := configfakeclient.NewSimpleClientset(tc.obj)
			ctrl := PowerVSPlatformCustomServiceController{
				infraClient: fake.ConfigV1().Infrastructures(),
				infraLister: configv1listers.NewInfrastructureLister(indexer),
			}

			err := ctrl.sync(context.TODO(),
				factory.NewSyncContext("PowerVSPlatformCustomServiceController", events.NewInMemoryRecorder("PowerVSPlatformCustomServiceController")))
			if tc.expectedErr == "" {
				assert.NoError(t, err)
			} else {
				assert.Regexp(t, tc.expectedErr, err.Error())
			}

			assert.Equal(t, tc.expectedActions, len(fake.Actions()))

			var services []configv1.PowerVSServiceEndpoint
			if tc.obj.Status.PlatformStatus != nil && tc.obj.Status.PlatformStatus.PowerVS != nil {
				services = tc.obj.Status.PlatformStatus.PowerVS.ServiceEndpoints
			}
			for _, a := range fake.Actions() {
				obj := a.(ktesting.UpdateAction).GetObject().(*configv1.Infrastructure)
				if obj.Status.PlatformStatus != nil && obj.Status.PlatformStatus.PowerVS != nil {
					services = obj.Status.PlatformStatus.PowerVS.ServiceEndpoints
				}
			}
			assert.EqualValues(t, tc.expectedServices, services)
		})
	}
}
