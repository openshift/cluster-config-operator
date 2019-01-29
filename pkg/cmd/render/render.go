package render

import (
	"io/ioutil"

	"github.com/golang/glog"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	configv1 "github.com/openshift/api/config/v1"
	genericrender "github.com/openshift/library-go/pkg/operator/render"
	genericrenderoptions "github.com/openshift/library-go/pkg/operator/render/options"
)

// renderOpts holds values to drive the render command.
type renderOpts struct {
	manifest genericrenderoptions.ManifestOptions
	generic  genericrenderoptions.GenericOptions

	clusterConfigFile string
}

// NewRenderCommand creates a render command.
func NewRenderCommand() *cobra.Command {
	renderOpts := renderOpts{
		generic:  *genericrenderoptions.NewGenericOptions(),
		manifest: *genericrenderoptions.NewManifestOptions("config", "openshift/origin-cluster-config-operator:latest"),
	}
	cmd := &cobra.Command{
		Use:   "render",
		Short: "Render kubernetes API server bootstrap manifests, secrets and configMaps",
		Run: func(cmd *cobra.Command, args []string) {
			if err := renderOpts.Validate(); err != nil {
				glog.Fatal(err)
			}
			if err := renderOpts.Complete(); err != nil {
				glog.Fatal(err)
			}
			if err := renderOpts.Run(); err != nil {
				glog.Fatal(err)
			}
		},
	}

	renderOpts.AddFlags(cmd.Flags())

	return cmd
}

func (r *renderOpts) AddFlags(fs *pflag.FlagSet) {
	r.manifest.AddFlags(fs, "config")
	r.generic.AddFlags(fs, configv1.GroupVersion.WithKind("Config"))

	fs.StringVar(&r.clusterConfigFile, "cluster-config-file", r.clusterConfigFile, "Openshift Cluster API Config file.")
}

// Validate verifies the inputs.
func (r *renderOpts) Validate() error {
	if err := r.manifest.Validate(); err != nil {
		return err
	}
	if err := r.generic.Validate(); err != nil {
		return err
	}

	return nil
}

// Complete fills in missing values before command execution.
func (r *renderOpts) Complete() error {
	if err := r.manifest.Complete(); err != nil {
		return err
	}
	if err := r.generic.Complete(); err != nil {
		return err
	}
	return nil
}

type TemplateData struct {
	genericrenderoptions.ManifestConfig
	genericrenderoptions.FileConfig
}

// Run contains the logic of the render command.
func (r *renderOpts) Run() error {
	renderConfig := TemplateData{}
	if len(r.clusterConfigFile) > 0 {
		_, err := ioutil.ReadFile(r.clusterConfigFile)
		if err != nil {
			return err
		}
		// TODO I'm thinking we parse this into a map and reference it that way
	}
	if err := r.manifest.ApplyTo(&renderConfig.ManifestConfig); err != nil {
		return err
	}
	if err := r.generic.ApplyTo(
		&renderConfig.FileConfig,

		// TODO I don't think we have these
		genericrenderoptions.Template{},
		genericrenderoptions.Template{},
		genericrenderoptions.Template{},

		&renderConfig,
	); err != nil {
		return err
	}

	return genericrender.WriteFiles(&r.generic, &renderConfig.FileConfig, renderConfig)
}
