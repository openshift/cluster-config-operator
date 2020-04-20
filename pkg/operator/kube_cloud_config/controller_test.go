package kube_cloud_config

import (
	"testing"

	"github.com/openshift/cluster-config-operator/pkg/operator/operatorclient"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
