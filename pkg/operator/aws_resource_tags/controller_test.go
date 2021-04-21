package aws_resource_tags

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ktesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	configv1 "github.com/openshift/api/config/v1"
	configfakeclient "github.com/openshift/client-go/config/clientset/versioned/fake"
	configv1listers "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
)

func Test_sync(t *testing.T) {
	cases := []struct {
		name            string
		obj             *configv1.Infrastructure
		expectedActions int
		expectedTags    []configv1.AWSResourceTag
		expectedErr     string
	}{{
		name: "empty infrastructure",
		obj:  buildInfra(),
	}, {
		name: "other platform",
		obj: buildInfra(withGCPSpec(), withGCPStatus()),
	}, {
		name: "no spec tags, no platform status",
		obj: buildInfra(withAWSSpec()),
	}, {
		name: "no spec tags, no aws status",
		obj: buildInfra(withAWSSpec(), withPlatformStatus()),
	}, {
		name: "no spec tags, no status tags",
		obj: buildInfra(withAWSSpec(), withAWSStatus()),
	}, {
		name: "no spec tags, non-empty status tags",
		obj: buildInfra(withAWSSpec(), withStatusTag("test-key", "test-value")),
		expectedActions: 1,
		expectedTags: []configv1.AWSResourceTag{},
	}, {
		name: "in-sync resource tag",
		obj: buildInfra(
			withSpecTag("test-key", "test-value"),
			withStatusTag("test-key", "test-value"),
		),
		expectedTags: []configv1.AWSResourceTag{{Key: "test-key", Value: "test-value"}},
	}, {
		name: "changed resource tag",
		obj: buildInfra(
			withSpecTag("test-key", "new-value"),
			withStatusTag("test-key", "orig-value"),
		),
		expectedActions: 1,
		expectedTags: []configv1.AWSResourceTag{{Key: "test-key", Value: "new-value"}},
	}, {
		name: "added resource tag",
		obj: buildInfra(
			withSpecTag("test-key", "test-value"),
			withSpecTag("other-key", "other-value"),
			withStatusTag("test-key", "test-value"),
		),
		expectedActions: 1,
		expectedTags: []configv1.AWSResourceTag{
			{Key: "test-key", Value: "test-value"},
			{Key: "other-key", Value: "other-value"},
		},
	}, {
		name: "removed resource tag",
		obj: buildInfra(
			withSpecTag("test-key", "test-value"),
			withStatusTag("test-key", "test-value"),
			withStatusTag("other-key", "other-value"),
		),
		expectedActions: 1,
		expectedTags: []configv1.AWSResourceTag{{Key: "test-key", Value: "test-value"}},
	}, {
		name: "tag with invalid characters in key rejected",
		obj: buildInfra(
			withSpecTag("test-key", "test-value"),
			withSpecTag("bad-key***", "other-value"),
		),
		expectedActions: 1,
		expectedTags: []configv1.AWSResourceTag{{Key: "test-key", Value: "test-value"}},
	}, {
		name: "tag with invalid characters in value rejected",
		obj: buildInfra(
			withSpecTag("test-key", "test-value"),
			withSpecTag("other-key", "bad-value***"),
		),
		expectedActions: 1,
		expectedTags: []configv1.AWSResourceTag{{Key: "test-key", Value: "test-value"}},
	}, {
		name: "tag with missing key rejected",
		obj: buildInfra(
			withSpecTag("test-key", "test-value"),
			withSpecTag("", "other-value"),
		),
		expectedActions: 1,
		expectedTags: []configv1.AWSResourceTag{{Key: "test-key", Value: "test-value"}},
	}, {
		name: "tag with missing value rejected",
		obj: buildInfra(
			withSpecTag("test-key", "test-value"),
			withSpecTag("other-key", ""),
		),
		expectedActions: 1,
		expectedTags: []configv1.AWSResourceTag{{Key: "test-key", Value: "test-value"}},
	}, {
		name: "tag with too-long key rejected",
		obj: buildInfra(
			withSpecTag("test-key", "test-value"),
			withSpecTag(strings.Repeat("k", 129), "other-value"),
		),
		expectedActions: 1,
		expectedTags: []configv1.AWSResourceTag{{Key: "test-key", Value: "test-value"}},
	}, {
		name: "tag with too-long value rejected",
		obj: buildInfra(
			withSpecTag("test-key", "test-value"),
			withSpecTag("other-key", strings.Repeat("v", 257)),
		),
		expectedActions: 1,
		expectedTags: []configv1.AWSResourceTag{{Key: "test-key", Value: "test-value"}},
	}, {
		name: "tag with key in kubernetes.io namespace rejected",
		obj: buildInfra(
			withSpecTag("test-key", "test-value"),
			withSpecTag("kubernetes.io/cluster/some-cluster", "owned"),
		),
		expectedActions: 1,
		expectedTags: []configv1.AWSResourceTag{{Key: "test-key", Value: "test-value"}},
	}, {
		name: "tag with key in openshift.io namespace rejected",
		obj: buildInfra(
			withSpecTag("test-key", "test-value"),
			withSpecTag("openshift.io/some-key", "some-value"),
		),
		expectedActions: 1,
		expectedTags: []configv1.AWSResourceTag{{Key: "test-key", Value: "test-value"}},
	}, {
		name: "tag with key in openshift.io subdomain namespace rejected",
		obj: buildInfra(
			withSpecTag("test-key", "test-value"),
			withSpecTag("other.openshift.io/some-key", "some-value"),
		),
		expectedActions: 1,
		expectedTags: []configv1.AWSResourceTag{{Key: "test-key", Value: "test-value"}},
	}, {
		name: "tag with key in namespace similar to openshift.io accepted",
		obj: buildInfra(
			withSpecTag("test-key", "test-value"),
			withSpecTag("otheropenshift.io/some-key", "some-value"),
		),
		expectedActions: 1,
		expectedTags: []configv1.AWSResourceTag{
			{Key: "test-key", Value: "test-value"},
			{Key: "otheropenshift.io/some-key", Value: "some-value"},
		},
	}, {
		name: "tag with duplicate key rejected",
		obj: buildInfra(
			withSpecTag("test-key", "test-value"),
			withSpecTag("test-key", "other-value"),
		),
		expectedActions: 1,
		expectedTags: []configv1.AWSResourceTag{{Key: "test-key", Value: "test-value"}},
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			if err := indexer.Add(tc.obj); err != nil {
				t.Fatal(err.Error())
			}
			fake := configfakeclient.NewSimpleClientset(tc.obj)
			ctrl := AWSResourceTagsController{
				infraClient: fake.ConfigV1().Infrastructures(),
				infraLister: configv1listers.NewInfrastructureLister(indexer),
			}

			err := ctrl.sync(context.TODO(),
				factory.NewSyncContext("AWSResourceTagsController", events.NewInMemoryRecorder("AWSResourceTagsController")))
			if tc.expectedErr == "" {
				assert.NoError(t, err)
			} else {
				if assert.Error(t, err) {
					assert.Regexp(t, tc.expectedErr, err.Error())
				}
			}
			assert.Equal(t, tc.expectedActions, len(fake.Actions()))

			var tags []configv1.AWSResourceTag
			if tc.obj.Status.PlatformStatus != nil && tc.obj.Status.PlatformStatus.AWS != nil {
				tags = tc.obj.Status.PlatformStatus.AWS.ResourceTags
			}
			for _, a := range fake.Actions() {
				obj := a.(ktesting.UpdateAction).GetObject().(*configv1.Infrastructure)
				if obj.Status.PlatformStatus != nil && obj.Status.PlatformStatus.AWS != nil {
					tags = obj.Status.PlatformStatus.AWS.ResourceTags
				}
			}
			assert.EqualValues(t, tc.expectedTags, tags)
		})
	}
}

type infraOption func(*configv1.Infrastructure)

func buildInfra(opts ...infraOption) *configv1.Infrastructure {
	infra := &configv1.Infrastructure{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cluster",
		},
	}
	for _, o := range opts {
		o(infra)
	}
	return infra
}

func withSpecTag(key, value string) infraOption {
	return func(infra *configv1.Infrastructure) {
		withAWSSpec()(infra)
		infra.Spec.PlatformSpec.AWS.ResourceTags = append(
			infra.Spec.PlatformSpec.AWS.ResourceTags,
			configv1.AWSResourceTag{Key: key, Value: value},
		)
	}
}

func withStatusTag(key, value string) infraOption {
	return func(infra *configv1.Infrastructure) {
		withAWSStatus()(infra)
		infra.Status.PlatformStatus.AWS.ResourceTags = append(
			infra.Status.PlatformStatus.AWS.ResourceTags,
			configv1.AWSResourceTag{Key: key, Value: value},
		)
	}
}

func withAWSSpec() infraOption {
	return func(infra *configv1.Infrastructure) {
		if infra.Spec.PlatformSpec.AWS == nil {
			infra.Spec.PlatformSpec.AWS = &configv1.AWSPlatformSpec{}
		}
	}
}

func withGCPSpec() infraOption {
	return func(infra *configv1.Infrastructure) {
		if infra.Spec.PlatformSpec.GCP == nil {
			infra.Spec.PlatformSpec.GCP = &configv1.GCPPlatformSpec{}
		}
	}
}

func withPlatformStatus() infraOption {
	return func(infra *configv1.Infrastructure) {
		if infra.Status.PlatformStatus == nil {
			infra.Status.PlatformStatus = &configv1.PlatformStatus{}
		}
	}
}

func withAWSStatus() infraOption {
	return func(infra *configv1.Infrastructure) {
		withPlatformStatus()(infra)
		if infra.Status.PlatformStatus.AWS == nil {
			infra.Status.PlatformStatus.AWS = &configv1.AWSPlatformStatus{}
		}
	}
}

func withGCPStatus() infraOption {
	return func(infra *configv1.Infrastructure) {
		withPlatformStatus()(infra)
		if infra.Status.PlatformStatus.GCP == nil {
			infra.Status.PlatformStatus.GCP = &configv1.GCPPlatformStatus{}
		}
	}
}