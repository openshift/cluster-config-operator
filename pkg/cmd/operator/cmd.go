package operator

import (
	"github.com/spf13/cobra"

	"github.com/openshift/library-go/pkg/controller/controllercmd"

	"github.com/openshift/cluster-config-operator/pkg/operator"
	"github.com/openshift/cluster-config-operator/pkg/version"
)

func NewOperator() *cobra.Command {
	o := operator.NewOperatorOptions()

	cmd := controllercmd.
		NewControllerCommandConfig("config-operator", version.Get(), o.RunOperator).
		NewCommand()
	cmd.Use = "operator"
	cmd.Short = "Start the Cluster Config Operator"

	o.AddFlags(cmd.Flags())

	return cmd
}
