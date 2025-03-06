package operator

import (
	"github.com/spf13/cobra"

	"github.com/openshift/library-go/pkg/controller/controllercmd"

	"github.com/openshift/cluster-config-operator/pkg/operator"
	"github.com/openshift/cluster-config-operator/pkg/version"

	"k8s.io/utils/clock"
)

func NewOperator() *cobra.Command {
	o := operator.NewOperatorOptions()

	cmd := controllercmd.
		NewControllerCommandConfig("config-operator", version.Get(), o.RunOperator, clock.RealClock{}).
		NewCommand()
	cmd.Use = "operator"
	cmd.Short = "Start the Cluster Config Operator"

	o.AddFlags(cmd.Flags())

	return cmd
}
