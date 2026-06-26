package topology_transition_controller

import (
	"context"

	configv1 "github.com/openshift/api/config/v1"
	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const selfClusterOperatorName = "config-operator"

// checkAllClusterOperatorsStable returns a function that checks whether all
// ClusterOperators (except config-operator itself) have reached a stable state
// (Available=True, Progressing=False, Degraded=False). config-operator is
// excluded to avoid a circular dependency where a transient degraded blip
// blocks its own reconciliation check.
func checkAllClusterOperatorsStable(coLister configlistersv1.ClusterOperatorLister) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		operators, err := coLister.List(labels.Everything())
		if err != nil {
			return false, err
		}

		for _, co := range operators {
			if co.Name == selfClusterOperatorName {
				continue
			}
			availableSeen := false
			for _, cond := range co.Status.Conditions {
				switch cond.Type {
				case configv1.OperatorAvailable:
					if cond.Status != configv1.ConditionTrue {
						return false, nil
					}
					availableSeen = true
				case configv1.OperatorProgressing:
					if cond.Status == configv1.ConditionTrue {
						return false, nil
					}
				case configv1.OperatorDegraded:
					if cond.Status == configv1.ConditionTrue {
						return false, nil
					}
				}
			}
			if !availableSeen {
				return false, nil
			}
		}

		return true, nil
	}
}
