// Package cmd implements the command-line interface for Preflight.
package cmd

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/redhat-openshift-ecosystem/openshift-preflight/artifacts"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/version"

	"github.com/bombsimon/logrusr/v4"
	"github.com/go-logr/logr"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	ctrl "sigs.k8s.io/controller-runtime"
)

var configFileUsed bool

func rootCmd(viper *viper.Viper) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     "preflight",
		Short:   "Preflight Red Hat certification prep tool.",
		Long:    "A utility that allows you to pre-test your bundles, operators, and container before submitting for Red Hat Certification.",
		Version: version.Version.String(),
		Args:    cobra.MinimumNArgs(1),
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			preRunConfig(cmd, viper)
		},
	}

	rootCmd.PersistentFlags().String("config", "", "A preflight config file. The default is config.yaml (env: PFLT_CONFIG)")
	_ = viper.BindPFlag("config", rootCmd.PersistentFlags().Lookup("config"))

	rootCmd.PersistentFlags().String("logfile", "", "Where the execution logfile will be written. (env: PFLT_LOGFILE)")
	_ = viper.BindPFlag("logfile", rootCmd.PersistentFlags().Lookup("logfile"))

	rootCmd.PersistentFlags().String("loglevel", "", "The verbosity of the preflight tool itself. Ex. warn, debug, trace, info, error. (env: PFLT_LOGLEVEL)")
	_ = viper.BindPFlag("loglevel", rootCmd.PersistentFlags().Lookup("loglevel"))

	rootCmd.AddCommand(checkCmd(viper))
	rootCmd.AddCommand(listChecksCmd())
	rootCmd.AddCommand(runtimeAssetsCmd())
	rootCmd.AddCommand(supportCmd())

	return rootCmd
}

func Execute(ctx context.Context, viper *viper.Viper) error {
	initConfig(viper)
	return rootCmd(viper).ExecuteContext(ctx)
}

func initConfig(v *viper.Viper) {
	// set up ENV var support
	v.SetEnvPrefix("pflt")
	v.AutomaticEnv()

	// set up optional config file support
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")

	configFileUsed = true
	if v.GetString("config") != "" {
		v.SetConfigFile(v.GetString("config"))
	}

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			configFileUsed = false
		}
	}

	// Set up logging config defaults
	v.SetDefault("logfile", DefaultLogFile)
	v.SetDefault("loglevel", DefaultLogLevel)
	v.SetDefault("artifacts", artifacts.DefaultArtifactsDir)

	// Set up cluster defaults
	v.SetDefault("namespace", DefaultNamespace)
	v.SetDefault("serviceaccount", DefaultServiceAccount)

	// Set up scorecard wait time default
	v.SetDefault("scorecard_wait_time", DefaultScorecardWaitTime)
}

// preRunConfig is used by cobra.PreRun in all non-root commands to load all necessary configurations
func preRunConfig(cmd *cobra.Command, viper *viper.Viper) {
	l := logrus.New()
	l.SetFormatter(&logrus.TextFormatter{DisableColors: true})

	// set up logging
	logname := viper.GetString("logfile")
	logFile, err := os.OpenFile(logname, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err == nil {
		mw := io.MultiWriter(os.Stderr, logFile)
		l.SetOutput(mw)
	} else {
		l.Infof("Failed to log to file, using default stderr")
	}
	if ll, err := logrus.ParseLevel(viper.GetString("loglevel")); err == nil {
		l.SetLevel(ll)
	}

	// if we are in the offline flow redirect log file to exist in the directory where all other artifact exist
	if viper.GetBool("offline") {
		// Get the base name of the logfile, in case logfile has a path
		baseLogName := filepath.Base(logname)
		artifacts := viper.GetString("artifacts")

		// ignoring error since OpenFile will error and we'll still have the multiwriter from above
		_ = os.Mkdir(artifacts, 0o777)

		artifactsLogFile, err := os.OpenFile(filepath.Join(artifacts, baseLogName), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err == nil {
			mw := io.MultiWriter(os.Stderr, logFile, artifactsLogFile)
			l.SetOutput(mw)
		}

		// setting log level to trace, to provide the most detailed logs possible
		l.SetLevel(logrus.TraceLevel)
	}

	if !configFileUsed {
		l.Debug("config file not found, proceeding without it")
	}

	logger := logrusr.New(l)
	ctx := logr.NewContext(cmd.Context(), logger)

	// Setting the controller-runtime logger to a no-op logger by default,
	// unless debug mode is enabled. This is because the controller-runtime
	// logger is *very* verbose even at info level. This is not really needed,
	// but otherwise we get a warning from the controller-runtime.
	ctrl.SetLogger(logr.Discard())

	cmd.SetContext(ctx)
}
