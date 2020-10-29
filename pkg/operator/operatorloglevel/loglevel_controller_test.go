package operatorloglevel

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func Test_normalizeLogLevelField(t *testing.T) {
	type args struct {
		cr           *unstructured.Unstructured
		resourceName string
	}
	tests := []struct {
		name           string
		args           args
		expectedEvents []string
		expectUpdate   bool
	}{
		{
			name: "log levels not set",
			args: args{
				cr: &unstructured.Unstructured{},
			},
			expectedEvents: []string{},
		},
		{
			name: "invalid operator log level",
			args: args{
				cr: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"spec": map[string]interface{}{
							"operatorLogLevel": "test",
						},
					},
				},
				resourceName: "dummy",
			},
			expectedEvents: []string{"\"spec.operatorLogLevel\" changed from \"test\" to \"Normal\""},
			expectUpdate:   true,
		},
		{
			name: "invalid operand log level",
			args: args{
				cr: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"spec": map[string]interface{}{
							"logLevel": "test",
						},
					},
				},
				resourceName: "dummy",
			},
			expectedEvents: []string{"\"spec.logLevel\" changed from \"test\" to \"Normal\""},
			expectUpdate:   true,
		},
		{
			name: "invalid operator and operand log level",
			args: args{
				cr: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"spec": map[string]interface{}{
							"logLevel":         "test",
							"operatorLogLevel": "test",
						},
					},
				},
				resourceName: "dummy",
			},
			expectedEvents: []string{"\"spec.operatorLogLevel\" changed from \"test\" to \"Normal\"",
				"\"spec.logLevel\" changed from \"test\" to \"Normal\""},
			expectUpdate: true,
		},
		{
			name: "valid operator and operand log level",
			args: args{
				cr: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"spec": map[string]interface{}{
							"logLevel":         "Trace",
							"operatorLogLevel": "TraceAll",
						},
					},
				},
				resourceName: "dummy",
			},
			expectedEvents: []string{},
		},
		{
			name: "valid operator log level",
			args: args{
				cr: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"spec": map[string]interface{}{
							"operatorLogLevel": "Normal",
						},
					},
				},
				resourceName: "dummy",
			},
			expectedEvents: []string{},
		},
		{
			name: "valid operand log level",
			args: args{
				cr: &unstructured.Unstructured{
					Object: map[string]interface{}{
						"spec": map[string]interface{}{
							"logLevel": "Debug",
						},
					},
				},
				resourceName: "dummy",
			},
			expectedEvents: []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events, needsUpdate := normalizeLogLevelField(tt.args.cr, tt.args.resourceName)
			if !reflect.DeepEqual(events, tt.expectedEvents) {
				t.Errorf("normalizeLogLevelField() events = %v, want %v", events, tt.expectedEvents)
			}
			if needsUpdate != tt.expectUpdate {
				t.Errorf("normalizeLogLevelField() needsUpdate = %v, want %v", needsUpdate, tt.expectUpdate)
			}
		})
	}
}
