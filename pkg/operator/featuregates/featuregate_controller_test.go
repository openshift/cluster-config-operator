package featuregates

import (
	"context"
	"reflect"
	"testing"

	"github.com/davecgh/go-spew/spew"

	configv1 "github.com/openshift/api/config/v1"
	configv1fake "github.com/openshift/client-go/config/clientset/versioned/fake"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/configobserver/featuregates"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubetesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
)

type testFeatureGateBuilder struct {
	featureSet              configv1.FeatureSet
	customFeatures          featuregates.Features
	statusVersionToFeatures []versionFeatures
}

type versionFeatures struct {
	version  string
	features featuregates.Features
}

func featureGateBuilder() *testFeatureGateBuilder {
	return &testFeatureGateBuilder{
		statusVersionToFeatures: []versionFeatures{},
	}
}

func (f *testFeatureGateBuilder) withFeatureSet(featureSet configv1.FeatureSet) *testFeatureGateBuilder {
	f.featureSet = featureSet

	return f
}

func (f *testFeatureGateBuilder) customEnabled(enabled ...configv1.FeatureGateName) *testFeatureGateBuilder {
	f.featureSet = configv1.CustomNoUpgrade
	f.customFeatures.Enabled = enabled

	return f
}

func (f *testFeatureGateBuilder) customDisabled(disabled ...configv1.FeatureGateName) *testFeatureGateBuilder {
	f.featureSet = configv1.CustomNoUpgrade
	f.customFeatures.Disabled = disabled

	return f
}

func (f *testFeatureGateBuilder) statusEnabled(version string, enabled ...configv1.FeatureGateName) *testFeatureGateBuilder {
	for i, val := range f.statusVersionToFeatures {
		if val.version == version {
			f.statusVersionToFeatures[i].features.Enabled = enabled
			return f
		}
	}
	f.statusVersionToFeatures = append(f.statusVersionToFeatures, versionFeatures{
		version: version,
		features: featuregates.Features{
			Enabled: enabled,
		},
	})

	return f
}

func (f *testFeatureGateBuilder) statusDisabled(version string, disabled ...configv1.FeatureGateName) *testFeatureGateBuilder {
	for i, val := range f.statusVersionToFeatures {
		if val.version == version {
			f.statusVersionToFeatures[i].features.Disabled = disabled
			return f
		}
	}
	f.statusVersionToFeatures = append(f.statusVersionToFeatures, versionFeatures{
		version: version,
		features: featuregates.Features{
			Disabled: disabled,
		},
	})

	return f
}

func (f *testFeatureGateBuilder) toFeatureGate() *configv1.FeatureGate {
	ret := &configv1.FeatureGate{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: configv1.FeatureGateSpec{
			FeatureGateSelection: configv1.FeatureGateSelection{
				FeatureSet: f.featureSet,
			},
		},
	}
	if f.featureSet == configv1.CustomNoUpgrade {
		ret.Spec.FeatureGateSelection.CustomNoUpgrade = &configv1.CustomFeatureGates{
			Enabled:  f.customFeatures.Enabled,
			Disabled: f.customFeatures.Disabled,
		}
	}

	for _, features := range f.statusVersionToFeatures {
		details := configv1.FeatureGateDetails{
			Version: features.version,
		}
		for _, curr := range features.features.Enabled {
			details.Enabled = append(details.Enabled, configv1.FeatureGateAttributes{Name: curr})
		}
		for _, curr := range features.features.Disabled {
			details.Disabled = append(details.Disabled, configv1.FeatureGateAttributes{Name: curr})
		}
		ret.Status.FeatureGates = append(ret.Status.FeatureGates, details)
	}

	return ret
}

var testingFeatureSets = map[configv1.FeatureSet]*configv1.FeatureGateEnabledDisabled{
	configv1.Default: {
		Enabled: []configv1.FeatureGateDescription{
			{
				FeatureGateAttributes: configv1.FeatureGateAttributes{
					Name: "Five",
				},
			},
			{
				FeatureGateAttributes: configv1.FeatureGateAttributes{
					Name: "Six",
				},
			},
		},
		Disabled: []configv1.FeatureGateDescription{
			{
				FeatureGateAttributes: configv1.FeatureGateAttributes{
					Name: "Eggplant",
				},
			},
			{
				FeatureGateAttributes: configv1.FeatureGateAttributes{
					Name: "FoieGras",
				},
			},
		},
	},
	configv1.CustomNoUpgrade: {
		Enabled:  []configv1.FeatureGateDescription{},
		Disabled: []configv1.FeatureGateDescription{},
	},
	configv1.TechPreviewNoUpgrade: {
		Enabled: []configv1.FeatureGateDescription{
			{
				FeatureGateAttributes: configv1.FeatureGateAttributes{
					Name: "One",
				},
			},
			{
				FeatureGateAttributes: configv1.FeatureGateAttributes{
					Name: "Two",
				},
			},
		},
		Disabled: []configv1.FeatureGateDescription{
			{
				FeatureGateAttributes: configv1.FeatureGateAttributes{
					Name: "Apple",
				},
			},
			{
				FeatureGateAttributes: configv1.FeatureGateAttributes{
					Name: "Banana",
				},
			},
		},
	},
}

func TestFeatureGateController_sync(t *testing.T) {
	type fields struct {
		processVersion  string
		versionRecorder status.VersionGetter
	}
	type args struct {
		syncCtx factory.SyncContext
	}
	tests := []struct {
		name             string
		firstFeatureGate *configv1.FeatureGate
		cvoVersions      []string

		fields  fields
		args    args
		wantErr bool

		changeVerifier func(t *testing.T, actions []kubetesting.Action, versionRecorder status.VersionGetter)
	}{
		{
			name:        "add-current-version-to-empty",
			cvoVersions: []string{},
			firstFeatureGate: featureGateBuilder().
				withFeatureSet(configv1.TechPreviewNoUpgrade).
				toFeatureGate(),
			fields: fields{
				processVersion: "current-version",
			},
			changeVerifier: func(t *testing.T, actions []kubetesting.Action, versionRecorder status.VersionGetter) {
				if versionRecorder.GetVersions()[FeatureVersionName] != "current-version" {
					t.Errorf("bad version: %v", versionRecorder.GetVersions())
				}
				if len(actions) != 1 {
					t.Fatalf("bad changes: %v", actions)
				}
				updateAction := actions[0].(kubetesting.UpdateAction)
				actual := updateAction.GetObject().(*configv1.FeatureGate)
				expected := featureGateBuilder().
					withFeatureSet(configv1.TechPreviewNoUpgrade).
					statusEnabled("current-version", "One", "Two").
					statusDisabled("current-version",
						"Apple",    // specific
						"Banana",   // specific
						"Eggplant", // known
						"Five",     // known
						"FoieGras", // known
						"Six",      // known
					).
					toFeatureGate()
				if !reflect.DeepEqual(actual, expected) {
					t.Fatal(spew.Sdump(actual))
				}
			},
		},
		{
			name:        "no-action-if-current-matches",
			cvoVersions: []string{"current-version"},
			firstFeatureGate: featureGateBuilder().
				withFeatureSet(configv1.TechPreviewNoUpgrade).
				statusEnabled("current-version", "One", "Two").
				statusDisabled("current-version",
					"Apple",    // specific
					"Banana",   // specific
					"Eggplant", // known
					"Five",     // known
					"FoieGras", // known
					"Six",      // known
				).
				toFeatureGate(),
			fields: fields{
				processVersion: "current-version",
			},
			changeVerifier: func(t *testing.T, actions []kubetesting.Action, versionRecorder status.VersionGetter) {
				if versionRecorder.GetVersions()[FeatureVersionName] != "current-version" {
					t.Errorf("bad version: %v", versionRecorder.GetVersions())
				}
				if len(actions) != 0 {
					t.Fatalf("bad changes: %v", actions)
				}
			},
		},
		{
			name:        "replace-existing-if-changed",
			cvoVersions: []string{"current-version"},
			firstFeatureGate: featureGateBuilder().
				withFeatureSet(configv1.Default).
				statusEnabled("current-version", "One", "Two").
				statusDisabled("current-version", "Apple", "Banana").
				toFeatureGate(),
			fields: fields{
				processVersion: "current-version",
			},
			changeVerifier: func(t *testing.T, actions []kubetesting.Action, versionRecorder status.VersionGetter) {
				if versionRecorder.GetVersions()[FeatureVersionName] != "current-version" {
					t.Errorf("bad version: %v", versionRecorder.GetVersions())
				}
				if len(actions) != 1 {
					t.Fatalf("bad changes: %v", actions)
				}
				updateAction := actions[0].(kubetesting.UpdateAction)
				actual := updateAction.GetObject().(*configv1.FeatureGate)
				expected := featureGateBuilder().
					withFeatureSet(configv1.Default).
					statusEnabled("current-version", "Five", "Six").
					statusDisabled("current-version",
						"Apple",    // known
						"Banana",   // known
						"Eggplant", // specific
						"FoieGras", // specific
						"One",      // known
						"Two",      // known
					).
					toFeatureGate()
				if !reflect.DeepEqual(actual, expected) {
					t.Fatal(spew.Sdump(actual))
				}
			},
		},
		{
			name:        "resolve-custom",
			cvoVersions: []string{"current-version"},
			firstFeatureGate: featureGateBuilder().
				withFeatureSet(configv1.CustomNoUpgrade).
				customEnabled("Eleven", "Twelve").
				customDisabled("Kale", "Lettuce").
				toFeatureGate(),
			fields: fields{
				processVersion: "current-version",
			},
			changeVerifier: func(t *testing.T, actions []kubetesting.Action, versionRecorder status.VersionGetter) {
				if versionRecorder.GetVersions()[FeatureVersionName] != "current-version" {
					t.Errorf("bad version: %v", versionRecorder.GetVersions())
				}
				if len(actions) != 1 {
					t.Fatalf("bad changes: %v", actions)
				}
				updateAction := actions[0].(kubetesting.UpdateAction)
				actual := updateAction.GetObject().(*configv1.FeatureGate)
				expected := featureGateBuilder().
					withFeatureSet(configv1.CustomNoUpgrade).
					customEnabled("Eleven", "Twelve").
					customDisabled("Kale", "Lettuce").
					statusEnabled("current-version",
						"Eleven",  // from spec
						"Five",    // from default
						"Six",     // from default
						"Twelve"). // from spec
					statusDisabled("current-version",
						"Eggplant", // from default
						"FoieGras", // from default
						"Kale",     // from spec
						"Lettuce"). // from spec
					toFeatureGate()
				if !reflect.DeepEqual(actual, expected) {
					t.Fatal(spew.Sdump(actual))
				}
			},
		},
		{
			name:        "add-current-version-to-empty-with-existing",
			cvoVersions: []string{"prior-version"},
			firstFeatureGate: featureGateBuilder().
				withFeatureSet(configv1.TechPreviewNoUpgrade).
				statusEnabled("prior-version", "Fifteen", "Sixteen").
				statusDisabled("prior-version", "Olive", "Potato"). // nearly left an 'e' here ;)
				toFeatureGate(),
			fields: fields{
				processVersion: "current-version",
			},
			changeVerifier: func(t *testing.T, actions []kubetesting.Action, versionRecorder status.VersionGetter) {
				if versionRecorder.GetVersions()[FeatureVersionName] != "current-version" {
					t.Errorf("bad version: %v", versionRecorder.GetVersions())
				}
				if len(actions) != 1 {
					t.Fatalf("bad changes: %v", actions)
				}
				updateAction := actions[0].(kubetesting.UpdateAction)
				actual := updateAction.GetObject().(*configv1.FeatureGate)
				expected := featureGateBuilder().
					withFeatureSet(configv1.TechPreviewNoUpgrade).
					statusEnabled("current-version", "One", "Two").
					statusDisabled("current-version",
						"Apple",    // specific
						"Banana",   // specific
						"Eggplant", // known
						"Five",     // known
						"FoieGras", // known
						"Six",      // known
					).
					statusEnabled("prior-version", "Fifteen", "Sixteen").
					statusDisabled("prior-version", "Olive", "Potato").
					toFeatureGate()
				if !reflect.DeepEqual(actual, expected) {
					t.Log(spew.Sdump(expected))
					t.Fatal(spew.Sdump(actual))
				}
			},
		},
		{
			name:        "no-action-if-current-matches-with-existing",
			cvoVersions: []string{"current-version", "prior-version"},
			firstFeatureGate: featureGateBuilder().
				withFeatureSet(configv1.TechPreviewNoUpgrade).
				statusEnabled("current-version", "One", "Two").
				statusDisabled("current-version",
					"Apple",    // specific
					"Banana",   // specific
					"Eggplant", // known
					"Five",     // known
					"FoieGras", // known
					"Six",      // known
				).
				statusEnabled("prior-version", "Fifteen", "Sixteen").
				statusDisabled("prior-version", "Olive", "Potato"). // nearly left an 'e' here ;)
				toFeatureGate(),
			fields: fields{
				processVersion: "current-version",
			},
			changeVerifier: func(t *testing.T, actions []kubetesting.Action, versionRecorder status.VersionGetter) {
				if versionRecorder.GetVersions()[FeatureVersionName] != "current-version" {
					t.Errorf("bad version: %v", versionRecorder.GetVersions())
				}
				if len(actions) != 0 {
					t.Fatalf("bad changes: %v", actions)
				}
			},
		},
		{
			name:        "replace-existing-if-changed-with-existing",
			cvoVersions: []string{"current-version", "prior-version"},
			firstFeatureGate: featureGateBuilder().
				withFeatureSet(configv1.Default).
				statusEnabled("current-version", "One", "Two").
				statusDisabled("current-version", "Apple", "Banana").
				statusEnabled("prior-version", "Fifteen", "Sixteen").
				statusDisabled("prior-version", "Olive", "Potato"). // nearly left an 'e' here ;)
				toFeatureGate(),
			fields: fields{
				processVersion: "current-version",
			},
			changeVerifier: func(t *testing.T, actions []kubetesting.Action, versionRecorder status.VersionGetter) {
				if versionRecorder.GetVersions()[FeatureVersionName] != "current-version" {
					t.Errorf("bad version: %v", versionRecorder.GetVersions())
				}
				if len(actions) != 1 {
					t.Fatalf("bad changes: %v", actions)
				}
				updateAction := actions[0].(kubetesting.UpdateAction)
				actual := updateAction.GetObject().(*configv1.FeatureGate)
				expected := featureGateBuilder().
					withFeatureSet(configv1.Default).
					statusEnabled("current-version", "Five", "Six").
					statusDisabled("current-version",
						"Apple",    // known
						"Banana",   // known
						"Eggplant", // specific
						"FoieGras", // specific
						"One",      // known
						"Two",      // known
					).
					statusEnabled("prior-version", "Fifteen", "Sixteen").
					statusDisabled("prior-version", "Olive", "Potato"). // nearly left an 'e' here ;)
					toFeatureGate()
				if !reflect.DeepEqual(actual, expected) {
					t.Log(spew.Sdump(expected))
					t.Fatal(spew.Sdump(actual))
				}
			},
		},
		{
			name:        "prune-removed-versions",
			cvoVersions: []string{"current-version"},
			firstFeatureGate: featureGateBuilder().
				withFeatureSet(configv1.Default).
				statusEnabled("current-version", "One", "Two").
				statusDisabled("current-version", "Apple", "Banana").
				statusEnabled("prior-version", "Fifteen", "Sixteen").
				statusDisabled("prior-version", "Olive", "Potato"). // nearly left an 'e' here ;)
				toFeatureGate(),
			fields: fields{
				processVersion: "current-version",
			},
			changeVerifier: func(t *testing.T, actions []kubetesting.Action, versionRecorder status.VersionGetter) {
				if versionRecorder.GetVersions()[FeatureVersionName] != "current-version" {
					t.Errorf("bad version: %v", versionRecorder.GetVersions())
				}
				if len(actions) != 1 {
					t.Fatalf("bad changes: %v", actions)
				}
				updateAction := actions[0].(kubetesting.UpdateAction)
				actual := updateAction.GetObject().(*configv1.FeatureGate)
				expected := featureGateBuilder().
					withFeatureSet(configv1.Default).
					statusEnabled("current-version", "Five", "Six").
					statusDisabled("current-version",
						"Apple",    // known
						"Banana",   // known
						"Eggplant", // specific
						"FoieGras", // specific
						"One",      // known
						"Two",      // known
					).toFeatureGate()
				if !reflect.DeepEqual(actual, expected) {
					t.Fatal(spew.Sdump(actual))
				}
			},
		},
		{
			name:        "prune-removed-versions-with-no-other-change",
			cvoVersions: []string{"current-version"},
			firstFeatureGate: featureGateBuilder().
				withFeatureSet(configv1.TechPreviewNoUpgrade).
				statusEnabled("current-version", "One", "Two").
				statusDisabled("current-version", "Apple", "Banana").
				statusEnabled("prior-version", "Fifteen", "Sixteen").
				statusDisabled("prior-version", "Olive", "Potato"). // nearly left an 'e' here ;)
				toFeatureGate(),
			fields: fields{
				processVersion: "current-version",
			},
			changeVerifier: func(t *testing.T, actions []kubetesting.Action, versionRecorder status.VersionGetter) {
				if versionRecorder.GetVersions()[FeatureVersionName] != "current-version" {
					t.Errorf("bad version: %v", versionRecorder.GetVersions())
				}
				if len(actions) != 1 {
					t.Fatalf("bad changes: %v", actions)
				}
				updateAction := actions[0].(kubetesting.UpdateAction)
				actual := updateAction.GetObject().(*configv1.FeatureGate)
				expected := featureGateBuilder().
					withFeatureSet(configv1.TechPreviewNoUpgrade).
					statusEnabled("current-version", "One", "Two").
					statusDisabled("current-version",
						"Apple",    // specific
						"Banana",   // specific
						"Eggplant", // known
						"Five",     // known
						"FoieGras", // known
						"Six",      // known
					).
					toFeatureGate()
				if !reflect.DeepEqual(actual, expected) {
					t.Fatal(spew.Sdump(actual))
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			_, cancel := context.WithCancel(ctx)
			defer cancel()

			var fakeClient *configv1fake.Clientset

			featureGateIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			featureGateLister := configlistersv1.NewFeatureGateLister(featureGateIndexer)
			if tt.firstFeatureGate != nil {
				featureGateIndexer.Add(tt.firstFeatureGate)
				fakeClient = configv1fake.NewSimpleClientset(tt.firstFeatureGate)
			} else {
				fakeClient = configv1fake.NewSimpleClientset()
			}

			var clusterVersionLister configlistersv1.ClusterVersionLister
			cvo := &configv1.ClusterVersion{
				ObjectMeta: metav1.ObjectMeta{Name: "version"},
				Status: configv1.ClusterVersionStatus{
					History: []configv1.UpdateHistory{},
				},
			}
			for _, version := range tt.cvoVersions {
				cvo.Status.History = append(cvo.Status.History, configv1.UpdateHistory{Version: version})
			}
			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			indexer.Add(cvo)
			clusterVersionLister = configlistersv1.NewClusterVersionLister(indexer)

			c := FeatureGateController{
				processVersion:       tt.fields.processVersion,
				featureGatesClient:   fakeClient.ConfigV1(),
				featureGatesLister:   featureGateLister,
				clusterVersionLister: clusterVersionLister,
				featureSetMap:        testingFeatureSets,
				versionRecorder:      status.NewVersionGetter(),
				eventRecorder:        events.NewInMemoryRecorder("fakee"),
			}
			if err := c.sync(ctx, tt.args.syncCtx); (err != nil) != tt.wantErr {
				t.Errorf("sync() error = %v, wantErr %v", err, tt.wantErr)
			}

			tt.changeVerifier(t, fakeClient.Actions(), c.versionRecorder)
		})
	}
}
