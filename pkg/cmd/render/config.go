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

func ReadFeatureGateV1(objBytes []byte) (*configv1.FeatureGate, error) {
	requiredObj, err := runtime.Decode(configCodecs.UniversalDecoder(configv1.SchemeGroupVersion), objBytes)
	if err != nil {
		return nil, err
	}

	return requiredObj.(*configv1.FeatureGate), nil
}

func WriteFeatureGateV1OrDie(obj *configv1.FeatureGate) string {
	return runtime.EncodeOrDie(configCodecs.LegacyCodec(configv1.SchemeGroupVersion), obj)
}
