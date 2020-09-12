package operatorloglevel

import (
	"context"
	"fmt"
	"strings"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/loglevel"
	operatorv1helpers "github.com/openshift/library-go/pkg/operator/v1helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog"
)

type operatorLogLevelNormalizer struct {
	dynamicClient dynamic.Interface
}

// NewLogLevelNormalizer normalizes the log level in operator custom resource
// if it's value differs from supported values
func NewLogLevelNormalizer(dynamicClient dynamic.Interface,
	operatorClient operatorv1helpers.OperatorClient,
	recorder events.Recorder) factory.Controller {
	c := operatorLogLevelNormalizer{
		dynamicClient: dynamicClient,
	}

	return factory.New().
		WithSync(c.sync).
		WithSyncDegradedOnError(operatorClient).
		ResyncEvery(5*time.Minute).
		ToController("OperatorLogLevelNormalizer", recorder)
}

// sync runs periodically and makes sure that the log level field values in operator
// spec does not differ from supported values.
func (c *operatorLogLevelNormalizer) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	gvrs := []schema.GroupVersionResource{
		{
			Group:    "imageregistry.operator.openshift.io",
			Version:  "v1",
			Resource: "configs",
		},
		{
			Group:    "operator.openshift.io",
			Version:  "v1",
			Resource: "configs",
		},
		{
			Group:    "operator.openshift.io",
			Version:  "v1",
			Resource: "etcds",
		},
		{
			Group:    "operator.openshift.io",
			Version:  "v1",
			Resource: "kubeapiservers",
		},
		{
			Group:    "operator.openshift.io",
			Version:  "v1",
			Resource: "kubecontrollermanagers",
		},
		{
			Group:    "operator.openshift.io",
			Version:  "v1",
			Resource: "kubeschedulers",
		},
		{
			Group:    "operator.openshift.io",
			Version:  "v1",
			Resource: "openshiftapiservers",
		},
		{
			Group:    "operator.openshift.io",
			Version:  "v1",
			Resource: "cloudcredentials",
		},
		{
			Group:    "operator.openshift.io",
			Version:  "v1",
			Resource: "kubestorageversionmigrators",
		},
		{
			Group:    "operator.openshift.io",
			Version:  "v1",
			Resource: "authentications",
		},
		{
			Group:    "operator.openshift.io",
			Version:  "v1",
			Resource: "openshiftcontrollermanagers",
		},
		{
			Group:    "operator.openshift.io",
			Version:  "v1",
			Resource: "storages",
		},
		{
			Group:    "operator.openshift.io",
			Version:  "v1",
			Resource: "networks",
		},
		{
			Group:    "operator.openshift.io",
			Version:  "v1",
			Resource: "consoles",
		},
		{
			Group:    "operator.openshift.io",
			Version:  "v1",
			Resource: "csisnapshotcontrollers",
		},
		{
			Group:    "operator.openshift.io",
			Version:  "v1",
			Resource: "clustercsidrivers",
		},
	}

	for _, gvr := range gvrs {
		customResources, err := c.dynamicClient.Resource(gvr).List(ctx, metav1.ListOptions{})
		if err != nil {
			klog.V(4).Infof("error trying to list custom resources for %s: %v", gvr.Resource, err)
			continue
		}

		for _, cr := range customResources.Items {
			crCopy := cr.DeepCopy()
			eventMsgs, needsUpdate := normalizeLogLevelField(crCopy, gvr.Resource)
			if needsUpdate {
				if _, err := c.dynamicClient.Resource(gvr).Update(ctx, crCopy, metav1.UpdateOptions{}); err != nil {
					klog.Warningf("failed to normalize log level to %v for operator %s: %v", operatorv1.Normal, gvr.Resource, err)
					continue
				}
				for _, event := range eventMsgs {
					syncCtx.Recorder().Event("OperatorLogLevelChange", event)
				}
			}
		}
	}

	return nil
}

func normalizeLogLevelField(cr *unstructured.Unstructured,
	resourceName string) ([]string, bool) {
	needsUpdate := false
	eventMsgs := []string{}

	for _, logLevelFieldPath := range [][]string{{"spec", "operatorLogLevel"}, {"spec", "logLevel"}} {
		// custom resources that do not have log level field are ignored.
		currentLogLevel, ok, err := unstructured.NestedString(cr.UnstructuredContent(), logLevelFieldPath...)
		if err != nil {
			klog.V(4).Infof("failed to find %q in custom resource %s: %v", strings.Join(logLevelFieldPath, "."), resourceName, err)
			continue
		}
		if !ok {
			continue
		}

		if loglevel.ValidLogLevel(operatorv1.LogLevel(currentLogLevel)) {
			continue
		}

		if err := unstructured.SetNestedField(cr.UnstructuredContent(), string(operatorv1.Normal), logLevelFieldPath...); err != nil {
			klog.Warningf("failed to set log level to %s in resource %s", operatorv1.Normal, resourceName)
			continue
		}

		eventMsgs = append(eventMsgs, fmt.Sprintf("%q changed from %q to %q", strings.Join(logLevelFieldPath, "."), currentLogLevel, operatorv1.Normal))
		needsUpdate = true
	}

	return eventMsgs, needsUpdate
}
