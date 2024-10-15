package cmd

import (
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/cli"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func checkCmd(viper *viper.Viper) *cobra.Command {
	checkCmd := &cobra.Command{
		Use:   "check",
		Short: "Run checks for an operator or container",
		Long:  "This command will allow you to execute the Red Hat Certification tests for an operator or a container.",
	}

	checkCmd.PersistentFlags().StringP("docker-config", "d", "", "Path to docker config.json file. This value is optional for publicly accessible images.\n"+
		"However, it is strongly encouraged for public Docker Hub images,\n"+
		"due to the rate limit imposed for unauthenticated requests. (env: PFLT_DOCKERCONFIG)")
	_ = viper.BindPFlag("dockerConfig", checkCmd.PersistentFlags().Lookup("docker-config"))

	checkCmd.PersistentFlags().String("artifacts", "", "Where check-specific artifacts will be written. (env: PFLT_ARTIFACTS)")
	_ = viper.BindPFlag("artifacts", checkCmd.PersistentFlags().Lookup("artifacts"))

	checkCmd.AddCommand(checkOperatorCmd(cli.RunPreflight, viper))
	checkCmd.AddCommand(checkContainerCmd(cli.RunPreflight, viper))

	return checkCmd
}
