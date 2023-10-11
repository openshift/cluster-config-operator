package render

import (
	configv1 "github.com/openshift/api/config/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

var (
	configScheme = runtime.NewScheme()
	configCodecs = serializer.NewCodecFactory(configScheme)
)

func init() {
	utilruntime.Must(configv1.AddToScheme(configScheme))
}

func WriteFeatureGateV1OrDie(obj *configv1.FeatureGate) string {
	return runtime.EncodeOrDie(configCodecs.LegacyCodec(configv1.SchemeGroupVersion), obj)
}
