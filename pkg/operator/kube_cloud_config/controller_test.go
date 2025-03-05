package kubecloudconfig

import (
	"context"
	"testing"
	"time"

	configv1 "github.com/openshift/api/config/v1"
	configfakeclient "github.com/openshift/client-go/config/clientset/versioned/fake"
	configv1listers "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/cluster-config-operator/pkg/operator/operatorclient"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	clocktesting "k8s.io/utils/clock/testing"
)

func Test_asIsTransformer(t *testing.T) {
	cases := []struct {
		input, output *corev1.ConfigMap
	}{{
		input:  &corev1.ConfigMap{},
		output: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: targetConfigName, Namespace: operatorclient.GlobalMachineSpecifiedConfigNamespace}},
	}, {
		input:  &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "something", Namespace: "something-else"}},
		output: &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: targetConfigName, Namespace: operatorclient.GlobalMachineSpecifiedConfigNamespace}},
	}, {
		input: &corev1.ConfigMap{
			Data: map[string]string{"config": "someval"},
		},
		output: &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: targetConfigName, Namespace: operatorclient.GlobalMachineSpecifiedConfigNamespace},
			Data:       map[string]string{"cloud.conf": "someval"},
		},
	}, {
		input: &corev1.ConfigMap{
			BinaryData: map[string][]byte{"config": []byte("someval")},
		},
		output: &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: targetConfigName, Namespace: operatorclient.GlobalMachineSpecifiedConfigNamespace},
			BinaryData: map[string][]byte{"cloud.conf": []byte("someval")},
		},
	}, {
		input: &corev1.ConfigMap{
			Data: map[string]string{"config": "someval", "ca-bundle": "bundle"},
		},
		output: &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: targetConfigName, Namespace: operatorclient.GlobalMachineSpecifiedConfigNamespace},
			Data:       map[string]string{"cloud.conf": "someval", "ca-bundle": "bundle"},
		},
	}, {
		input: &corev1.ConfigMap{
			Data:       map[string]string{"config": "someval"},
			BinaryData: map[string][]byte{"ca-bundle": []byte("bundle")},
		},
		output: &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: targetConfigName, Namespace: operatorclient.GlobalMachineSpecifiedConfigNamespace},
			Data:       map[string]string{"cloud.conf": "someval"},
			BinaryData: map[string][]byte{"ca-bundle": []byte("bundle")},
		},
	}, {
		input: &corev1.ConfigMap{
			Data: map[string]string{"ca-bundle": "bundle"},
		},
		output: &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: targetConfigName, Namespace: operatorclient.GlobalMachineSpecifiedConfigNamespace},
			Data:       map[string]string{"ca-bundle": "bundle"},
		},
	}, {
		input: &corev1.ConfigMap{
			BinaryData: map[string][]byte{"ca-bundle": []byte("bundle")},
		},
		output: &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: targetConfigName, Namespace: operatorclient.GlobalMachineSpecifiedConfigNamespace},
			BinaryData: map[string][]byte{"ca-bundle": []byte("bundle")},
		},
	}}
	for _, test := range cases {
		t.Run("", func(t *testing.T) {
			got, err := asIsTransformer(test.input, "config", nil)
			assert.NoError(t, err)
			assert.EqualValues(t, test.output, got)
		})
	}
}

func Test_sync(t *testing.T) {
	cases := []struct {
		inputinfra *configv1.Infrastructure
		inputdata  string

		outputdata map[string]string
		err        string
		actions    []ktesting.Action
	}{{
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{Type: configv1.GCPPlatformType}}},
		inputdata: `[global]
somekey = somevalue`,

		outputdata: map[string]string{"cloud.conf": `[global]
somekey = somevalue`},
		actions: []ktesting.Action{
			ktesting.NewGetAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config", "cluster-config-v1"),
			ktesting.NewGetAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config-managed", "kube-cloud-config"),
			ktesting.NewUpdateAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config-managed", nil),
		},
	}, {
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{Type: configv1.NonePlatformType}}},

		actions: []ktesting.Action{
			ktesting.NewDeleteAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config-managed", "kube-cloud-config"),
		},
	}, {
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region"}}}},

		actions: []ktesting.Action{
			ktesting.NewDeleteAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config-managed", "kube-cloud-config"),
		},
	}, {
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region", ServiceEndpoints: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "https://ec2.local"}}}}}},

		outputdata: map[string]string{"cloud.conf": `
[ServiceOverride "0"]
	Service = ec2
	Region = test-region
	URL = https://ec2.local
	SigningRegion = test-region
`},
		actions: []ktesting.Action{
			ktesting.NewGetAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config-managed", "kube-cloud-config"),
			ktesting.NewUpdateAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config-managed", nil),
		},
	}, {
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region"}}}},
		inputdata: `[Global]
VPC = vpc-test
SubnetID = subnet-test`,

		outputdata: map[string]string{"cloud.conf": `[Global]
VPC = vpc-test
SubnetID = subnet-test`},
		actions: []ktesting.Action{
			ktesting.NewGetAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config", "cluster-config-v1"),
			ktesting.NewGetAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config-managed", "kube-cloud-config"),
			ktesting.NewUpdateAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config-managed", nil),
		},
	}, {
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region", ServiceEndpoints: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "https://ec2.local"}}}}}},
		inputdata: `[Global]
VPC = vpc-test
SubnetID = subnet-test`,

		outputdata: map[string]string{"cloud.conf": `[Global]
VPC = vpc-test
SubnetID = subnet-test
[ServiceOverride "0"]
	Service = ec2
	Region = test-region
	URL = https://ec2.local
	SigningRegion = test-region
`},
		actions: []ktesting.Action{
			ktesting.NewGetAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config", "cluster-config-v1"),
			ktesting.NewGetAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config-managed", "kube-cloud-config"),
			ktesting.NewUpdateAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config-managed", nil),
		},
	}, {
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{}}}},
		inputdata:  `{"resourceGroup":"test-rg"}`,

		outputdata: map[string]string{"cloud.conf": `{
	"cloud": "AzurePublicCloud",
	"resourceGroup": "test-rg"
}
`},
		actions: []ktesting.Action{
			ktesting.NewGetAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config", "cluster-config-v1"),
			ktesting.NewGetAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config-managed", "kube-cloud-config"),
			ktesting.NewUpdateAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config-managed", nil),
		},
	}, {
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzureUSGovernmentCloud}}}},
		inputdata:  `{"resourceGroup":"test-rg"}`,

		outputdata: map[string]string{"cloud.conf": `{
	"cloud": "AzureUSGovernmentCloud",
	"resourceGroup": "test-rg"
}
`},
		actions: []ktesting.Action{
			ktesting.NewGetAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config", "cluster-config-v1"),
			ktesting.NewGetAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config-managed", "kube-cloud-config"),
			ktesting.NewUpdateAction(schema.GroupVersionResource{Resource: "configmaps"}, "openshift-config-managed", nil),
		},
	}}
	for _, test := range cases {
		t.Run("", func(t *testing.T) {
			fake := fake.NewSimpleClientset()
			if len(test.inputdata) > 0 {
				fake.Tracker().Add(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cluster-config-v1", Namespace: "openshift-config"}, Data: map[string]string{"config": test.inputdata}})
				test.inputinfra.Spec.CloudConfig = configv1.ConfigMapFileReference{Name: "cluster-config-v1", Key: "config"}
			}
			fake.Tracker().Add(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "kube-cloud-config", Namespace: "openshift-config-managed"}})

			test.inputinfra.ObjectMeta = metav1.ObjectMeta{Name: "cluster"}
			indexerInfra := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			if err := indexerInfra.Add(test.inputinfra); err != nil {
				t.Fatal(err.Error())
			}
			fakeConfig := configfakeclient.NewSimpleClientset(test.inputinfra)

			ctrl := KubeCloudConfigController{
				infraClient:             fakeConfig.ConfigV1().Infrastructures(),
				infraLister:             configv1listers.NewInfrastructureLister(indexerInfra),
				configMapClient:         fake.CoreV1(),
				cloudConfigTransformers: cloudConfigTransformers(),
			}

			err := ctrl.sync(context.TODO(),
				factory.NewSyncContext("KubeCloudConfigController", events.NewInMemoryRecorder("KubeCloudConfigController", clocktesting.NewFakePassiveClock(time.Now()))))
			if test.err == "" {
				assert.NoError(t, err)
				assert.Equal(t, len(test.actions), len(fake.Actions()))

				for idx, a := range fake.Actions() {
					actionGot := a.(ktesting.Action)
					actionExp := test.actions[idx].(ktesting.Action)
					assert.Equalf(t, actionExp.GetVerb(), actionGot.GetVerb(), "mismatch verb at action %d", idx)
					assert.Equalf(t, actionExp.GetNamespace(), actionGot.GetNamespace(), "mismatch namespace at action %d", idx)
					assert.Equalf(t, actionExp.GetResource().Resource, actionGot.GetResource().Resource, "mismatch resource at action %d", idx)

					switch obj := a.(type) {
					case ktesting.GetAction:
						getExp := actionExp.(ktesting.GetAction)
						assert.Equalf(t, getExp.GetName(), obj.GetName(), "mismatch name at action %d", idx)
					case ktesting.UpdateAction:
						assert.Equalf(t, test.outputdata, obj.GetObject().(*corev1.ConfigMap).Data, "mismatch Update data at action %d", idx)
					case ktesting.DeleteAction:
						deleteExp := actionExp.(ktesting.DeleteAction)
						assert.Equalf(t, deleteExp.GetName(), obj.GetName(), "mismatch name at action %d", idx)
					}
				}
			} else {
				assert.Regexp(t, test.err, err.Error())
			}
		})
	}
}
