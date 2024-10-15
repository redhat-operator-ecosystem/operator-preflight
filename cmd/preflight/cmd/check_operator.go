package cmd

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/redhat-openshift-ecosystem/openshift-preflight/artifacts"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/cli"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/formatters"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/lib"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/runtime"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/operator"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/version"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func checkOperatorCmd(runpreflight runPreflight, viper *viper.Viper) *cobra.Command {
	checkOperatorCmd := &cobra.Command{
		Use:   "operator",
		Short: "Run checks for an Operator",
		Long:  `This command will run the Certification checks for an Operator bundle image. `,
		Args: func(cmd *cobra.Command, args []string) error {
			return checkOperatorPositionalArgs(args, viper)
		},
		// this fmt.Sprintf is in place to keep spacing consistent with cobras two spaces that's used in: Usage, Flags, etc
		Example: fmt.Sprintf("  %s", "preflight check operator quay.io/repo-name/operator-bundle:version"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return checkOperatorRunE(cmd, args, runpreflight, viper)
		},
	}

	checkOperatorCmd.Flags().String("namespace", "", "The namespace to use when running OperatorSDK Scorecard. (env: PFLT_NAMESPACE)")
	_ = viper.BindPFlag("namespace", checkOperatorCmd.Flags().Lookup("namespace"))

	checkOperatorCmd.Flags().String("serviceaccount", "", "The service account to use when running OperatorSDK Scorecard. (env: PFLT_SERVICEACCOUNT)")
	_ = viper.BindPFlag("serviceaccount", checkOperatorCmd.Flags().Lookup("serviceaccount"))

	checkOperatorCmd.Flags().String("scorecard-image", "", "A uri that points to the scorecard image digest, used in disconnected environments.\n"+
		"It should only be used in a disconnected environment. Use preflight runtime-assets on a connected \n"+
		"workstation to generate the digest that needs to be mirrored. (env: PFLT_SCORECARD_IMAGE)")
	_ = viper.BindPFlag("scorecard_image", checkOperatorCmd.Flags().Lookup("scorecard-image"))

	checkOperatorCmd.Flags().String("scorecard-wait-time", "", "A time value that will be passed to scorecard's --wait-time environment variable.\n"+
		"(env: PFLT_SCORECARD_WAIT_TIME)")
	_ = viper.BindPFlag("scorecard_wait_time", checkOperatorCmd.Flags().Lookup("scorecard-wait-time"))

	checkOperatorCmd.Flags().String("channel", "", "The name of the operator channel which is used by DeployableByOLM to deploy the operator.\n"+
		"If empty, the default operator channel in bundle's annotations file is used.. (env: PFLT_CHANNEL)")
	_ = viper.BindPFlag("channel", checkOperatorCmd.Flags().Lookup("channel"))

	return checkOperatorCmd
}

// ensureKubeconfigIsSet ensures that the KUBECONFIG environment variable has a value.
func ensureKubeconfigIsSet() error {
	if _, ok := os.LookupEnv("KUBECONFIG"); !ok {
		return fmt.Errorf("environment variable KUBECONFIG could not be found")
	}

	return nil
}

// ensureIndexImageConfigIsSet ensures that the PFLT_INDEXIMAGE environment variable has
// a value.
func ensureIndexImageConfigIsSet(viper *viper.Viper) error {
	if catalogImage := viper.GetString("indexImage"); len(catalogImage) == 0 {
		return fmt.Errorf("environment variable PFLT_INDEXIMAGE could not be found")
	}

	return nil
}

// checkOperatorRunE executes checkOperator using the user args to inform the execution.
func checkOperatorRunE(cmd *cobra.Command, args []string, runpreflight runPreflight, viper *viper.Viper) error {
	ctx := cmd.Context()
	logger, err := logr.FromContext(ctx)
	if err != nil {
		return fmt.Errorf("invalid logging configuration")
	}

	logger.Info("certification library version", "version", version.Version.String())
	operatorImage := args[0]

	// Render the Viper configuration as a runtime.Config
	cfg, err := runtime.NewConfigFrom(*viper)
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	ctx, _, err = configureArtifactsWriter(ctx, cfg.Artifacts)
	if err != nil {
		return err
	}

	formatter, err := formatters.NewByName(formatters.DefaultFormat)
	if err != nil {
		return err
	}

	opts := generateOperatorCheckOptions(cfg)

	kubeconfig, err := func() ([]byte, error) {
		kubeconfigFile, err := os.Open(cfg.Kubeconfig)
		if err != nil {
			return nil, fmt.Errorf("unable to open provided kubeconfig file: %s", err)
		}
		defer kubeconfigFile.Close()
		return io.ReadAll(kubeconfigFile)
	}()
	if err != nil {
		return fmt.Errorf("unable to read provided kubeconfig file's contents: %s", err)
	}

	checkoperator := operator.NewCheck(operatorImage, cfg.IndexImage, kubeconfig, opts...)

	cmd.SilenceUsage = true
	return runpreflight(
		ctx,
		checkoperator.Run,
		cli.CheckConfig{
			IncludeJUnitResults: cfg.WriteJUnit,
			SubmitResults:       false, // operator results are not submitted.
		},
		formatter,
		&runtime.ResultWriterFile{},
		&lib.NoopSubmitter{},
		viper.GetString("pyxis_env"),
	)
}

func checkOperatorPositionalArgs(args []string, viper *viper.Viper) error {
	if len(args) != 1 {
		return fmt.Errorf("an operator bundle image positional argument is required")
	}

	if err := ensureKubeconfigIsSet(); err != nil {
		return err
	}

	if err := ensureIndexImageConfigIsSet(viper); err != nil {
		return err
	}

	return nil
}

// generateOperatorCheckOptions returns options to be used with OperatorCheck based on cfg.
func generateOperatorCheckOptions(cfg *runtime.Config) []operator.Option {
	opts := []operator.Option{
		operator.WithDockerConfigJSONFromFile(cfg.DockerConfig),
		// empty value is handled downstream for below options, so we always add them here.
		operator.WithScorecardImage(cfg.ScorecardImage),
		operator.WithScorecardServiceAccount(cfg.ServiceAccount),
		operator.WithScorecardNamespace(cfg.Namespace),
	}

	if cfg.ScorecardWaitTime != "" {
		opts = append(opts, operator.WithScorecardWaitTime(cfg.ScorecardWaitTime))
	}

	if cfg.Channel != "" {
		opts = append(opts, operator.WithOperatorChannel(cfg.Channel))
	}

	if cfg.Insecure {
		opts = append(opts, operator.WithInsecureConnection())
	}

	return opts
}

// configureArtifactsWriter adds a filesystem ArtifactsWriter to the context.
func configureArtifactsWriter(ctx context.Context, dir string) (context.Context, *artifacts.FilesystemWriter, error) {
	artifactsWriter, err := artifacts.NewFilesystemWriter(artifacts.WithDirectory(dir))
	if err != nil {
		return ctx, &artifacts.FilesystemWriter{}, err
	}

	return artifacts.ContextWithWriter(ctx, artifactsWriter), artifactsWriter, nil
}
