package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"

	"github.com/redhat-openshift-ecosystem/openshift-preflight/certification"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/check"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/cli"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/formatters"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/lib"

	"github.com/go-logr/logr"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	cranev1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	cranev1types "github.com/google/go-containerregistry/pkg/v1/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	"github.com/spf13/afero"
	"github.com/spf13/viper"
)

func createPlatformImage(arch string, addlLayers int) cranev1.Image {
	// Expected values.
	img, err := random.Image(1024, 5)
	Expect(err).ToNot(HaveOccurred())

	for i := 0; i < addlLayers; i++ {
		newLayer, err := random.Layer(1024, cranev1types.OCILayer)
		Expect(err).ToNot(HaveOccurred())
		img, err = mutate.AppendLayers(img, newLayer)
		Expect(err).ToNot(HaveOccurred())
	}

	cfgFile, err := img.ConfigFile()
	Expect(err).ToNot(HaveOccurred())

	cfgFile.Architecture = arch

	cfgImg, err := mutate.ConfigFile(img, cfgFile)
	Expect(err).ToNot(HaveOccurred())

	return cfgImg
}

func createImageAndPush(src, arch string, addlLayers int) string {
	img := createPlatformImage(arch, addlLayers)
	Expect(crane.Push(img, src)).To(Succeed())
	return src
}

var _ = Describe("Check Container Command", func() {
	var src string
	var manifestListSrc string
	var srcppc string
	var manifests map[string]string
	var s *httptest.Server
	var u *url.URL
	BeforeEach(func() {
		manifests = make(map[string]string, 2)
		// Set up a fake registry.
		registryLogger := log.New(io.Discard, "", log.Ldate)
		s = httptest.NewServer(registry.New(registry.Logger(registryLogger)))
		DeferCleanup(s.Close)

		var err error
		u, err = url.Parse(s.URL)
		Expect(err).ToNot(HaveOccurred())

		src = fmt.Sprintf("%s/test/crane", u.Host)
		manifests["image"] = createImageAndPush(src, "amd64", 0)

		srcppc = fmt.Sprintf("%s/test/craneppc", u.Host)
		manifests["imageppc"] = createImageAndPush(srcppc, "ppc64le", 1)

		manifestListSrc = fmt.Sprintf("%s/test/cranelist", u.Host)
		manifests["index"] = manifestListSrc

		platforms := [5]string{"amd64", "arm64", "ppc64le", "s390x", "arm"}
		lst, err := random.Index(1024, 5, int64(len(platforms)+1))
		Expect(err).ToNot(HaveOccurred())

		ref, err := name.ParseReference(manifestListSrc)
		Expect(err).ToNot(HaveOccurred())

		m, err := lst.IndexManifest()
		Expect(err).ToNot(HaveOccurred())

		for i, manifest := range m.Manifests {
			switch {
			case i == len(platforms):
				m.Manifests[i].Platform = &cranev1.Platform{
					Architecture: "unknown",
					OS:           "unknown",
				}
			case manifest.MediaType.IsImage():
				m.Manifests[i].Platform = &cranev1.Platform{
					Architecture: platforms[i],
					OS:           "linux",
				}
			}
		}
		err = remote.WriteIndex(ref, lst)
		Expect(err).ToNot(HaveOccurred())
	})
	BeforeEach(createAndCleanupDirForArtifactsAndLogs)

	When("a manifest list is passed", func() {
		When("default params otherwise", func() {
			var v *viper.Viper

			BeforeEach(func() {
				v = viper.New()
			})

			It("should not error", func() {
				_, err := executeCommandWithLogger(checkContainerCmd(mockRunPreflightReturnNil, v), logr.Discard(), manifestListSrc)
				Expect(err).ToNot(HaveOccurred())
			})
		})
	})

	DescribeTable("--platform tests",
		func(manifestKey string, platform string, match types.GomegaMatcher, includePlatformArg bool) {
			v := viper.New()
			args := []string{manifests[manifestKey]}
			if includePlatformArg {
				args = append(args, "--platform", platform)
			}
			_, err := executeCommandWithLogger(checkContainerCmd(mockRunPreflightReturnNil, v), logr.Discard(), args...)
			Expect(err).To(match)
		},
		Entry("image manifest, valid platform", "image", "amd64", Not(HaveOccurred()), true),
		Entry("image manifest, different platform, modifier", "image", "none", Not(HaveOccurred()), true),
		Entry("image manifest, different platform, no modifier", "imageppc", "none", HaveOccurred(), false),
		Entry("index manifest, valid platform", "index", "amd64", Not(HaveOccurred()), true),
		Entry("index manifest, invalid platform", "index", "none", HaveOccurred(), true),
	)

	When("validating check container arguments and flags", func() {
		Context("and the user provided more than 1 positional arg", func() {
			It("should fail to run", func() {
				_, err := executeCommand(checkContainerCmd(mockRunPreflightReturnNil, viper.New()), "foo", "bar")
				Expect(err).To(HaveOccurred())
			})
		})

		When("the user provided less than 1 positional arg", func() {
			It("should fail to run", func() {
				_, err := executeCommand(checkContainerCmd(mockRunPreflightReturnNil, viper.New()))
				Expect(err).To(HaveOccurred())
			})
		})

		DescribeTable("and the user has enabled the submit flag",
			func(errString string, args []string) {
				out, err := executeCommand(checkContainerCmd(mockRunPreflightReturnNil, viper.New()), args...)
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring(errString))
			},
			Entry("certification-project-id and pyxis-api-token are not supplied", "certification Project ID must be specified when --submit is present", []string{"--submit", "foo"}),
			Entry("pyxis-api-token is not supplied", "pyxis API Token must be specified when --submit is present", []string{"foo", "--submit", "--certification-project-id=fooid"}),
			Entry("certification-project-id is not supplied", "certification Project ID must be specified when --submit is present", []string{"--submit", "foo", "--pyxis-api-token=footoken"}),
			Entry("pyxis-api-token flag is present but empty because of '='", "cannot be empty when --submit is present", []string{"foo", "--submit", "--certification-project-id=fooid", "--pyxis-api-token="}),
			Entry("certification-project-id flag is present but empty because of '='", "cannot be empty when --submit is present", []string{"foo", "--submit", "--certification-project-id=", "--pyxis-api-token=footoken"}),
			Entry("submit is passed after empty api token", "pyxis API token and certification ID are required when --submit is present", []string{"foo", "--certification-project-id=fooid", "--pyxis-api-token", "--submit"}),
			Entry("submit is passed with explicit value after empty api token", "pyxis API token and certification ID are required when --submit is present", []string{"foo", "--certification-project-id=fooid", "--pyxis-api-token", "--submit=true"}),
			Entry("submit is passed and insecure is specified", "if any flags in the group [submit insecure] are set", []string{"foo", "--submit", "--insecure", "--certification-project-id=fooid", "--pyxis-api-token=footoken"}),
		)

		When("the user enables the submit flag", func() {
			var v *viper.Viper

			BeforeEach(func() {
				v = viper.New()
			})
			When("environment variables are used for certification ID and api token", func() {
				BeforeEach(func() {
					os.Setenv("PFLT_CERTIFICATION_PROJECT_ID", "certid")
					os.Setenv("PFLT_PYXIS_API_TOKEN", "tokenid")
					DeferCleanup(os.Unsetenv, "PFLT_CERTIFICATION_PROJECT_ID")
					DeferCleanup(os.Unsetenv, "PFLT_PYXIS_API_TOKEN")
				})
				It("should still execute with no error", func() {
					initConfig(v)
					submit = true
					err := checkContainerPositionalArgs(checkContainerCmd(mockRunPreflightReturnNil, v), []string{"foo"}, v)
					Expect(err).ToNot(HaveOccurred())
					Expect(v.GetString("pyxis_api_token")).To(Equal("tokenid"))
					Expect(v.GetString("certification_project_id")).To(Equal("certid"))
				})
			})
			When("a config file is used", func() {
				BeforeEach(func() {
					fs := afero.NewMemMapFs()
					v.SetFs(fs)
					config := `pyxis_api_token: mytoken
certification_project_id: mycertid`
					Expect(afero.WriteFile(fs, "/config/config.yaml", bytes.NewBufferString(config).Bytes(), 0o644)).To(Succeed())
					v.AddConfigPath("/config")
				})
				It("should still execute with no error", func() {
					// Make sure that we've read the config file
					initConfig(v)
					submit = true

					err := checkContainerPositionalArgs(checkContainerCmd(mockRunPreflightReturnNil, v), []string{"foo"}, v)
					Expect(err).ToNot(HaveOccurred())
					Expect(v.GetString("pyxis_api_token")).To(Equal("mytoken"))
					Expect(v.GetString("certification_project_id")).To(Equal("mycertid"))
				})
			})
		})
	})

	DescribeTable("validating the certification-project-id flag",
		func(value, expected string, succeed bool) {
			updated, err := validateCertificationProjectID(value)
			if succeed {
				Expect(err).ToNot(HaveOccurred())
				Expect(updated).To(Equal(expected))
				return
			}
			Expect(err).To(HaveOccurred())
		},
		Entry("the flag is set properly should not change value", "123456789", "123456789", true),
		Entry("a valid ospid should strip ospid-", "ospid-123456789", "123456789", true),
		Entry("legacy format with ospid", "ospid-62423-f26c346-6cc1dc7fae92", "", false),
		Entry("legacy format without ospid", "62423-f26c346-6cc1dc7fae92", "", false),
	)

	When("running the check container subcommand with a logger provided", func() {
		When("with all of the required parameters", func() {
			It("should reach the core logic, and execute the mocked RunPreflight", func() {
				_, err := executeCommandWithLogger(checkContainerCmd(mockRunPreflightReturnNil, viper.New()), logr.Discard(), src)
				Expect(err).ToNot(HaveOccurred())
			})
		})
		When("with all of the required parameters with error mocked", func() {
			It("should reach the core logic, and execute the mocked RunPreflight and return error", func() {
				_, err := executeCommandWithLogger(checkContainerCmd(mockRunPreflightReturnErr, viper.New()), logr.Discard(), src)
				Expect(err).To(HaveOccurred())
			})
		})
	})

	When("running the check container subcommand with a offline config provided", func() {
		var v *viper.Viper

		BeforeEach(func() {
			v = viper.New()
		})
		When("with all of the required parameters", func() {
			BeforeEach(func() {
				tmpDir, err := os.MkdirTemp("", "preflight-submit-test-*")
				Expect(err).ToNot(HaveOccurred())
				DeferCleanup(os.RemoveAll, tmpDir)

				platformDir := filepath.Join(tmpDir, runtime.GOARCH)
				Expect(os.Mkdir(platformDir, 0o755)).Should(Succeed())

				// creating test files on in the tmpDir so the tar function has files to tar
				f1, err := os.Create(filepath.Join(platformDir, "test-file-1.json"))
				Expect(err).ToNot(HaveOccurred())
				defer f1.Close()

				f2, err := os.Create(filepath.Join(platformDir, "test-file-1.json"))
				Expect(err).ToNot(HaveOccurred())
				defer f2.Close()

				v.Set("artifacts", tmpDir)
				v.Set("offline", true)
			})
			It("should reach core logic, and the additional offline logic", func() {
				out, err := executeCommandWithLogger(checkContainerCmd(mockRunPreflightReturnNil, v), logr.Discard(), src)
				Expect(err).ToNot(HaveOccurred())
				Expect(out).ToNot(BeNil())
			})
		})
		When("an existing artifacts.tar already on disk", func() {
			BeforeEach(func() {
				tmpDir, err := os.MkdirTemp("", "preflight-submit-test-*")
				Expect(err).ToNot(HaveOccurred())
				DeferCleanup(os.RemoveAll, tmpDir)

				platformDir := filepath.Join(tmpDir, runtime.GOARCH)
				Expect(os.Mkdir(platformDir, 0o755)).Should(Succeed())

				// creating test files on in the tmpDir so the tar function has files to tar
				f1, err := os.Create(filepath.Join(platformDir, "test-file-1.json"))
				Expect(err).ToNot(HaveOccurred())
				defer f1.Close()

				f2, err := os.Create(filepath.Join(platformDir, "test-file-1.json"))
				Expect(err).ToNot(HaveOccurred())
				defer f2.Close()

				// creating a tar file to mimic a user re-running check container for a second time
				f3, err := os.Create(filepath.Join(tmpDir, check.DefaultArtifactsTarFileName))
				Expect(err).ToNot(HaveOccurred())
				defer f3.Close()

				v.Set("artifacts", tmpDir)
				v.Set("offline", true)
			})
			It("should reach the additional offline logic, and remove existing tar file", func() {
				out, err := executeCommandWithLogger(checkContainerCmd(mockRunPreflightReturnNil, v), logr.Discard(), src)
				Expect(err).ToNot(HaveOccurred())
				Expect(out).ToNot(BeNil())
			})
		})
	})

	When("artifactsTar is called directly", func() {
		When("the src does not exist", func() {
			It("should get an unable to tar files error", func() {
				err := artifactsTar(context.Background(), "", nil)
				Expect(err).To(HaveOccurred())
			})
		})
		When("a bad writer is passed", func() {
			It("should get an error with trying to write the header", func() {
				err := artifactsTar(context.Background(), ".", errWriter(0))
				Expect(err).To(HaveOccurred())
			})
		})
		When("a src has no permissions", func() {
			var tmpDir string
			var err error
			var buf bytes.Buffer
			JustBeforeEach(func() {
				tmpDir, err = os.MkdirTemp("", "preflight-submit-test-*")
				Expect(err).ToNot(HaveOccurred())
				err = os.Chmod(tmpDir, 0o000)
				Expect(err).ToNot(HaveOccurred())
				DeferCleanup(os.RemoveAll, tmpDir)
			})
			It("should get an error with trying to read directory", func() {
				err := artifactsTar(context.Background(), tmpDir, &buf)
				Expect(err).To(HaveOccurred())
			})
		})
		When("and src only contains another directory", func() {
			var tmpDir string
			var err error
			var buf bytes.Buffer
			JustBeforeEach(func() {
				tmpDir, err = os.MkdirTemp("", "preflight-submit-test-*")
				Expect(err).ToNot(HaveOccurred())
				err = os.Mkdir(filepath.Join(tmpDir, "test"), 0o755)
				Expect(err).ToNot(HaveOccurred())
				DeferCleanup(os.RemoveAll, tmpDir)
			})
			It("should continue and not tar any files", func() {
				err := artifactsTar(context.Background(), tmpDir, &buf)
				Expect(err).To(BeNil())
			})
		})
	})
})

func mockRunPreflightReturnNil(context.Context, func(ctx context.Context) (certification.Results, error), cli.CheckConfig, formatters.ResponseFormatter, lib.ResultWriter, lib.ResultSubmitter, string) error {
	return nil
}

func mockRunPreflightReturnErr(context.Context, func(ctx context.Context) (certification.Results, error), cli.CheckConfig, formatters.ResponseFormatter, lib.ResultWriter, lib.ResultSubmitter, string) error {
	return errors.New("random error")
}
