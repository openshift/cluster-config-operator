package kube_cloud_config

import (
	"bytes"
	"fmt"
	"text/template"

	configv1 "github.com/openshift/api/config/v1"
	"github.com/openshift/cluster-config-operator/pkg/operator/kube_cloud_config/internal/aws"
	"github.com/openshift/cluster-config-operator/pkg/operator/operatorclient"
	"gopkg.in/gcfg.v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// awsTransformer implements the cloudConfigTransformer. It uses the input ConfigMap and infra.status.platformStatus.AWS.ServiceEndpoints
// to create a new config that include the ServiceOverrides sections. The transformer uses infra.status.platformStatus.region as the
// signing service for all the ServiceOverrides.
// It returns an error if the platform is not AWSPlatformType.
func awsTransformer(input *corev1.ConfigMap, key string, infra *configv1.Infrastructure) (*corev1.ConfigMap, error) {
	if !(infra.Status.PlatformStatus != nil &&
		infra.Status.PlatformStatus.Type == configv1.AWSPlatformType) {
		return nil, fmt.Errorf("invalid platform, expected to be AWS")
	}
	if infra.Status.PlatformStatus.AWS == nil || len(infra.Status.PlatformStatus.AWS.ServiceEndpoints) == 0 {
		return asIsTransformer(input, key, infra) // no transformation required
	}

	var region string
	if infra.Status.PlatformStatus != nil && infra.Status.PlatformStatus.AWS != nil {
		region = infra.Status.PlatformStatus.AWS.Region
	}
	if region == "" {
		return nil, field.Required(field.NewPath("status", "platformStatus", "aws", "region"), "region is required to be set for AWS platform")
	}

	output := input.DeepCopy()
	output.Namespace = operatorclient.GlobalMachineSpecifiedConfigNamespace
	output.Name = targetConfigName
	delete(output.Data, key)
	delete(output.BinaryData, key)

	inCfgRaw := &bytes.Buffer{}
	if v, ok := input.Data[key]; ok {
		inCfgRaw = bytes.NewBufferString(v)
	} else if v, ok := input.BinaryData[key]; ok {
		inCfgRaw = bytes.NewBuffer(v)
	}

	if len(inCfgRaw.String()) > 0 {
		var cfg aws.CloudConfig
		err := gcfg.ReadInto(&cfg, bytes.NewBufferString(inCfgRaw.String()))
		if err != nil {
			return nil, fmt.Errorf("failed to read the cloud.conf: %w", err)
		}

		if len(cfg.ServiceOverride) > 0 {
			return nil, fmt.Errorf("invalid user provided cloud.conf: user provided cloud.conf and infrastructure object both include service overrides")
		}
	}

	overrides, err := serviceOverrides(infra.Status.PlatformStatus.AWS.ServiceEndpoints, region)
	if err != nil {
		return nil, fmt.Errorf("failed to create service overrides section for cloud.conf: %w", err)
	}

	_, err = inCfgRaw.WriteString(overrides)
	if err != nil {
		return nil, fmt.Errorf("failed to append service overrides section for cloud.conf: %w", err)
	}

	if _, ok := input.Data[key]; ok {
		output.Data[targetConfigKey] = inCfgRaw.String() // store the config to same as input
	} else if _, ok := input.BinaryData[key]; ok {
		output.BinaryData[targetConfigKey] = inCfgRaw.Bytes() // store the config to same as input
	} else {
		if output.Data == nil {
			output.Data = map[string]string{}
		}
		output.Data[targetConfigKey] = inCfgRaw.String() // store the new config to input key
	}

	return output, nil
}

// serviceOverrides returns a section of configuration that matches the expected based on https://github.com/kubernetes/kubernetes/blob/46b2891089574749b3d98b2a09fc3270789795b6/staging/src/k8s.io/legacy-cloud-providers/aws/aws.go#L595-L607
// since there is no writer for gopkg.in/gcfg.v1 available, we have to manually create a section block that is compatible with gcfg.
func serviceOverrides(overrides []configv1.AWSServiceEndpoint, defaultRegion string) (string, error) {
	tinput := struct {
		DefaultRegion    string
		ServiceOverrides []configv1.AWSServiceEndpoint
	}{DefaultRegion: defaultRegion, ServiceOverrides: overrides}

	buf := &bytes.Buffer{}
	err := template.Must(template.New("service_overrides").Parse(serviceOverrideTmpl)).Execute(buf, tinput)
	return buf.String(), err
}

// serviceOverrideTmpl can be used to generate a list of serviceOverride sections given,
// input: {DefaultRegion(string), ServiceOverrides (list configv1.AWSServiceEndpoint)}
var serviceOverrideTmpl = `
{{- range $idx, $service := .ServiceOverrides }}
[ServiceOverride "{{ $idx }}"]
	Service = {{ $service.Name }}
	Region = {{ $.DefaultRegion }}
	URL = {{ $service.URL }}
	SigningRegion = {{ $.DefaultRegion }}
{{ end }}`
