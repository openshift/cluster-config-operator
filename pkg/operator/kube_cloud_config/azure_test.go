package kube_cloud_config

import (
	"testing"

	"github.com/stretchr/testify/assert"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configv1 "github.com/openshift/api/config/v1"
)

func Test_azureTransformer(t *testing.T) {
	cases := []struct {
		name       string
		inputcm    *corev1.ConfigMap
		inputinfra *configv1.Infrastructure

		outputcm *corev1.ConfigMap
		err      string
	}{
		{
			name:       "empty config map, non azure infra",
			inputcm:    &corev1.ConfigMap{},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{}},

			outputcm: nil,
			err:      `invalid platform, expected to be Azure`,
		}, {
			name:       "empty config map, non azure infra",
			inputcm:    &corev1.ConfigMap{},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.NonePlatformType}},

			outputcm: nil,
			err:      `invalid platform, expected to be Azure`,
		}, {
			name:       "empty config map, non azure infra",
			inputcm:    &corev1.ConfigMap{},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.NonePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.NonePlatformType}}},

			outputcm: nil,
			err:      `invalid platform, expected to be Azure`,
		}, {
			name:       "empty config map, non azure infra",
			inputcm:    &corev1.ConfigMap{},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region"}}}},

			outputcm: nil,
			err:      `invalid platform, expected to be Azure`,
		}, {
			name:       "non empty config map, non azure infra",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"resourceGroup":"test-rg"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region"}}}},

			outputcm: nil,
			err:      `invalid platform, expected to be Azure`,
		}, {
			name:       "empty config map, azure infra",
			inputcm:    &corev1.ConfigMap{},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzurePublicCloud"
}
`}},
			err: ``,
		}, {
			name:       "empty config map, azure infra",
			inputcm:    &corev1.ConfigMap{},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{ResourceGroupName: "test-rg"}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzurePublicCloud"
}
`}},
			err: ``,
		}, {
			name:       "non empty config map, azure infra",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"resourceGroup":"test-rg"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{ResourceGroupName: "test-rg"}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzurePublicCloud",
	"resourceGroup": "test-rg"
}
`}},
			err: ``,
		}, {
			name:       "empty config map, azure infra with public cloud",
			inputcm:    &corev1.ConfigMap{},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzurePublicCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzurePublicCloud"
}
`}},
			err: ``,
		}, {
			name:       "empty config map, azure infra with US Gov cloud",
			inputcm:    &corev1.ConfigMap{},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzureUSGovernmentCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzureUSGovernmentCloud"
}
`}},
			err: ``,
		}, {
			name:       "empty config map, azure infra with China cloud",
			inputcm:    &corev1.ConfigMap{},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzureChinaCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzureChinaCloud"
}
`}},
			err: ``,
		}, {
			name:       "empty config map, azure infra with German cloud",
			inputcm:    &corev1.ConfigMap{},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzureGermanCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzureGermanCloud"
}
`}},
			err: ``,
		}, {
			name:       "empty config map, azure infra with empty cloud",
			inputcm:    &corev1.ConfigMap{},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: ""}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzurePublicCloud"
}
`}},
			err: ``,
		}, {
			name:       "empty config map, azure infra with invalid cloud",
			inputcm:    &corev1.ConfigMap{},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: "AzureOtherCloud"}}}},

			outputcm: nil,
			err:      `status\.platformStatus\.azure\.cloudName: Unsupported value: "AzureOtherCloud": supported values: "\w+"(, "\w+")*`,
		}, {
			name:       "non-empty config map, azure infra with public cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"resourceGroup":"test-rg"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzurePublicCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzurePublicCloud",
	"resourceGroup": "test-rg"
}
`}},
			err: ``,
		}, {
			name:       "non-empty config map, azure infra with US Gov cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"resourceGroup":"test-rg"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzureUSGovernmentCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzureUSGovernmentCloud",
	"resourceGroup": "test-rg"
}
`}},
			err: ``,
		}, {
			name:       "non-empty config map, azure infra with China cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"resourceGroup":"test-rg"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzureChinaCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzureChinaCloud",
	"resourceGroup": "test-rg"
}
`}},
			err: ``,
		}, {
			name:       "non-empty config map, azure infra with German cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"resourceGroup":"test-rg"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzureGermanCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzureGermanCloud",
	"resourceGroup": "test-rg"
}
`}},
			err: ``,
		}, {
			name:       "non-empty config map, azure infra with empty cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"resourceGroup":"test-rg"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: ""}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzurePublicCloud",
	"resourceGroup": "test-rg"
}
`}},
			err: ``,
		}, {
			name:       "non-empty config map, azure infra with invalid cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"resourceGroup":"test-rg"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: "AzureOtherCloud"}}}},

			outputcm: nil,
			err:      `status\.platformStatus\.azure\.cloudName: Unsupported value: "AzureOtherCloud": supported values: "\w+"(, "\w+")*`,
		}, {
			name:       "config map with matching cloud, azure infra with public cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":"AzurePublicCloud"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzurePublicCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{"cloud":"AzurePublicCloud"}`}},
			err:      ``,
		}, {
			name:       "config map with matching cloud, azure infra with US Gov cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":"AzureUSGovernmentCloud"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzureUSGovernmentCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{"cloud":"AzureUSGovernmentCloud"}`}},
			err:      ``,
		}, {
			name:       "config map with matching cloud, azure infra with China cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":"AzureChinaCloud"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzureChinaCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{"cloud":"AzureChinaCloud"}`}},
			err:      ``,
		}, {
			name:       "config map with matching cloud, azure infra with German cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":"AzureGermanCloud"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzureGermanCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{"cloud":"AzureGermanCloud"}`}},
			err:      ``,
		}, {
			name:       "config map with matching cloud, azure infra with empty cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":"AzurePublicCloud"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: ""}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{"cloud":"AzurePublicCloud"}`}},
			err:      ``,
		}, {
			name:       "config map with empty cloud, azure infra with public cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":""}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzurePublicCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzurePublicCloud"
}
`}},
			err: ``,
		}, {
			name:       "config map with empty cloud, azure infra with US Gov cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":""}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzureUSGovernmentCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzureUSGovernmentCloud"
}
`}},
			err: ``,
		}, {
			name:       "config map with empty cloud, azure infra with China cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":""}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzureChinaCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzureChinaCloud"
}
`}},
			err: ``,
		}, {
			name:       "config map with empty cloud, azure infra with German cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":""}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzureGermanCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzureGermanCloud"
}
`}},
			err: ``,
		}, {
			name:       "config map with empty cloud, azure infra with empty cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":""}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: ""}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzurePublicCloud"
}
`}},
			err: ``,
		}, {
			name:       "config map with empty cloud, azure infra with invalid cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":""}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: "AzureOtherCloud"}}}},

			outputcm: nil,
			err:      `status\.platformStatus\.azure\.cloudName: Unsupported value: "AzureOtherCloud": supported values: "\w+"(, "\w+")*`,
		}, {
			name:       "config map with conflicting cloud, azure infra with public cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":"AzureUSGovernmentCloud"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzurePublicCloud}}}},

			outputcm: nil,
			err:      `invalid user-provided cloud.conf: "cloud" field in user-provided cloud.conf conflicts with infrastructure object`,
		}, {
			name:       "config map with conflicting cloud, azure infra with US Gov cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":"AzurePublicCloud"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzureUSGovernmentCloud}}}},

			outputcm: nil,
			err:      `invalid user-provided cloud.conf: "cloud" field in user-provided cloud.conf conflicts with infrastructure object`,
		}, {
			name:       "config map with conflicting cloud, azure infra with China cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":"AzurePublicCloud"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzureChinaCloud}}}},

			outputcm: nil,
			err:      `invalid user-provided cloud.conf: "cloud" field in user-provided cloud.conf conflicts with infrastructure object`,
		}, {
			name:       "config map with conflicting cloud, azure infra with German cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":"AzurePublicCloud"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzureGermanCloud}}}},

			outputcm: nil,
			err:      `invalid user-provided cloud.conf: "cloud" field in user-provided cloud.conf conflicts with infrastructure object`,
		}, {
			name:       "config map with conflicting cloud, azure infra with empty cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":"AzureUSGovernmentCloud"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: ""}}}},

			outputcm: nil,
			err:      `invalid user-provided cloud.conf: "cloud" field in user-provided cloud.conf conflicts with infrastructure object`,
		}, {
			name:       "config map with non-yaml data, azure infra",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `not yaml`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: ""}}}},

			outputcm: nil,
			err:      `failed to read the cloud.conf: error unmarshaling`,
		}, {
			name:       "config map with non-string cloud, azure infra",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":1}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: ""}}}},

			outputcm: nil,
			err:      `invalid user-provided cloud.conf: "cloud" field is not a string`,
		}, {
			name:       "config map with binary data, azure infra",
			inputcm:    &corev1.ConfigMap{BinaryData: map[string][]byte{"config": []byte(`{"resourceGroup":"test-rg"}`)}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: ""}}}},

			outputcm: &corev1.ConfigMap{BinaryData: map[string][]byte{"cloud.conf": []byte(`{
	"cloud": "AzurePublicCloud",
	"resourceGroup": "test-rg"
}
`)}},
			err: ``,
		}, {
			name:       "config map with unicode, azure infra with public cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"resourceGroup":"测试资源组"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzurePublicCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{
	"cloud": "AzurePublicCloud",
	"resourceGroup": "测试资源组"
}
`}},
			err: ``,
		}, {
			name:       "config map with unicode binary data, azure infra with public cloud",
			inputcm:    &corev1.ConfigMap{BinaryData: map[string][]byte{"config": []byte(`{"resourceGroup":"测试资源组"}`)}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzurePublicCloud}}}},

			outputcm: &corev1.ConfigMap{BinaryData: map[string][]byte{"cloud.conf": []byte(`{
	"cloud": "AzurePublicCloud",
	"resourceGroup": "测试资源组"
}
`)}},
			err: ``,
		}, {
			name:       "config map with matching upper-case cloud, azure infra with public cloud",
			inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"cloud":"AZUREPUBLICCLOUD"}`}},
			inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{CloudName: configv1.AzurePublicCloud}}}},

			outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `{"cloud":"AZUREPUBLICCLOUD"}`}},
			err:      ``,
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			outputcm, err := azureTransformer(test.inputcm, "config", test.inputinfra)
			if test.err == "" {
				if assert.NoError(t, err) {
					outputcm.ObjectMeta = metav1.ObjectMeta{}
					assert.EqualValues(t, test.outputcm, outputcm)
				}
			} else {
				assert.Regexp(t, test.err, err)
			}
		})
	}
}
