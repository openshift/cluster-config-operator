package topology_transition_controller

import (
	"context"

	configlistersv1 "github.com/openshift/client-go/config/listers/config/v1"
)

const selfClusterOperatorName = "config-operator"

// reconcileClusterOperatorsStable returns a function that checks whether all
// ClusterOperators (except config-operator itself) have reached a stable state
// (Available=True, Progressing=False, Degraded=False). This wraps the shared
// checkClusterOperatorsStable core for the reconciliation check interface.
func reconcileClusterOperatorsStable(coLister configlistersv1.ClusterOperatorLister) func(context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		unstable, err := checkClusterOperatorsStable(coLister)
		if err != nil {
			return false, err
		}
		return len(unstable) == 0, nil
	}
}
