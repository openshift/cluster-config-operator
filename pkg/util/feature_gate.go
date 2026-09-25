package util

import (
	"os"

	"k8s.io/klog/v2"
)

const (
	releaseVersionEnvVariableName = "OPERATOR_IMAGE_VERSION"
	unknownVersionValue           = "unknown"
)

// GetReleaseVersion gets the release version string from the env
func GetReleaseVersion() string {
	releaseVersion := os.Getenv(releaseVersionEnvVariableName)
	if len(releaseVersion) == 0 {
		releaseVersion = unknownVersionValue
		klog.Infof("%s environment variable is missing, defaulting to %q", releaseVersionEnvVariableName, unknownVersionValue)
	}
	return releaseVersion
}
