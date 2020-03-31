package aws_platform_service_location

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	_ "github.com/stretchr/testify/assert"

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
		Spec:       configv1.InfrastructureSpec{PlatformSpec: configv1.PlatformSpec{Type: configv1.AWSPlatformType}},
		Status: configv1.InfrastructureStatus{
			Platform: configv1.AWSPlatformType,
			PlatformStatus: &configv1.PlatformStatus{
				Type: configv1.AWSPlatformType,
				AWS:  &configv1.AWSPlatformStatus{Region: "us-east-1"},
			},
		},
	}
	validEndpoints := modifier(basicObj, func(i *configv1.Infrastructure) {
		i.Spec.PlatformSpec.AWS = &configv1.AWSPlatformSpec{
			ServiceEndpoints: []configv1.AWSServiceEndpoint{{
				Name: "ec2",
				URL:  "https://ec2.local",
			}, {
				Name: "s3",
				URL:  "https://s3.local",
			}},
		}
	})
	cases := []struct {
		obj              *configv1.Infrastructure
		expectedActions  int
		expectedServices []configv1.AWSServiceEndpoint
		expectedErr      string
	}{{
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
		expectedErr:      `spec\.platformSpec\.type: Invalid value: "None": non AWS platform type set in specification`,
	}, {
		obj: modifier(basicObj, func(i *configv1.Infrastructure) {
			i.Spec.PlatformSpec.AWS = &configv1.AWSPlatformSpec{}
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
		expectedServices: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "https://ec2.local"}, {Name: "s3", URL: "https://s3.local"}},
		expectedErr:      "",
	}, {
		obj: modifier(validEndpoints, func(i *configv1.Infrastructure) {
			sort.Slice(i.Spec.PlatformSpec.AWS.ServiceEndpoints, func(x, y int) bool {
				return i.Spec.PlatformSpec.AWS.ServiceEndpoints[x].Name > i.Spec.PlatformSpec.AWS.ServiceEndpoints[y].Name
			})
		}),
		expectedActions:  1,
		expectedServices: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "https://ec2.local"}, {Name: "s3", URL: "https://s3.local"}},
		expectedErr:      "",
	}, {
		obj: modifier(validEndpoints, func(i *configv1.Infrastructure) {
			i.Spec.PlatformSpec.AWS.ServiceEndpoints = append(i.Spec.PlatformSpec.AWS.ServiceEndpoints, configv1.AWSServiceEndpoint{Name: "r53", URL: "https://r53.local/something"})
		}),
		expectedActions:  0,
		expectedServices: nil,
		expectedErr:      `^spec\.platformSpec\.aws.serviceEndpoints\[2\]\.url: Invalid value: "https://r53.local/something": no path or request parameters must be provided, "/something" was provided$`,
	}, {
		obj: modifier(validEndpoints, func(i *configv1.Infrastructure) {
			i.Spec.PlatformSpec.AWS.ServiceEndpoints = append(i.Spec.PlatformSpec.AWS.ServiceEndpoints, configv1.AWSServiceEndpoint{Name: "ec2", URL: "https://ec2-fips.local"})
		}),
		expectedActions:  0,
		expectedServices: nil,
		expectedErr:      `^spec\.platformSpec\.aws\.serviceEndpoints\[2\]\.name: Invalid value: "ec2": duplicate service endpoint not allowed for ec2, service endpoint already defined at spec\.platformSpec\.aws\.serviceEndpoints\[0\]$`,
	}, {
		obj: modifier(validEndpoints, func(i *configv1.Infrastructure) {
			i.Status.PlatformStatus.AWS.ServiceEndpoints = i.Spec.PlatformSpec.AWS.ServiceEndpoints
		}),
		expectedActions:  0,
		expectedServices: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "https://ec2.local"}, {Name: "s3", URL: "https://s3.local"}},
		expectedErr:      "",
	}, {
		obj: modifier(validEndpoints, func(i *configv1.Infrastructure) {
			i.Status.PlatformStatus.AWS.ServiceEndpoints = i.Spec.PlatformSpec.AWS.ServiceEndpoints
			i.Spec.PlatformSpec.AWS.ServiceEndpoints = append(i.Spec.PlatformSpec.AWS.ServiceEndpoints, configv1.AWSServiceEndpoint{Name: "r53", URL: "https://r53.local/something"})
		}),
		expectedActions:  0,
		expectedServices: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "https://ec2.local"}, {Name: "s3", URL: "https://s3.local"}},
		expectedErr:      `^spec\.platformSpec\.aws.serviceEndpoints\[2\]\.url: Invalid value: "https://r53.local/something": no path or request parameters must be provided, "/something" was provided$`,
	}, {
		obj: modifier(validEndpoints, func(i *configv1.Infrastructure) {
			i.Status.PlatformStatus.AWS.ServiceEndpoints = i.Spec.PlatformSpec.AWS.ServiceEndpoints
			i.Spec.PlatformSpec.AWS.ServiceEndpoints = append(i.Spec.PlatformSpec.AWS.ServiceEndpoints, configv1.AWSServiceEndpoint{Name: "ec2", URL: "https://ec2-fips.local"})
		}),
		expectedActions:  0,
		expectedServices: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "https://ec2.local"}, {Name: "s3", URL: "https://s3.local"}},
		expectedErr:      `^spec\.platformSpec\.aws\.serviceEndpoints\[2\]\.name: Invalid value: "ec2": duplicate service endpoint not allowed for ec2, service endpoint already defined at spec\.platformSpec\.aws\.serviceEndpoints\[0\]$`,
	}}
	for _, tc := range cases {
		t.Run("test_sync", func(t *testing.T) {
			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			if err := indexer.Add(tc.obj); err != nil {
				t.Fatal(err.Error())
			}
			fake := configfakeclient.NewSimpleClientset(tc.obj)
			ctrl := AWSPlatformServiceLocationController{
				infraClient: fake.ConfigV1().Infrastructures(),
				infraLister: configv1listers.NewInfrastructureLister(indexer),
			}

			err := ctrl.sync(context.TODO(),
				factory.NewSyncContext("AWSPlatformServiceLocationController", events.NewInMemoryRecorder("AWSPlatformServiceLocationController")))
			if tc.expectedErr == "" {
				assert.NoError(t, err)
			} else {
				assert.Regexp(t, tc.expectedErr, err.Error())
			}
			assert.Equal(t, tc.expectedActions, len(fake.Actions()))

			var services []configv1.AWSServiceEndpoint
			if tc.obj.Status.PlatformStatus != nil && tc.obj.Status.PlatformStatus.AWS != nil {
				services = tc.obj.Status.PlatformStatus.AWS.ServiceEndpoints
			}
			for _, a := range fake.Actions() {
				obj := a.(ktesting.UpdateAction).GetObject().(*configv1.Infrastructure)
				if obj.Status.PlatformStatus != nil && obj.Status.PlatformStatus.AWS != nil {
					services = obj.Status.PlatformStatus.AWS.ServiceEndpoints
				}
			}
			assert.EqualValues(t, tc.expectedServices, services)
		})
	}
}
