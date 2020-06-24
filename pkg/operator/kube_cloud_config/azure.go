package kube_cloud_config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"

	configv1 "github.com/openshift/api/config/v1"

	"github.com/openshift/cluster-config-operator/pkg/operator/operatorclient"
)

const (
	azureCloudFieldName = "cloud"
)

var (
	validAzureCloudNames = map[configv1.AzureCloudEnvironment]bool{
		configv1.AzurePublicCloud:       true,
		configv1.AzureUSGovernmentCloud: true,
		configv1.AzureChinaCloud:        true,
		configv1.AzureGermanCloud:       true,
	}

	validAzureCloudNameValues = func() []string {
		v := make([]string, 0, len(validAzureCloudNames))
		for n := range validAzureCloudNames {
			v = append(v, string(n))
		}
		return v
	}()
)

// azureTransformer implements the cloudConfigTransformer. It uses the input ConfigMap and infra.status.platformStatus.azure.cloudName
// to create a new config that has the cloud field set.
// It returns an error if the platform is not AzurePlatformType.
func azureTransformer(input *corev1.ConfigMap, key string, infra *configv1.Infrastructure) (*corev1.ConfigMap, error) {
	if !(infra.Status.PlatformStatus != nil &&
		infra.Status.PlatformStatus.Type == configv1.AzurePlatformType) {
		return nil, fmt.Errorf("invalid platform, expected to be Azure")
	}

	cloud := configv1.AzurePublicCloud
	if azurePlatform := infra.Status.PlatformStatus.Azure; azurePlatform != nil {
		if c := azurePlatform.CloudName; c != "" {
			if !validAzureCloudNames[c] {
				return nil, field.NotSupported(field.NewPath("status", "platformStatus", "azure", "cloudName"), c, validAzureCloudNameValues)
			}
			cloud = c
		}
	}

	output := input.DeepCopy()
	output.Namespace = operatorclient.GlobalMachineSpecifiedConfigNamespace
	output.Name = targetConfigName
	delete(output.Data, key)
	delete(output.BinaryData, key)

	var inCfgRaw []byte
	if v, ok := input.Data[key]; ok {
		inCfgRaw = []byte(v)
	} else if v, ok := input.BinaryData[key]; ok {
		inCfgRaw = v
	}

	useInCfg := false

	var cfg map[string]interface{}
	if len(inCfgRaw) > 0 {
		if err := yaml.Unmarshal(inCfgRaw, &cfg); err != nil {
			return nil, fmt.Errorf("failed to read the cloud.conf: %w", err)
		}
		if inCloudUntyped, ok := cfg[azureCloudFieldName]; ok {
			inCloud, ok := inCloudUntyped.(string)
			if !ok {
				return nil, fmt.Errorf("invalid user-provided cloud.conf: \"cloud\" field is not a string")
			}
			if len(inCloud) > 0 {
				if !strings.EqualFold(inCloud, string(cloud)) {
					return nil, fmt.Errorf("invalid user-provided cloud.conf: \"cloud\" field in user-provided cloud.conf conflicts with infrastructure object")
				}
				useInCfg = true
			}
		}
	}

	outCfgRaw := inCfgRaw
	if !useInCfg {
		if cfg == nil {
			cfg = make(map[string]interface{}, 1)
		}
		cfg[azureCloudFieldName] = string(cloud)
		outCfgBuffer := &bytes.Buffer{}
		encoder := json.NewEncoder(outCfgBuffer)
		encoder.SetIndent("", "\t")
		if err := encoder.Encode(cfg); err != nil {
			return nil, fmt.Errorf("failed to encode config: %w", err)
		}
		outCfgRaw = outCfgBuffer.Bytes()
	}

	if _, ok := input.Data[key]; ok {
		output.Data[targetConfigKey] = string(outCfgRaw) // store the config to same as input
	} else if _, ok := input.BinaryData[key]; ok {
		output.BinaryData[targetConfigKey] = outCfgRaw // store the config to same as input
	} else {
		if output.Data == nil {
			output.Data = make(map[string]string, 1)
		}
		output.Data[targetConfigKey] = string(outCfgRaw) // store the new config to input key
	}

	return output, nil
}
