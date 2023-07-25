package removelatencysensitive

import (
	"context"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	configv1fake "github.com/openshift/client-go/config/clientset/versioned/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubetesting "k8s.io/client-go/testing"
)

func TestFeatureGateController_syncFeatureGate(t *testing.T) {
	tests := []struct {
		name        string
		featureGate *configv1.FeatureGate

		changeVerifier func(t *testing.T, actions []kubetesting.Action)
	}{
		// patch type isn't supported by fake in this fake client level.
		// the actual API was GA in 1.22
		//{
		//	name: "clear-value",
		//	featureGate: &configv1.FeatureGate{
		//		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		//		Spec: configv1.FeatureGateSpec{
		//			FeatureGateSelection: configv1.FeatureGateSelection{
		//				FeatureSet: "LatencySensitive",
		//			},
		//		},
		//	},
		//	changeVerifier: func(t *testing.T, actions []kubetesting.Action) {
		//		if len(actions) != 1 {
		//			t.Fatalf("bad changes: %v", actions)
		//		}
		//		patchAction := actions[0].(kubetesting.PatchAction)
		//		if patchAction.GetPatchType() != types.ApplyPatchType {
		//			t.Fatalf("unexpected patch type: %v", patchAction.GetPatchType())
		//		}
		//		applied := string(patchAction.GetPatch())
		//		expectedApplied := `{"kind":"FeatureGate","apiVersion":"config.openshift.io/v1","metadata":{"name":"cluster"},"spec":{"featureSet":""}}`
		//		if applied != expectedApplied {
		//			t.Fatal(applied)
		//		}
		//	},
		//},
		{
			name: "leave-other-value",
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
					t.Fatalf("bad changes: %v", actions)
				}
			},
		},
		{
			name: "leave-desired-value",
			featureGate: &configv1.FeatureGate{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Spec: configv1.FeatureGateSpec{
					FeatureGateSelection: configv1.FeatureGateSelection{
						FeatureSet: configv1.Default,
					},
				},
			},
			changeVerifier: func(t *testing.T, actions []kubetesting.Action) {
				if len(actions) != 0 {
					t.Fatalf("bad changes: %v", actions)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			_, cancel := context.WithCancel(ctx)
			defer cancel()

			fakeClient := configv1fake.NewSimpleClientset(tt.featureGate)

			c := LatencySensitiveRemovalController{
				featureGatesClient: fakeClient.ConfigV1(),
			}
			if err := c.syncFeatureGate(ctx, tt.featureGate); err != nil {
				t.Fatal(err)
			}

			tt.changeVerifier(t, fakeClient.Actions())
		})
	}
}
