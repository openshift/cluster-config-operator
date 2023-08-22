package featuregatesnapshot

import (
	"fmt"

	configv1 "github.com/openshift/api/config/v1"
)

// FeatureSets Contains a map of Feature names to Enabled/Disabled Feature.
//
// NOTE: The caller needs to make sure to check for the existence of the value
// using golang's existence field. A possible scenario is an upgrade where new
// FeatureSets are added and a controller has not been upgraded with a newer
// version of this file. In this upgrade scenario the map could return nil.
//
// example:
//
//	if featureSet, ok := FeatureSets["SomeNewFeature"]; ok { }
//
// If you put an item in either of these lists, put your area and name on it so we can find owners.
var FeatureSets = map[configv1.FeatureSet]*configv1.FeatureGateEnabledDisabled{
	configv1.Default: defaultFeatures,
	configv1.CustomNoUpgrade: {
		Enabled:  []configv1.FeatureGateDescription{},
		Disabled: []configv1.FeatureGateDescription{},
	},
	configv1.TechPreviewNoUpgrade: newDefaultFeatures().
		with(configv1.ValidatingAdmissionPolicy).
		with(configv1.ExternalCloudProvider).
		with(configv1.ExternalCloudProviderGCP).
		with(configv1.CSIDriverSharedResource).
		with(configv1.NodeSwap).
		with(configv1.MachineAPIProviderOpenStack).
		with(configv1.InsightsConfigAPI).
		with(configv1.RetroactiveDefaultStorageClass).
		with(configv1.DynamicResourceAllocation).
		with(configv1.AdmissionWebhookMatchConditions).
		with(configv1.AzureWorkloadIdentity).
		with(configv1.GateGatewayAPI).
		with(configv1.MaxUnavailableStatefulSet).
		without(configv1.EventedPleg).
		with(configv1.SigstoreImageVerification).
		with(configv1.GCPLabelsTags).
		with(configv1.VSphereStaticIPs).
		with(configv1.RouteExternalCertificate).
		with(configv1.AutomatedEtcdBackup).
		without(configv1.MachineAPIOperatorDisableMachineHealthCheckController).
		with(configv1.AdminNetworkPolicy).
		toFeatures(defaultFeatures),
	configv1.LatencySensitive: newDefaultFeatures().
		toFeatures(defaultFeatures),
}

var defaultFeatures = &configv1.FeatureGateEnabledDisabled{
	Enabled: []configv1.FeatureGateDescription{
		configv1.OpenShiftPodSecurityAdmission,
		configv1.AlibabaPlatform, // This is a bug, it should be TechPreviewNoUpgrade. This must be downgraded before 4.14 is shipped.
		configv1.CloudDualStackNodeIPs,
		configv1.ExternalCloudProviderAzure,
		configv1.ExternalCloudProviderExternal,
		configv1.PrivateHostedZoneAWS,
		configv1.BuildCSIVolumes,
	},
	Disabled: []configv1.FeatureGateDescription{
		configv1.RetroactiveDefaultStorageClass,
	},
}

type featureSetBuilder struct {
	forceOn  []configv1.FeatureGateDescription
	forceOff []configv1.FeatureGateDescription
}

func newDefaultFeatures() *featureSetBuilder {
	return &featureSetBuilder{}
}

func (f *featureSetBuilder) with(forceOn configv1.FeatureGateDescription) *featureSetBuilder {
	for _, curr := range f.forceOn {
		if curr.FeatureGateAttributes.Name == forceOn.FeatureGateAttributes.Name {
			panic(fmt.Errorf("coding error: %q enabled twice", forceOn.FeatureGateAttributes.Name))
		}
	}
	f.forceOn = append(f.forceOn, forceOn)
	return f
}

func (f *featureSetBuilder) without(forceOff configv1.FeatureGateDescription) *featureSetBuilder {
	for _, curr := range f.forceOff {
		if curr.FeatureGateAttributes.Name == forceOff.FeatureGateAttributes.Name {
			panic(fmt.Errorf("coding error: %q disabled twice", forceOff.FeatureGateAttributes.Name))
		}
	}
	f.forceOff = append(f.forceOff, forceOff)
	return f
}

func (f *featureSetBuilder) isForcedOff(needle configv1.FeatureGateDescription) bool {
	for _, forcedOff := range f.forceOff {
		if needle.FeatureGateAttributes.Name == forcedOff.FeatureGateAttributes.Name {
			return true
		}
	}
	return false
}

func (f *featureSetBuilder) isForcedOn(needle configv1.FeatureGateDescription) bool {
	for _, forceOn := range f.forceOn {
		if needle.FeatureGateAttributes.Name == forceOn.FeatureGateAttributes.Name {
			return true
		}
	}
	return false
}

func (f *featureSetBuilder) toFeatures(defaultFeatures *configv1.FeatureGateEnabledDisabled) *configv1.FeatureGateEnabledDisabled {
	finalOn := []configv1.FeatureGateDescription{}
	finalOff := []configv1.FeatureGateDescription{}

	// only add the default enabled features if they haven't been explicitly set off
	for _, defaultOn := range defaultFeatures.Enabled {
		if !f.isForcedOff(defaultOn) {
			finalOn = append(finalOn, defaultOn)
		}
	}
	for _, currOn := range f.forceOn {
		if f.isForcedOff(currOn) {
			panic("coding error, you can't have features both on and off")
		}
		found := false
		for _, alreadyOn := range finalOn {
			if alreadyOn.FeatureGateAttributes.Name == currOn.FeatureGateAttributes.Name {
				found = true
			}
		}
		if found {
			continue
		}

		finalOn = append(finalOn, currOn)
	}

	// only add the default disabled features if they haven't been explicitly set on
	for _, defaultOff := range defaultFeatures.Disabled {
		if !f.isForcedOn(defaultOff) {
			finalOff = append(finalOff, defaultOff)
		}
	}
	for _, currOff := range f.forceOff {
		finalOff = append(finalOff, currOff)
	}

	return &configv1.FeatureGateEnabledDisabled{
		Enabled:  finalOn,
		Disabled: finalOff,
	}
}
