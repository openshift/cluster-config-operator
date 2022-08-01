package onprem_platform_status_vips_sync

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	configfakeclient "github.com/openshift/client-go/config/clientset/versioned/fake"
	configv1listers "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
)

func TestOnPremPlatformStatusVIPsSyncController_sync(t *testing.T) {
	tests := []struct {
		name           string
		givenStatus    configv1.InfrastructureStatus
		expectedStatus configv1.InfrastructureStatus
		actions        int
	}{
		{
			name: "`new` field is empty, `old` with value: should set `new[0]` to value from `old`",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP: "fooA",
						IngressIP:           "fooI",
					},
				},
			},
			expectedStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI"},
					},
				},
			},
			actions: 1,
		},
		{
			name: "`new` contains values, `old` is empty: should set `old` to value from `new[0]`",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIPs: []string{"fooA", "barA"},
						IngressIPs:           []string{"fooI", "barI"},
					},
				},
			},
			expectedStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA", "barA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI", "barI"},
					},
				},
			},
			actions: 1,
		},
		{
			name: "`new` contains values, `old` contains `new[0]`: should not update anything",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA", "barA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI", "barI"},
					},
				},
			},
			expectedStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA", "barA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI", "barI"},
					},
				},
			},
			actions: 0,
		},
		{
			name: "`new` contains values, `old` contains `new[1]`: should not update anything",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "barA",
						APIServerInternalIPs: []string{"fooA", "barA"},
						IngressIP:            "barI",
						IngressIPs:           []string{"fooI", "barI"},
					},
				},
			},
			expectedStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "barA",
						APIServerInternalIPs: []string{"fooA", "barA"},
						IngressIP:            "barI",
						IngressIPs:           []string{"fooI", "barI"},
					},
				},
			},
			actions: 0,
		},
		{
			name: "`new` contains values, `old` contains a value which is not included in `new`: should set `old` to value from `new[0]` (new values take precedence over old values)",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "bazA",
						APIServerInternalIPs: []string{"fooA", "barA"},
						IngressIP:            "bazI",
						IngressIPs:           []string{"fooI", "barI"},
					},
				},
			},
			expectedStatus: configv1.InfrastructureStatus{
				Platform: configv1.BareMetalPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					BareMetal: &configv1.BareMetalPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA", "barA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI", "barI"},
					},
				},
			},
			actions: 1,
		},
		{
			name: "should handle OpenStack platform",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.OpenStackPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					OpenStack: &configv1.OpenStackPlatformStatus{
						APIServerInternalIP: "fooA",
						IngressIP:           "fooI",
					},
				},
			},
			expectedStatus: configv1.InfrastructureStatus{
				Platform: configv1.OpenStackPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					OpenStack: &configv1.OpenStackPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI"},
					},
				},
			},
			actions: 1,
		},
		{
			name: "should handle vSphere platform",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.VSpherePlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					VSphere: &configv1.VSpherePlatformStatus{
						APIServerInternalIP: "fooA",
						IngressIP:           "fooI",
					},
				},
			},
			expectedStatus: configv1.InfrastructureStatus{
				Platform: configv1.VSpherePlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					VSphere: &configv1.VSpherePlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI"},
					},
				},
			},
			actions: 1,
		},
		{
			name: "should handle oVirt platform",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.OvirtPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Ovirt: &configv1.OvirtPlatformStatus{
						APIServerInternalIP: "fooA",
						IngressIP:           "fooI",
					},
				},
			},
			expectedStatus: configv1.InfrastructureStatus{
				Platform: configv1.OvirtPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Ovirt: &configv1.OvirtPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI"},
					},
				},
			},
			actions: 1,
		},
		{
			name: "should handle Nutanix platform",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.NutanixPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Nutanix: &configv1.NutanixPlatformStatus{
						APIServerInternalIP: "fooA",
						IngressIP:           "fooI",
					},
				},
			},
			expectedStatus: configv1.InfrastructureStatus{
				Platform: configv1.NutanixPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					Nutanix: &configv1.NutanixPlatformStatus{
						APIServerInternalIP:  "fooA",
						APIServerInternalIPs: []string{"fooA"},
						IngressIP:            "fooI",
						IngressIPs:           []string{"fooI"},
					},
				},
			},
			actions: 1,
		},
		{
			name: "should do nothing on non onprem platform",
			givenStatus: configv1.InfrastructureStatus{
				Platform: configv1.AWSPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					AWS: &configv1.AWSPlatformStatus{},
				},
			},
			expectedStatus: configv1.InfrastructureStatus{
				Platform: configv1.AWSPlatformType,
				PlatformStatus: &configv1.PlatformStatus{
					AWS: &configv1.AWSPlatformStatus{},
				},
			},
			actions: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			infra := &configv1.Infrastructure{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Status:     tt.givenStatus,
			}

			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			if err := indexer.Add(infra); err != nil {
				t.Fatal(err.Error())
			}
			fakeclient := configfakeclient.NewSimpleClientset(infra)
			ctrl := OnPremPlatformStatusVIPsSyncController{
				infraClient: fakeclient.ConfigV1().Infrastructures(),
				infraLister: configv1listers.NewInfrastructureLister(indexer),
			}

			err := ctrl.sync(context.TODO(), factory.NewSyncContext("OnPremPlatformStatusVIPsSyncController", events.NewInMemoryRecorder("OnPremPlatformStatusVIPsSyncController")))
			assert.NoError(t, err)

			assert.Equal(t, tt.actions, len(fakeclient.Actions()))

			got := infra.DeepCopy()
			for _, a := range fakeclient.Actions() {
				obj := a.(ktesting.UpdateAction).GetObject().(*configv1.Infrastructure)
				got.Status = obj.Status
			}
			assert.EqualValues(t, tt.expectedStatus, got.Status)
		})
	}
}
