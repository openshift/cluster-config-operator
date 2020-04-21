package kube_cloud_config

import (
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func Test_serviceOverrides(t *testing.T) {
	cases := []struct {
		overrides []configv1.AWSServiceEndpoint

		section string
		err     string
	}{{
		overrides: nil,
		section:   ``,
	}, {
		overrides: []configv1.AWSServiceEndpoint{{
			Name: "ec2",
			URL:  "ec2.local",
		}},
		section: `
[ServiceOverride "0"]
	Service = ec2
	Region = test-region
	URL = ec2.local
	SigningRegion = test-region
`,
	}, {
		overrides: []configv1.AWSServiceEndpoint{{
			Name: "ec2",
			URL:  "ec2.local",
		}, {
			Name: "s3",
			URL:  "s3.local",
		}},
		section: `
[ServiceOverride "0"]
	Service = ec2
	Region = test-region
	URL = ec2.local
	SigningRegion = test-region

[ServiceOverride "1"]
	Service = s3
	Region = test-region
	URL = s3.local
	SigningRegion = test-region
`,
	}}

	for _, test := range cases {
		t.Run("test", func(t *testing.T) {
			section, err := serviceOverrides(test.overrides, "test-region")
			if test.err == "" {
				assert.NoError(t, err)
				assert.Equal(t, test.section, section)
			} else {
				assert.Regexp(t, test.err, err)
			}
		})
	}
}

func Test_awsTransformer(t *testing.T) {
	cases := []struct {
		name       string
		inputcm    *corev1.ConfigMap
		inputinfra *configv1.Infrastructure

		outputcm *corev1.ConfigMap
		err      string
	}{{
		name:       "empty config map, non aws infra",
		inputcm:    &corev1.ConfigMap{},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{}},

		outputcm: nil,
		err:      `invalid platform, expected to be AWS`,
	}, {
		name:       "empty config map, non aws infra",
		inputcm:    &corev1.ConfigMap{},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.NonePlatformType}},

		outputcm: nil,
		err:      `invalid platform, expected to be AWS`,
	}, {
		name:       "empty config map, non aws infra",
		inputcm:    &corev1.ConfigMap{},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.NonePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.NonePlatformType}}},

		outputcm: nil,
		err:      `invalid platform, expected to be AWS`,
	}, {
		name:       "empty config map, non aws infra",
		inputcm:    &corev1.ConfigMap{},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{ResourceGroupName: "test-rg"}}}},

		outputcm: nil,
		err:      `invalid platform, expected to be AWS`,
	}, {
		name:       "non empty config map, non aws infra",
		inputcm:    &corev1.ConfigMap{Data: map[string]string{"config": `{"resource-group": "test-rg"}`}},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AzurePlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AzurePlatformType, Azure: &configv1.AzurePlatformStatus{ResourceGroupName: "test-rg"}}}},

		outputcm: nil,
		err:      `invalid platform, expected to be AWS`,
	}, {
		name:       "empty config map, aws infra",
		inputcm:    &corev1.ConfigMap{},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType}}},

		outputcm: &corev1.ConfigMap{},
		err:      ``,
	}, {
		name:       "empty config map, aws infra",
		inputcm:    &corev1.ConfigMap{},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region"}}}},

		outputcm: &corev1.ConfigMap{},
		err:      ``,
	}, {
		name: "non empty config map, aws infra",
		inputcm: &corev1.ConfigMap{Data: map[string]string{"config": `[Global]
VPC = vpc-test
SubnetID = subnet-test
`}},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region"}}}},

		outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `[Global]
VPC = vpc-test
SubnetID = subnet-test
`}},
		err: ``,
	}, {
		name:       "empty config map, aws infra with service endpoints",
		inputcm:    &corev1.ConfigMap{},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region", ServiceEndpoints: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "ec2.local"}}}}}},

		outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `
[ServiceOverride "0"]
	Service = ec2
	Region = test-region
	URL = ec2.local
	SigningRegion = test-region
`}},
		err: ``,
	}, {
		name: "non empty config map, aws infra with service endpoints",
		inputcm: &corev1.ConfigMap{Data: map[string]string{"config": `[Global]
VPC = vpc-test
SubnetID = subnet-test
`}},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region", ServiceEndpoints: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "ec2.local"}}}}}},

		outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `[Global]
VPC = vpc-test
SubnetID = subnet-test

[ServiceOverride "0"]
	Service = ec2
	Region = test-region
	URL = ec2.local
	SigningRegion = test-region
`}},
		err: ``,
	}, {
		name:       "empty config map, aws infra with service endpoints",
		inputcm:    &corev1.ConfigMap{},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region", ServiceEndpoints: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "ec2.local"}, {Name: "s3", URL: "s3.local"}}}}}},

		outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `
[ServiceOverride "0"]
	Service = ec2
	Region = test-region
	URL = ec2.local
	SigningRegion = test-region

[ServiceOverride "1"]
	Service = s3
	Region = test-region
	URL = s3.local
	SigningRegion = test-region
`}},
		err: ``,
	}, {
		name: "non empty config map, aws infra with service endpoints",
		inputcm: &corev1.ConfigMap{Data: map[string]string{"config": `[Global]
VPC = vpc-test
SubnetID = subnet-test
`}},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region", ServiceEndpoints: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "ec2.local"}, {Name: "s3", URL: "s3.local"}}}}}},

		outputcm: &corev1.ConfigMap{Data: map[string]string{"cloud.conf": `[Global]
VPC = vpc-test
SubnetID = subnet-test

[ServiceOverride "0"]
	Service = ec2
	Region = test-region
	URL = ec2.local
	SigningRegion = test-region

[ServiceOverride "1"]
	Service = s3
	Region = test-region
	URL = s3.local
	SigningRegion = test-region
`}},
		err: ``,
	}, {
		name:       "empty config map, aws infra with service endpoints, no region",
		inputcm:    &corev1.ConfigMap{},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "", ServiceEndpoints: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "ec2.local"}}}}}},

		outputcm: nil,
		err:      `status\.platformStatus\.aws\.region: Required value: region is required to be set for AWS platform`,
	}, {
		name: "non empty config map, aws infra with service endpoints, no region",
		inputcm: &corev1.ConfigMap{Data: map[string]string{"config": `[Global]
VPC = vpc-test
SubnetID = subnet-test
`}},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "", ServiceEndpoints: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "ec2.local"}}}}}},

		outputcm: nil,
		err:      `status\.platformStatus\.aws\.region: Required value: region is required to be set for AWS platform`,
	}, {
		name: "non empty config map, aws infra with service endpoints",
		inputcm: &corev1.ConfigMap{BinaryData: map[string][]byte{"config": []byte(`[Global]
VPC = vpc-test
SubnetID = subnet-test
`)}},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region", ServiceEndpoints: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "ec2.local"}}}}}},

		outputcm: &corev1.ConfigMap{BinaryData: map[string][]byte{"cloud.conf": []byte(`[Global]
VPC = vpc-test
SubnetID = subnet-test

[ServiceOverride "0"]
	Service = ec2
	Region = test-region
	URL = ec2.local
	SigningRegion = test-region
`)}},
		err: ``,
	}, {
		name: "non empty config map, aws infra with service endpoints",
		inputcm: &corev1.ConfigMap{Data: map[string]string{
			"cloud.ca": `-----BUNDLE-----
-----END BUNDLE----
`,
			"config": `[Global]
VPC = vpc-test
SubnetID = subnet-test
`}},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region", ServiceEndpoints: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "ec2.local"}}}}}},

		outputcm: &corev1.ConfigMap{Data: map[string]string{
			"cloud.ca": `-----BUNDLE-----
-----END BUNDLE----
`,
			"cloud.conf": `[Global]
VPC = vpc-test
SubnetID = subnet-test

[ServiceOverride "0"]
	Service = ec2
	Region = test-region
	URL = ec2.local
	SigningRegion = test-region
`}},
		err: ``,
	}, {
		name: "non empty config map, aws infra with service endpoints",
		inputcm: &corev1.ConfigMap{
			Data: map[string]string{"config": `[Global]
VPC = vpc-test
SubnetID = subnet-test
`},
			BinaryData: map[string][]byte{"cloud.ca": []byte(`-----BUNDLE-----
-----END BUNDLE----
`)},
		},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region", ServiceEndpoints: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "ec2.local"}}}}}},

		outputcm: &corev1.ConfigMap{
			Data: map[string]string{"cloud.conf": `[Global]
VPC = vpc-test
SubnetID = subnet-test

[ServiceOverride "0"]
	Service = ec2
	Region = test-region
	URL = ec2.local
	SigningRegion = test-region
`},
			BinaryData: map[string][]byte{"cloud.ca": []byte(`-----BUNDLE-----
-----END BUNDLE----
`)},
		},
		err: ``,
	}, {
		name: "non empty config map, aws infra with service endpoints, conflict",
		inputcm: &corev1.ConfigMap{Data: map[string]string{"config": `[Global]
VPC = vpc-test
SubnetID = subnet-test

[ServiceOverride "0"]
	Service = ec2
	Region = test-region
	URL = ec2.local
	SigningRegion = test-region
`}},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region", ServiceEndpoints: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "ec2.local"}}}}}},

		outputcm: nil,
		err:      `invalid user provided cloud.conf: user provided cloud.conf and infrastructure object both include service overrides`,
	}, {
		name: "non empty config map, aws infra with service endpoints, conflict",
		inputcm: &corev1.ConfigMap{Data: map[string]string{"config": `[Global]
VPC = vpc-test
SubnetID = subnet-test

[ServiceOverride "0"]
	Service = elb
	Region = test-region
	URL = elb.local
	SigningRegion = test-region
`}},
		inputinfra: &configv1.Infrastructure{Status: configv1.InfrastructureStatus{Platform: configv1.AWSPlatformType, PlatformStatus: &configv1.PlatformStatus{Type: configv1.AWSPlatformType, AWS: &configv1.AWSPlatformStatus{Region: "test-region", ServiceEndpoints: []configv1.AWSServiceEndpoint{{Name: "ec2", URL: "ec2.local"}}}}}},

		outputcm: nil,
		err:      `invalid user provided cloud.conf: user provided cloud.conf and infrastructure object both include service overrides`,
	}}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			outputcm, err := awsTransformer(test.inputcm, "config", test.inputinfra)
			if test.err == "" {
				assert.NoError(t, err)
				outputcm.ObjectMeta = metav1.ObjectMeta{}
				assert.EqualValues(t, test.outputcm, outputcm)
			} else {
				assert.Regexp(t, test.err, err)
			}
		})
	}
}
