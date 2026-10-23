package kubecloudconfig

import (
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	configv1 "github.com/openshift/api/config/v1"
	operatorclient "github.com/openshift/cluster-config-operator/pkg/operator/operatorclient"
)

// ValidateFile verifies a file exists, has content, and is a regular file
func ValidateFile(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("failed to stat %s: %w", path, err)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if st.Size() <= 0 {
		return fmt.Errorf("%s is empty", path)
	}
	return nil
}

// BootstrapTransform implements the cloudConfigTransformer during bootstrapping.
// It uses the input ConfigMap and Infrastructure provided by files on the bootstrap
// host to create a new config that has the cloud field set.
func BootstrapTransform(infrastructureFile string, cloudProviderFile string) ([]byte, error) {

	// Read, parse, and save the infrastructure object
	var clusterInfrastructure configv1.Infrastructure
	fileData, err := os.ReadFile(infrastructureFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read infrastructure file: %w", err)
	}
	err = yaml.Unmarshal(fileData, &clusterInfrastructure)
	if err != nil {
		return nil, fmt.Errorf("failed unmarshal infrastructure: %w", err)
	}

	// Read, parse, and save the user provided cloud configmap
	var cloudProviderConfigInput corev1.ConfigMap
	if len(cloudProviderFile) > 0 {
		fileData, err = os.ReadFile(cloudProviderFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read cloud provider file: %w", err)
		}
		err = yaml.Unmarshal(fileData, &cloudProviderConfigInput)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal cloud provider: %w", err)
		}
	}

	// Determine the platform type
	var platformName configv1.PlatformType
	if pstatus := clusterInfrastructure.Status.PlatformStatus; pstatus != nil {
		platformName = pstatus.Type
	}
	if len(platformName) == 0 {
		platformName = clusterInfrastructure.Status.Platform
	}

	// Determine the platform specific transformer method to use
	cloudConfigTransformers := cloudConfigTransformers()
	cloudConfigTransformerFn, ok := cloudConfigTransformers[platformName]
	if !ok {
		cloudConfigTransformerFn = asIsTransformer
	}
	target, err := cloudConfigTransformerFn(&cloudProviderConfigInput, clusterInfrastructure.Spec.CloudConfig.Key, &clusterInfrastructure)
	if err != nil {
		return nil, fmt.Errorf("failed to transform cloud config: %w", err)
	}

	target.Name = targetConfigName
	target.Namespace = operatorclient.GlobalMachineSpecifiedConfigNamespace
	/* ApplyConfigMap() */

	targetCloudConfigMapData, err := yaml.Marshal(target)
	if err != nil {
		return nil, fmt.Errorf("failed to marhsal cloud config: %w", err)
	}

	return targetCloudConfigMapData, nil
}
