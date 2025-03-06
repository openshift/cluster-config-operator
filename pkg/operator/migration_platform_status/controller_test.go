package migration_platform_status

import (
	"context"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	configfakeclient "github.com/openshift/client-go/config/clientset/versioned/fake"
	configv1listers "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	clocktesting "k8s.io/utils/clock/testing"
)

func Test_sync(t *testing.T) {
	cases := []struct {
		inputstatus configv1.InfrastructureStatus
		inputdata   map[string]string

		outputstatus configv1.InfrastructureStatus
		err          string
		actions      int
	}{{
		inputstatus:  configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType},
		outputstatus: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType}},
		actions:      1,
	}, {
		inputstatus:  configv1.InfrastructureStatus{Platform: configv1.PlatformType("random")},
		outputstatus: configv1.InfrastructureStatus{Platform: configv1.PlatformType("random"), PlatformStatus: &configv1.PlatformStatus{Type: "random"}},
		actions:      1,
	}, {
		inputstatus: configv1.InfrastructureStatus{Platform: configv1.PlatformType("oldType"), PlatformStatus: &configv1.PlatformStatus{Type: "newType"}},
		err:         `^Mis-match between status\.platform \(oldType\) and status\.platformStatus\.type \(newType\) in infrastructures\.config\.openshift\.io/cluster$`,
	}, {
		inputstatus:  configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType}},
		outputstatus: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType}},
		actions:      0,
	}, {
		inputstatus:  configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{ResourceGroupName: "test-rg"}}},
		outputstatus: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{ResourceGroupName: "test-rg"}}},
		actions:      0,
	}, {
		inputstatus:  configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region"}}},
		outputstatus: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region"}}},
		actions:      0,
	}, {
		inputstatus:  configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region", ServiceEndpoints: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "https://ec2.local"}}}}},
		outputstatus: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region", ServiceEndpoints: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "https://ec2.local"}}}}},
		actions:      0,
	}, {
		inputstatus: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType}},
		err:         `^install-config key doesn't exist in ConfigMap kube-system/cluster-config-v1$`,
	}, {
		inputstatus: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType},
		err:         `^install-config key doesn't exist in ConfigMap kube-system/cluster-config-v1$`,
	}, {
		inputstatus: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType},
		inputdata:   map[string]string{"random-key": "random-value"},
		err:         `^install-config key doesn't exist in ConfigMap kube-system/cluster-config-v1$`,
	}, {
		inputstatus: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType},
		inputdata: map[string]string{
			"install-config": `apiVersion: v1
baseDomain: testing.openshift.com
compute:
- hyperthreading: Enabled
  name: worker
  replicas: 3
controlPlane:
  hyperthreading: Enabled
  name: master
  replicas: 3
metadata:
  name: testing
networking:
  clusterNetwork:
  - cidr: 10.128.0.0/14
    hostPrefix: 23
  machineCIDR: 10.0.0.0/16
  networkType: OpenShiftSDN
  serviceNetwork:
  - 172.30.0.0/16
platform:
  azure:
    baseDomainResourceGroupName: os4-common
    region: centralus
pullSecret: REDACTED
sshKey: REDACTED`,
		},
		err: `^no AWS configuration found in cluster-config-v1$`,
	}, {
		inputstatus: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType},
		inputdata: map[string]string{
			"install-config": `{
  "apiVersion": "v1",
  "baseDomain": "testing.openshift.com",
  "compute": [
    {
      "hyperthreading": "Enabled",
      "name": "worker",
      "replicas": 3
    }
  ],
  "controlPlane": {
    "hyperthreading": "Enabled",
    "name": "master",
    "replicas": 3
  },
  "metadata": {
    "name": "testing"
  },
  "networking": {
    "clusterNetwork": [
      {
        "cidr": "10.128.0.0/14",
        "hostPrefix": 23
      }
    ],
    "machineCIDR": "10.0.0.0/16",
    "networkType": "OpenShiftSDN",
    "serviceNetwork": [
      "172.30.0.0/16"
    ]
  },
  "platform": {
    "azure": {
      "baseDomainResourceGroupName": "os4-common",
      "region": "centralus"
    }
  },
  "pullSecret": "REDACTED",
  "sshKey": "REDACTED"
}`,
		},
		err: `^no AWS configuration found in cluster-config-v1$`,
	}, {
		inputstatus: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType},
		inputdata: map[string]string{
			"install-config": `apiVersion: v1
baseDomain: testing.openshift.com
compute:
- hyperthreading: Enabled
  name: worker
  replicas: 3
controlPlane:
  hyperthreading: Enabled
  name: master
  replicas: 3
metadata:
  name: testing
networking:
  clusterNetwork:
  - cidr: 10.128.0.0/14
    hostPrefix: 23
  machineCIDR: 10.0.0.0/16
  networkType: OpenShiftSDN
  serviceNetwork:
  - 172.30.0.0/16
platform:
  aws:
    region: testing-region
pullSecret: REDACTED
sshKey: REDACTED`,
		},
		outputstatus: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "testing-region"}}},
		actions:      1,
	}}
	for _, test := range cases {
		t.Run("", func(t *testing.T) {
			infra := &configv1.Infrastructure{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}, Status: test.inputstatus}
			indexerInfra := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			if err := indexerInfra.Add(infra); err != nil {
				t.Fatal(err.Error())
			}
			fakeConfig := configfakeclient.NewSimpleClientset(infra)

			cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cluster-config-v1", Namespace: "kube-system"}, Data: test.inputdata}
			fake := fake.NewSimpleClientset(cm)

			ctrl := MigrationPlatformStatusController{
				infraClient:     fakeConfig.ConfigV1().Infrastructures(),
				infraLister:     configv1listers.NewInfrastructureLister(indexerInfra),
				configMapClient: fake.CoreV1(),
			}

			err := ctrl.sync(context.TODO(),
				factory.NewSyncContext("MigrationPlatformStatusController", events.NewInMemoryRecorder("MigrationPlatformStatusController", clocktesting.NewFakePassiveClock(time.Now()))))
			if test.err == "" {
				assert.NoError(t, err)
				assert.Equal(t, test.actions, len(fakeConfig.Actions()))
				got := infra.DeepCopy()
				for _, a := range fakeConfig.Actions() {
					obj := a.(ktesting.UpdateAction).GetObject().(*configv1.Infrastructure)
					got.Status = obj.Status
				}
				assert.EqualValues(t, test.outputstatus, got.Status)
			} else if assert.Error(t, err) {
				assert.Regexp(t, test.err, err.Error())
			}
		})
	}
}
