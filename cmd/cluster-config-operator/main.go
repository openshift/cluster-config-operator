package main

import (
	"os"

	"github.com/spf13/cobra"

	"k8s.io/component-base/cli"

	"github.com/openshift/cluster-config-operator/pkg/cmd/operator"
	"github.com/openshift/cluster-config-operator/pkg/cmd/render"
	"github.com/openshift/cluster-config-operator/pkg/version"
)

func main() {
	command := NewOperatorCommand()
	code := cli.Run(command)
	os.Exit(code)
}

func NewOperatorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster-config-operator",
		Short: "OpenShift cluster config operator",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
			os.Exit(1)
		},
	}

	if v := version.Get().String(); len(v) == 0 {
		cmd.Version = "<unknown>"
	} else {
		cmd.Version = v
	}

	cmd.AddCommand(render.NewRenderCommand())
	cmd.AddCommand(operator.NewOperator())

	return cmd
}
