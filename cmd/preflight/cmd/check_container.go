package cmd

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	rt "runtime"
	"slices"
	"strings"

	"github.com/redhat-openshift-ecosystem/openshift-preflight/artifacts"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/certification"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/container"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/check"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/cli"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/formatters"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/lib"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/log"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/option"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/runtime"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/version"

	"github.com/go-logr/logr"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

var submit bool

// runPreflight is introduced to make testing of this command possible, it has the same method signature as cli.RunPreflight.
type runPreflight func(context.Context, func(ctx context.Context) (certification.Results, error), cli.CheckConfig, formatters.ResponseFormatter, lib.ResultWriter, lib.ResultSubmitter, string) error

func checkContainerCmd(runpreflight runPreflight, viper *viper.Viper) *cobra.Command {
	checkContainerCmd := &cobra.Command{
		Use:   "container",
		Short: "Run checks for a container",
		Long:  `This command will run the Certification checks for a container image. `,
		Args: func(cmd *cobra.Command, args []string) error {
			return checkContainerPositionalArgs(cmd, args, viper)
		},
		// this fmt.Sprintf is in place to keep spacing consistent with cobras two spaces that's used in: Usage, Flags, etc
		Example: fmt.Sprintf("  %s", "preflight check container quay.io/repo-name/container-name:version"),
		PreRunE: checkContainerPreRunE,
		RunE: func(cmd *cobra.Command, args []string) error {
			return checkContainerRunE(cmd, args, runpreflight, viper)
		},
	}

	flags := checkContainerCmd.Flags()

	flags.BoolVarP(&submit, "submit", "s", false, "submit check container results to Red Hat")
	_ = viper.BindPFlag("submit", flags.Lookup("submit"))

	flags.Bool("insecure", false, "Use insecure protocol for the registry. Default is False. Cannot be used with submit.")
	_ = viper.BindPFlag("insecure", flags.Lookup("insecure"))

	flags.Bool("offline", false, "Intended to be used in disconnected environments and will tar artifacts used for submission,\n"+
		"enabling other Red Hat managed tools/process to submit these artifacts at a later time.\n"+
		"Cannot be used with submit.")
	_ = viper.BindPFlag("offline", flags.Lookup("offline"))

	// Make --submit mutually exclusive to --offline
	checkContainerCmd.MarkFlagsMutuallyExclusive("submit", "offline")

	// Make --submit mutually exclusive to --insecure
	checkContainerCmd.MarkFlagsMutuallyExclusive("submit", "insecure")

	flags.String("pyxis-api-token", "", "API token for Pyxis authentication (env: PFLT_PYXIS_API_TOKEN)")
	_ = viper.BindPFlag("pyxis_api_token", flags.Lookup("pyxis-api-token"))

	flags.String("pyxis-host", "", fmt.Sprintf("Host to use for Pyxis submissions. This will override Pyxis Env. Only set this if you know what you are doing.\n"+
		"If you do set it, it should include just the host, and the URI path. (env: PFLT_PYXIS_HOST)"))
	_ = viper.BindPFlag("pyxis_host", flags.Lookup("pyxis-host"))

	flags.String("pyxis-env", check.DefaultPyxisEnv, "Env to use for Pyxis submissions.")
	_ = viper.BindPFlag("pyxis_env", flags.Lookup("pyxis-env"))

	flags.String("certification-project-id", "", fmt.Sprintf("Certification Project ID from connect.redhat.com/projects/{certification-project-id}/overview\n"+
		"URL paramater. This value may differ from the PID on the overview page. (env: PFLT_CERTIFICATION_PROJECT_ID)"))
	_ = viper.BindPFlag("certification_project_id", flags.Lookup("certification-project-id"))

	flags.String("platform", rt.GOARCH, "Architecture of image to pull. Defaults to runtime platform.")
	_ = viper.BindPFlag("platform", flags.Lookup("platform"))

	return checkContainerCmd
}

// checkContainerRunE executes checkContainer using the user args to inform the execution.
func checkContainerRunE(cmd *cobra.Command, args []string, runpreflight runPreflight, viper *viper.Viper) error {
	ctx := cmd.Context()
	logger, err := logr.FromContext(ctx)
	if err != nil {
		return fmt.Errorf("invalid logging configuration")
	}
	logger.Info("certification library version", "version", version.Version.String())

	containerImage := args[0]

	// Render the Viper configuration as a runtime.Config
	cfg, err := runtime.NewConfigFrom(*viper)
	if err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	cfg.Image = containerImage

	containerImagePlatforms, err := platformsToBeProcessed(cmd, cfg)
	if err != nil {
		return err
	}

	for _, platform := range containerImagePlatforms {
		logger.Info(fmt.Sprintf("running checks for %s for platform %s", containerImage, platform))
		artifactsWriter, err := artifacts.NewFilesystemWriter(artifacts.WithDirectory(filepath.Join(cfg.Artifacts, platform)))
		if err != nil {
			return err
		}

		// Add the artifact writer to the context for use by checks.
		ctx := artifacts.ContextWithWriter(ctx, artifactsWriter)

		formatter, err := formatters.NewByName(formatters.DefaultFormat)
		if err != nil {
			return err
		}

		opts := generateContainerCheckOptions(cfg)
		opts = append(opts, container.WithPlatform(platform))

		checkcontainer := container.NewCheck(
			containerImage,
			opts...,
		)

		pc := lib.NewPyxisClient(ctx, cfg.CertificationProjectID, cfg.PyxisAPIToken, cfg.PyxisHost)
		resultSubmitter := lib.ResolveSubmitter(pc, cfg.CertificationProjectID, cfg.DockerConfig, cfg.LogFile)

		// Run the  container check.
		cmd.SilenceUsage = true

		if err := runpreflight(
			ctx,
			checkcontainer.Run,
			cli.CheckConfig{
				IncludeJUnitResults: cfg.WriteJUnit,
				SubmitResults:       cfg.Submit,
			},
			formatter,
			&runtime.ResultWriterFile{},
			resultSubmitter,
			viper.GetString("pyxis_env"),
		); err != nil {
			return err
		}

		// checking for offline flag, if present tar up the contents of the artifacts directory
		if cfg.Offline {
			src := artifactsWriter.Path()
			var buf bytes.Buffer

			// check to see if a tar file already exist to account for someone re-running
			exists, err := artifactsWriter.Exists(check.DefaultArtifactsTarFileName)
			if err != nil {
				return fmt.Errorf("unable to check if tar already exists: %v", err)
			}

			// remove the tar file if it exists
			if exists {
				err = artifactsWriter.Remove(check.DefaultArtifactsTarFileName)
				if err != nil {
					return fmt.Errorf("unable to remove existing tar: %v", err)
				}
			}

			// tar the directory
			err = artifactsTar(ctx, src, &buf)
			if err != nil {
				return fmt.Errorf("unable to tar up artifacts directory: %v", err)
			}

			// writing the tar file to disk
			_, err = artifactsWriter.WriteFile(check.DefaultArtifactsTarFileName, &buf)
			if err != nil {
				return fmt.Errorf("could not artifacts tar to artifacts dir: %w", err)
			}

			logger.Info("artifact tar written to disk", "filename", check.DefaultArtifactsTarFileName)
		}
	}

	return nil
}

func checkContainerPositionalArgs(cmd *cobra.Command, args []string, viper *viper.Viper) error {
	if len(args) != 1 {
		return fmt.Errorf("a container image positional argument is required")
	}

	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Changed && strings.Contains(f.Value.String(), "--submit") {
			// We have --submit in one of the flags. That's a problem.
			// We will set the submit flag to true so that the next block functions properly
			submit = true
		}
	})

	// --submit was specified
	if submit {
		// If the flag is not marked as changed AND viper hasn't gotten it from environment, it's an error
		if !cmd.Flag("certification-project-id").Changed && !viper.IsSet("certification_project_id") {
			return fmt.Errorf("certification Project ID must be specified when --submit is present")
		}
		if !cmd.Flag("pyxis-api-token").Changed && !viper.IsSet("pyxis_api_token") {
			return fmt.Errorf("pyxis API Token must be specified when --submit is present")
		}

		// If the flag is marked as changed AND it's still empty, it's an error
		if cmd.Flag("certification-project-id").Changed && viper.GetString("certification_project_id") == "" {
			return fmt.Errorf("certification Project ID cannot be empty when --submit is present")
		}
		if cmd.Flag("pyxis-api-token").Changed && viper.GetString("pyxis_api_token") == "" {
			return fmt.Errorf("pyxis API Token cannot be empty when --submit is present")
		}

		// Finally, if either certification project id or pyxis api token start with '--', it's an error
		if strings.HasPrefix(viper.GetString("pyxis_api_token"), "--") || strings.HasPrefix(viper.GetString("certification_project_id"), "--") {
			return fmt.Errorf("pyxis API token and certification ID are required when --submit is present")
		}
	}

	return nil
}

func checkContainerPreRunE(cmd *cobra.Command, args []string) error {
	certificationProjectID := viper.GetString("certification_project_id")
	validatedCertificationProjectID, err := validateCertificationProjectID(certificationProjectID)
	if err != nil {
		return err
	}
	if validatedCertificationProjectID != certificationProjectID {
		viper.Set("certification_project_id", validatedCertificationProjectID)
	}
	return nil
}

// validateCertificationProjectID validates that the certification project id is in the proper format
// and throws an error if the value provided is in a legacy format that is not usable to query pyxis
func validateCertificationProjectID(certificationProjectID string) (string, error) {
	// splitting the certification project id into parts. if there are more than 2 elements in the array,
	// we know they inputted a legacy project id, which can not be used to query pyxis
	parts := strings.Split(certificationProjectID, "-")

	if len(parts) > 2 {
		return certificationProjectID, fmt.Errorf("certification project id: %s is improperly formatted see help command for instructions on obtaining proper value", certificationProjectID)
	}

	if parts[0] == "ospid" {
		return parts[1], nil
	}

	return certificationProjectID, nil
}

// generateContainerCheckOptions returns appropriate container.Options based on cfg.
func generateContainerCheckOptions(cfg *runtime.Config) []container.Option {
	o := []container.Option{
		container.WithCertificationProject(cfg.CertificationProjectID, cfg.PyxisAPIToken),
		container.WithDockerConfigJSONFromFile(cfg.DockerConfig),
		// Always add PyxisHost, since the value is always set in viper config parsing.
		container.WithPyxisHost(cfg.PyxisHost),
		container.WithPlatform(cfg.Platform),
		container.WithManifestListDigest(cfg.ManifestListDigest),
	}

	// set auth information if both are present in config.
	if cfg.PyxisAPIToken != "" && cfg.CertificationProjectID != "" {
		o = append(o, container.WithCertificationProject(cfg.CertificationProjectID, cfg.PyxisAPIToken))
	}

	if cfg.Insecure {
		// Do not allow for submission if Insecure is set.
		// This is a secondary check to be safe.
		cfg.Submit = false
		o = append(o, container.WithInsecureConnection())
	}

	return o
}

// artifactsTar takes a source path and a writer; a tar writer loops over the files in the source
// directory, writes the appropriate header information and copies the file into the tar writer
//
//nolint:unparam // ctx is unused. Keep for future use.
func artifactsTar(ctx context.Context, src string, w io.Writer) error {
	// ensure the src actually exists before trying to tar it
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("unable to tar files - %v", err.Error())
	}

	tw := tar.NewWriter(w)
	defer tw.Close()

	// getting a list of DirEntry's
	files, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	// iterating over the list
	for _, file := range files {
		// ignoring directories and only processing files
		if file.IsDir() {
			continue
		}

		// getting the FileInfo from the DirEntry, account for errors with this call ie ErrNotExist
		fileInfo, err := file.Info()
		if err != nil {
			return err
		}

		// continue on non-regular files
		if !fileInfo.Mode().IsRegular() {
			continue
		}

		// create a new dir/file header
		header, err := tar.FileInfoHeader(fileInfo, fileInfo.Name())
		if err != nil {
			return err
		}

		// write the header
		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		err = func() error {
			// open files to tar
			f, err := os.Open(filepath.Join(src, fileInfo.Name()))
			if err != nil {
				return err
			}
			defer f.Close()

			// copy file data into tar writer
			if _, err := io.Copy(tw, f); err != nil {
				return err
			}

			return nil
		}()
		if err != nil {
			return err
		}
	}

	return nil
}

func platformsToBeProcessed(cmd *cobra.Command, cfg *runtime.Config) ([]string, error) {
	ctx := cmd.Context()
	logger := logr.FromContextOrDiscard(ctx)

	// flag.Changed is not set if the env is all that is set.
	_, platformEnvPresent := os.LookupEnv("PFLT_PLATFORM")
	platformChanged := cmd.Flags().Lookup("platform").Changed || platformEnvPresent

	containerImagePlatforms := []string{cfg.Platform}

	options := crane.GetOptions(option.GenerateCraneOptions(ctx, cfg)...)
	ref, err := name.ParseReference(cfg.Image, options.Name...)
	if err != nil {
		return nil, fmt.Errorf("invalid image reference: %w", err)
	}

	desc, err := remote.Get(ref, options.Remote...)
	if err != nil {
		return nil, fmt.Errorf("invalid manifest?: %w", err)
	}

	if !desc.MediaType.IsIndex() {
		// This means the passed image is just an image, and not a manifest list
		// So, let's get the config to find out if the image matches the
		// given platform.
		img, err := desc.Image()
		if err != nil {
			return nil, fmt.Errorf("could not convert descriptor to image: %w", err)
		}
		cfgFile, err := img.ConfigFile()
		if err != nil {
			return nil, fmt.Errorf("could not retrieve image config: %w", err)
		}

		// A specific arch was specified. This image does not contain that arch.
		if cfgFile.Architecture != cfg.Platform && !platformChanged {
			return nil, fmt.Errorf("cannot process image manifest of different arch without platform override")
		}

		// At this point, we know that the original containerImagePlatform is correct, so
		// we can just return it and skip the below.
		// While we could just let this fall through to the end, I'd rather short-circuit
		// here, in case any further changes disrupt that logic flow.
		return containerImagePlatforms, nil
	}

	// If platform param is not changed, it means that a platform was not specified on the
	// command line. Therefore, we should process all platforms in the manifest list.
	// As long as what is poinged to is a manifest list. Otherwise, it will just be the
	// currnt runtime platform.
	if desc.MediaType.IsIndex() {
		logger.V(log.DBG).Info("manifest list detected, checking all platforms in manifest")

		idx, err := desc.ImageIndex()
		if err != nil {
			return nil, fmt.Errorf("could not convert descriptor to index: %w", err)
		}
		manifestListDigest, err := idx.Digest()
		if err != nil {
			return nil, fmt.Errorf("could not retrieve index digest: %w", err)
		}
		cfg.ManifestListDigest = manifestListDigest.String()

		manifest, err := idx.IndexManifest()
		if err != nil {
			return nil, fmt.Errorf("could not retrieve index manifest: %w", err)
		}

		// Preflight was given a manifest list. --platform was not specified.
		// Therefore, all platforms in the manifest list that we support
		// for certification ie: {"arm64", "amd64", "ppc64le", "s390x"}, should be processed.
		// Create a new slice since the original was for a single platform.
		containerImagePlatforms = make([]string, 0, len(manifest.Manifests))
		for _, img := range manifest.Manifests {
			if platformChanged && cfg.Platform != img.Platform.Architecture {
				// The user selected a platform. If this isn't it, continue.
				continue
			}
			if img.Platform.Architecture == "unknown" && img.Platform.OS == "unknown" {
				// This must be an attestation manifest. Skip it.
				continue
			}
			if !slices.Contains(allowedArchitectures(), img.Platform.Architecture) {
				// The user has a architecture type in the manifest list that we do not support.
				continue
			}
			containerImagePlatforms = append(containerImagePlatforms, img.Platform.Architecture)
		}
		if platformChanged && len(containerImagePlatforms) == 0 {
			return nil, fmt.Errorf("invalid platform specified")
		}
	}

	return containerImagePlatforms, nil
}

// allowedArchitectures returns a list of container architectures that are supported for certification.
// Only supported architectures are "arm64", "amd64", "ppc64le", "s390x".
func allowedArchitectures() []string {
	return []string{"arm64", "amd64", "ppc64le", "s390x"}
}
