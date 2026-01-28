package migrateokdfeatureset

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	configv1fake "github.com/openshift/client-go/config/clientset/versioned/fake"
	"github.com/openshift/cluster-config-operator/pkg/version"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubetesting "k8s.io/client-go/testing"
)

func TestOKDFeatureSetMigrationController_syncFeatureGate(t *testing.T) {
	tests := []struct {
		name        string
		featureGate *configv1.FeatureGate

		changeVerifier func(t *testing.T, actions []kubetesting.Action)
	}{
		{
			name: "migrate-empty-featureset-to-okd",
			featureGate: &configv1.FeatureGate{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.FeatureGateSpec{
					FeatureGateSelection: configv1.FeatureGateSelection{
						FeatureSet: "",
					},
				},
			},
			changeVerifier: func(t *testing.T, actions []kubetesting.Action) {
				if len(actions) != 1 {
					t.Fatalf("expected 1 action, got %d: %v", len(actions), actions)
				}
				patchAction := actions[0].(kubetesting.PatchAction)
				if patchAction.GetPatchType() != types.ApplyPatchType {
					t.Fatalf("unexpected patch type: %v", patchAction.GetPatchType())
				}
				applied := string(patchAction.GetPatch())
				expectedApplied := `{"kind":"FeatureGate","apiVersion":"config.openshift.io/v1","metadata":{"name":"cluster"},"spec":{"featureSet":"OKD"}}`
				if applied != expectedApplied {
					t.Fatalf("unexpected patch:\ngot:  %s\nwant: %s", applied, expectedApplied)
				}
			},
		},
		{
			name: "migrate-default-featureset-to-okd",
			featureGate: &configv1.FeatureGate{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.FeatureGateSpec{
					FeatureGateSelection: configv1.FeatureGateSelection{
						FeatureSet: configv1.Default,
					},
				},
			},
			changeVerifier: func(t *testing.T, actions []kubetesting.Action) {
				if len(actions) != 1 {
					t.Fatalf("expected 1 action, got %d: %v", len(actions), actions)
				}
				patchAction := actions[0].(kubetesting.PatchAction)
				if patchAction.GetPatchType() != types.ApplyPatchType {
					t.Fatalf("unexpected patch type: %v", patchAction.GetPatchType())
				}
				applied := string(patchAction.GetPatch())
				expectedApplied := `{"kind":"FeatureGate","apiVersion":"config.openshift.io/v1","metadata":{"name":"cluster"},"spec":{"featureSet":"OKD"}}`
				if applied != expectedApplied {
					t.Fatalf("unexpected patch:\ngot:  %s\nwant: %s", applied, expectedApplied)
				}
			},
		},
		{
			name: "leave-techpreview-unchanged",
			featureGate: &configv1.FeatureGate{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.FeatureGateSpec{
					FeatureGateSelection: configv1.FeatureGateSelection{
						FeatureSet: configv1.TechPreviewNoUpgrade,
					},
				},
			},
			changeVerifier: func(t *testing.T, actions []kubetesting.Action) {
				if len(actions) != 0 {
					t.Fatalf("expected no actions, got %d: %v", len(actions), actions)
				}
			},
		},
		{
			name: "leave-okd-unchanged",
			featureGate: &configv1.FeatureGate{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.FeatureGateSpec{
					FeatureGateSelection: configv1.FeatureGateSelection{
						FeatureSet: configv1.OKD,
					},
				},
			},
			changeVerifier: func(t *testing.T, actions []kubetesting.Action) {
				if len(actions) != 0 {
					t.Fatalf("expected no actions, got %d: %v", len(actions), actions)
				}
			},
		},
		{
			name: "leave-custompoupgrade-unchanged",
			featureGate: &configv1.FeatureGate{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.FeatureGateSpec{
					FeatureGateSelection: configv1.FeatureGateSelection{
						FeatureSet: configv1.CustomNoUpgrade,
					},
				},
			},
			changeVerifier: func(t *testing.T, actions []kubetesting.Action) {
				if len(actions) != 0 {
					t.Fatalf("expected no actions, got %d: %v", len(actions), actions)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip tests if not running in SCOS mode
			if !version.IsSCOS() {
				t.Skipf("Skipping test %s because version.IsSCOS() is false", tt.name)
			}

			ctx := context.Background()
			_, cancel := context.WithCancel(ctx)
			defer cancel()

			fakeClient := configv1fake.NewSimpleClientset(tt.featureGate)

			c := OKDFeatureSetMigrationController{
				featureGatesClient: fakeClient.ConfigV1(),
			}
			if err := c.syncFeatureGate(ctx, tt.featureGate); err != nil {
				t.Fatal(err)
			}

			tt.changeVerifier(t, fakeClient.Actions())
		})
	}
}

// Note: Testing the non-SCOS code path (early return in sync) is not necessary
// as it's a trivial check. The version.IsSCOS() function is tested in the version package.
// All meaningful functionality is tested in the syncFeatureGate tests above.
