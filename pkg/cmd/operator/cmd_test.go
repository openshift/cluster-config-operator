package operator

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	configv1 "github.com/openshift/api/config/v1"
	operatorv1alpha1 "github.com/openshift/api/operator/v1alpha1"
	"github.com/openshift/library-go/pkg/config/configdefaults"
	"github.com/openshift/library-go/pkg/config/serving"
	"github.com/openshift/library-go/pkg/crypto"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	kyaml "k8s.io/apimachinery/pkg/util/yaml"
	genericapiserver "k8s.io/apiserver/pkg/server"
	certutil "k8s.io/client-go/util/cert"
	"sigs.k8s.io/yaml"
)

// These tests cover TLS group (curve) preferences support on the operator's
// serving endpoint, the only TLS-serving surface of the cluster-config-operator.
//
// The propagation chain under test mirrors the library-go controllercmd path
// started by NewOperator in this package:
//
//	cluster-wide TLSSecurityProfile.groups
//	  -> CVO observes the groups and, with the TLSGroupPreferences feature gate
//	     enabled, writes them as numeric servingInfo.curvePreferences into the
//	     openshift-config-operator-config ConfigMap (config.openshift.io/inject-tls)
//	  -> controllercmd decodes the mounted config.yaml into GenericOperatorConfig
//	  -> ControllerBuilder.WithServer applies recommended serving defaults
//	  -> serving.ToServerConfig/ToServingOptions convert servingInfo into
//	     Kubernetes SecureServingOptions
//	  -> SecureServingOptions.ApplyTo validates the curve IDs and stores them on
//	     SecureServingInfo, from where they reach the serving tls.Config
//
// When the gate is disabled, or the profile carries no recognized groups, the
// CVO removes the curvePreferences field and the operator keeps Go's default
// curve selection.

// cvoInjectedOperatorConfig renders the config.yaml entry the CVO writes into
// the openshift-config-operator-config ConfigMap. curvePreferences holds the
// numeric curve IDs the CVO derives from the cluster-wide
// TLSSecurityProfile.groups; a nil value means the field is absent, which is
// what the CVO writes when the TLSGroupPreferences gate is disabled or no
// recognized groups remain.
func cvoInjectedOperatorConfig(curvePreferences []int32) string {
	var config strings.Builder
	config.WriteString(`apiVersion: operator.openshift.io/v1alpha1
kind: GenericOperatorConfig
servingInfo:
  minTLSVersion: VersionTLS12
  cipherSuites:
  - TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
`)
	if len(curvePreferences) > 0 {
		config.WriteString("  curvePreferences:\n")
		for _, curveID := range curvePreferences {
			fmt.Fprintf(&config, "  - %d\n", curveID)
		}
	}
	return config.String()
}

// decodeOperatorConfig mirrors controllercmd: the config file content is
// converted to JSON, decoded as unstructured, and converted into the
// GenericOperatorConfig consumed by the operator command.
func decodeOperatorConfig(t *testing.T, configYAML string) *operatorv1alpha1.GenericOperatorConfig {
	t.Helper()

	data, err := kyaml.ToJSON([]byte(configYAML))
	if err != nil {
		t.Fatal(err)
	}
	uncastObj, err := runtime.Decode(unstructured.UnstructuredJSONScheme, data)
	if err != nil {
		t.Fatal(err)
	}
	unstructuredConfig, ok := uncastObj.(*unstructured.Unstructured)
	if !ok {
		t.Fatalf("expected an unstructured object, got %T", uncastObj)
	}

	config := &operatorv1alpha1.GenericOperatorConfig{}
	configCopy := unstructuredConfig.DeepCopy()
	configCopy.SetGroupVersionKind(operatorv1alpha1.GroupVersion.WithKind("GenericOperatorConfig"))
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(configCopy.Object, config); err != nil {
		t.Fatal(err)
	}
	return config
}

// applyServingOptions mirrors the serving path of
// controllercmd.ControllerBuilder.WithServer followed by
// serving.ToServerConfig: recommended defaults are applied to a copy of the
// servingInfo, the servingInfo is converted into Kubernetes serving options and
// applied to the SecureServingInfo backing the operator's metrics endpoint.
// The returned error comes from SecureServingOptions.ApplyTo, which validates
// the curve IDs.
func applyServingOptions(t *testing.T, servingInfo configv1.HTTPServingInfo) (*genericapiserver.SecureServingInfo, error) {
	t.Helper()

	// mirrors controllercmd.ControllerBuilder.WithServer
	defaultedServingInfo := *servingInfo.DeepCopy()
	configdefaults.SetRecommendedHTTPServingInfoDefaults(&defaultedServingInfo)

	// In the cluster the recommended cert locations point at the mounted
	// serving-cert secret; point them at a temporary self-signed pair so
	// ApplyTo can load them. Certificate loading is orthogonal to curve
	// propagation.
	certPEM, keyPEM, err := certutil.GenerateSelfSignedCertKey("localhost", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	certDir := t.TempDir()
	defaultedServingInfo.CertFile = filepath.Join(certDir, "tls.crt")
	defaultedServingInfo.KeyFile = filepath.Join(certDir, "tls.key")
	if err := os.WriteFile(defaultedServingInfo.CertFile, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defaultedServingInfo.KeyFile, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}

	servingOptions, err := serving.ToServingOptions(defaultedServingInfo)
	if err != nil {
		return nil, err
	}

	// ApplyTo binds the configured address; point it at an ephemeral listener
	// so the test does not occupy the production metrics port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	servingOptions.SecureServingOptions.Listener = listener

	var secureServing *genericapiserver.SecureServingInfo
	// The loopback variant used in production additionally mints a loopback
	// client certificate, which is orthogonal to curve propagation.
	if err := servingOptions.SecureServingOptions.ApplyTo(&secureServing); err != nil {
		return nil, err
	}
	if secureServing == nil {
		t.Fatal("secure serving info was not populated")
	}
	return secureServing, nil
}

// appliedCurvePreferences runs a CVO-injected operator config through the
// decode and serving options pipeline and returns the curve preferences that
// reach the operator's serving configuration.
func appliedCurvePreferences(t *testing.T, curvePreferences []int32) []tls.CurveID {
	t.Helper()

	config := decodeOperatorConfig(t, cvoInjectedOperatorConfig(curvePreferences))
	secureServing, err := applyServingOptions(t, config.ServingInfo)
	if err != nil {
		t.Fatal(err)
	}
	return secureServing.CurvePreferences
}

func TestCVOInjectedTLSGroupsReachServingConfig(t *testing.T) {
	// The numeric curve IDs the CVO writes for the default (Intermediate)
	// profile groups when the TLSGroupPreferences feature gate is enabled.
	intermediateGroups := configv1.TLSProfiles[configv1.TLSProfileIntermediateType].Groups
	injectedCurveIDs, unrecognizedGroups := crypto.TLSGroupsToCurvePreferences(intermediateGroups)
	assert.Empty(t, unrecognizedGroups)
	assert.NotEmpty(t, injectedCurveIDs)
	expectedCurves := make([]tls.CurveID, 0, len(injectedCurveIDs))
	for _, curveID := range injectedCurveIDs {
		expectedCurves = append(expectedCurves, tls.CurveID(curveID))
	}

	cases := []struct {
		name             string
		curvePreferences []int32
		wantCurves       []tls.CurveID
	}{
		{
			name:             "configured groups are consumed by the operator",
			curvePreferences: injectedCurveIDs,
			wantCurves:       expectedCurves,
		},
		{
			name:             "gate disabled or groups unset keeps default curve selection",
			curvePreferences: nil,
			wantCurves:       nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := decodeOperatorConfig(t, cvoInjectedOperatorConfig(tc.curvePreferences))
			assert.Equal(t, tc.curvePreferences, config.ServingInfo.CurvePreferences,
				"the injected curvePreferences must be decoded into the operator config")

			// recommended serving defaults must not alter injected curve preferences
			defaultedServingInfo := *config.ServingInfo.DeepCopy()
			configdefaults.SetRecommendedHTTPServingInfoDefaults(&defaultedServingInfo)
			assert.Equal(t, tc.curvePreferences, defaultedServingInfo.CurvePreferences)

			secureServing, err := applyServingOptions(t, config.ServingInfo)
			if !assert.NoError(t, err) {
				return
			}
			assert.Equal(t, tc.wantCurves, secureServing.CurvePreferences,
				"serving curve preferences must match the injected groups")

			// existing TLS behavior is preserved in both cases
			assert.Equal(t, uint16(tls.VersionTLS12), secureServing.MinTLSVersion)
			assert.NotEmpty(t, secureServing.CipherSuites,
				"cipher suites must still be applied from the injected config")
		})
	}
}

func TestInvalidCVOInjectedCurvePreferencesFailClosed(t *testing.T) {
	cases := []struct {
		name             string
		curvePreferences []int32
		wantErr          string
	}{
		{
			name:             "out of range curve id",
			curvePreferences: []int32{99999},
			wantErr:          "curve preference 99999 is out of range",
		},
		{
			name:             "curve id unknown to the Go version in use",
			curvePreferences: []int32{22},
			wantErr:          "curve preference 22 is not supported by the current Go version",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := decodeOperatorConfig(t, cvoInjectedOperatorConfig(tc.curvePreferences))
			assert.Equal(t, tc.curvePreferences, config.ServingInfo.CurvePreferences)

			_, err := applyServingOptions(t, config.ServingInfo)
			if err == nil {
				t.Fatal("expected invalid curve preferences to fail closed")
			}
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestServingCurvePreferencesEnforcedDuringHandshake(t *testing.T) {
	certPEM, keyPEM, err := certutil.GenerateSelfSignedCertKey("localhost", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	serverCertificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}

	// newTLSServer starts a server whose TLS configuration mirrors
	// (*genericapiserver.SecureServingInfo).tlsConfig(), which copies the
	// applied curve preferences into the tls.Config used for serving.
	newTLSServer := func(t *testing.T, curvePreferences []tls.CurveID) *httptest.Server {
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		server.TLS = &tls.Config{
			MinVersion:       tls.VersionTLS12,
			NextProtos:       []string{"h2", "http/1.1"},
			CurvePreferences: curvePreferences,
			Certificates:     []tls.Certificate{serverCertificate},
		}
		server.StartTLS()
		t.Cleanup(server.Close)
		return server
	}

	newClient := func(curvePreferences ...tls.CurveID) *http.Client {
		return &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: true, // the test server uses a self-signed certificate
					MinVersion:         tls.VersionTLS12,
					CurvePreferences:   curvePreferences,
				},
			},
		}
	}

	t.Run("configured groups are enforced on the serving endpoint", func(t *testing.T) {
		// a configuration restricted to X25519, as injected for a
		// TLSSecurityProfile carrying only the X25519 group
		x25519Only := newTLSServer(t, appliedCurvePreferences(t, []int32{int32(tls.X25519)}))

		// a client that only offers P-256 finds no mutually supported group
		// and must not be able to complete the handshake
		_, err := newClient(tls.CurveP256).Get(x25519Only.URL)
		if err == nil {
			t.Fatal("handshake must fail when the client offers no configured group")
		}

		// a client offering the configured group connects successfully
		resp, err := newClient(tls.X25519).Get(x25519Only.URL)
		if err != nil {
			t.Fatalf("handshake must succeed when the client offers a configured group: %v", err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unset groups keep Go's default curve selection", func(t *testing.T) {
		// no curve preferences applied, as when the gate is disabled or the
		// profile carries no groups
		defaults := newTLSServer(t, appliedCurvePreferences(t, nil))

		// a P-256 only client still connects, preserving existing behavior
		resp, err := newClient(tls.CurveP256).Get(defaults.URL)
		if err != nil {
			t.Fatalf("handshake must succeed with default curve selection: %v", err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestOperatorReceivesCentralizedTLSConfiguration(t *testing.T) {
	configMapBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "manifests", "0000_10_config-operator_06_configmap.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	configMap := &corev1.ConfigMap{}
	if err := yaml.Unmarshal(configMapBytes, configMap); err != nil {
		t.Fatal(err)
	}

	// The CVO only injects servingInfo (minTLSVersion, cipherSuites and, with
	// TLSGroupPreferences enabled, curvePreferences derived from the cluster
	// wide TLSSecurityProfile.groups) into ConfigMaps annotated with
	// config.openshift.io/inject-tls.
	assert.Equal(t, "true", configMap.Annotations["config.openshift.io/inject-tls"],
		"the operator config ConfigMap must opt into centralized TLS injection")

	// the injected entry must decode as the GenericOperatorConfig the operator
	// command consumes
	if _, ok := configMap.Data["config.yaml"]; !ok {
		t.Fatal("the operator config ConfigMap must carry a config.yaml entry")
	}
	_ = decodeOperatorConfig(t, configMap.Data["config.yaml"])

	deploymentBytes, err := os.ReadFile(filepath.Join("..", "..", "..", "manifests", "0000_10_config-operator_07_deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	deployment := &appsv1.Deployment{}
	if err := yaml.Unmarshal(deploymentBytes, deployment); err != nil {
		t.Fatal(err)
	}

	var operatorContainer *corev1.Container
	for i := range deployment.Spec.Template.Spec.Containers {
		if deployment.Spec.Template.Spec.Containers[i].Name == "openshift-config-operator" {
			operatorContainer = &deployment.Spec.Template.Spec.Containers[i]
			break
		}
	}
	if operatorContainer == nil {
		t.Fatal("the deployment must have an openshift-config-operator container")
	}

	// the operator must consume the injected config...
	assert.Contains(t, operatorContainer.Command, "--config=/var/run/configmaps/config/config.yaml",
		"the operator must be started with the injected config")
	// ...and restart when the CVO rewrites it, e.g. when groups change
	assert.Contains(t, operatorContainer.Command, "--terminate-on-files=/var/run/configmaps/config/config.yaml",
		"the operator must terminate when the injected config changes")

	// the config volume must be mounted so the injected file is readable
	foundConfigMount := false
	for _, mount := range operatorContainer.VolumeMounts {
		if mount.Name == "config" && mount.MountPath == "/var/run/configmaps/config" {
			foundConfigMount = true
			break
		}
	}
	assert.True(t, foundConfigMount, "the injected config must be mounted into the operator container")
}
