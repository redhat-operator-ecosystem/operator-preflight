package cmd

import (
	"os"
	"path/filepath"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/spf13/viper"
)

var _ = Describe("Check Operator", func() {
	BeforeEach(createAndCleanupDirForArtifactsAndLogs)

	When("running the check operator subcommand", func() {
		When("without the operator bundle image being provided", func() {
			It("should return an error", func() {
				_, err := executeCommand(checkOperatorCmd(mockRunPreflightReturnNil, viper.New()))
				Expect(err).To(HaveOccurred())
			})
		})

		When("without having set the KUBECONFIG environment variable", func() {
			BeforeEach(func() {
				if val, isSet := os.LookupEnv("KUBECONFIG"); isSet {
					DeferCleanup(os.Setenv, "KUBECONFIG", val)
				}
				os.Unsetenv("KUBECONFIG")
			})
			It("should return an error", func() {
				out, err := executeCommand(checkOperatorCmd(mockRunPreflightReturnNil, viper.New()), "quay.io/example/image:mytag")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring("KUBECONFIG could not"))
			})
		})

		When("without having set the PFLT_INDEXIMAGE environment variable", func() {
			BeforeEach(func() {
				if val, isSet := os.LookupEnv("PFLT_INDEXIMAGE"); isSet {
					DeferCleanup(os.Setenv, "PFLT_INDEXIMAGE", val)
				}
				os.Unsetenv("PFLT_INDEXIMAGE")
				if val, isSet := os.LookupEnv("KUBECONFIG"); isSet {
					DeferCleanup(os.Setenv, "KUBECONFIG", val)
				} else {
					DeferCleanup(os.Unsetenv, "KUBECONFIG")
				}
				os.Setenv("KUBECONFIG", "foo")
			})
			It("should return an error", func() {
				out, err := executeCommand(checkOperatorCmd(mockRunPreflightReturnNil, viper.New()), "quay.io/example/image:mytag")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring("PFLT_INDEXIMAGE could not"))
			})
		})

		When("with all of the required parameters", func() {
			var v *viper.Viper
			BeforeEach(func() {
				v = viper.New()
				v.Set("indexImage", "foo")
				if val, isSet := os.LookupEnv("KUBECONFIG"); isSet {
					DeferCleanup(os.Setenv, "KUBECONFIG", val)
				} else {
					DeferCleanup(os.Unsetenv, "KUBECONFIG")
				}

				tmpDir, err := os.MkdirTemp("", "preflight-operator-test-*")
				Expect(err).ToNot(HaveOccurred())
				DeferCleanup(os.RemoveAll, tmpDir)

				// creating an empty kubeconfig file in the tmpDir so we don't fail for a missing file
				f1, err := os.Create(filepath.Join(tmpDir, "kubeconfig"))
				Expect(err).ToNot(HaveOccurred())
				defer f1.Close()

				os.Setenv("KUBECONFIG", f1.Name())
			})
			It("should reach the core logic, and execute the mocked RunPreflight", func() {
				out, err := executeCommandWithLogger(checkOperatorCmd(mockRunPreflightReturnNil, v), logr.Discard(), "quay.io/example/image:mytag")
				Expect(err).ToNot(HaveOccurred())
				Expect(out).ToNot(BeNil())
			})
			It("should reach the core logic, and execute the mocked RunPreflight and return error", func() {
				out, err := executeCommandWithLogger(checkOperatorCmd(mockRunPreflightReturnErr, v), logr.Discard(), "quay.io/example/image:mytag")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring("random error"))
			})
		})

		When("with an invalid KUBECONFIG file location", func() {
			var v *viper.Viper
			BeforeEach(func() {
				v = viper.New()
				v.Set("indexImage", "foo")
				if val, isSet := os.LookupEnv("KUBECONFIG"); isSet {
					DeferCleanup(os.Setenv, "KUBECONFIG", val)
				} else {
					DeferCleanup(os.Unsetenv, "KUBECONFIG")
				}

				os.Setenv("KUBECONFIG", "foo")
			})
			It("should return a no such file error", func() {
				out, err := executeCommandWithLogger(checkOperatorCmd(mockRunPreflightReturnNil, v), logr.Discard(), "quay.io/example/image:mytag")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring(": open foo: no such file or directory"))
			})
		})

		When("with a KUBECONFIG file location that is a directory", func() {
			var v *viper.Viper
			BeforeEach(func() {
				v = viper.New()
				v.Set("indexImage", "foo")
				if val, isSet := os.LookupEnv("KUBECONFIG"); isSet {
					DeferCleanup(os.Setenv, "KUBECONFIG", val)
				} else {
					DeferCleanup(os.Unsetenv, "KUBECONFIG")
				}

				os.Setenv("KUBECONFIG", ".")
			})
			It("should return an is a directory error", func() {
				out, err := executeCommandWithLogger(checkOperatorCmd(mockRunPreflightReturnNil, v), logr.Discard(), "quay.io/example/image:mytag")
				Expect(err).To(HaveOccurred())
				Expect(out).To(ContainSubstring(": is a directory"))
			})
		})
	})

	When("checking for required environment variables", func() {
		When("specifically, KUBECONFIG", func() {
			BeforeEach(func() {
				if val, isSet := os.LookupEnv("KUBECONIFG"); isSet {
					DeferCleanup(os.Setenv, "KUBECONFIG", val)
				} else {
					DeferCleanup(os.Unsetenv, "KUBECONFIG")
				}
				os.Setenv("KUBECONFIG", "foo")
			})
			It("should not encounter an error if the value is set", func() {
				err := ensureKubeconfigIsSet()
				Expect(err).ToNot(HaveOccurred())
			})

			It("should encounter an error if the value is not set", func() {
				os.Unsetenv("KUBECONFIG")
				err := ensureKubeconfigIsSet()
				Expect(err).To(HaveOccurred())
			})
		})

		When("specifically, PFLT_INDEXIMAGE", func() {
			var v *viper.Viper
			BeforeEach(func() {
				v = viper.New()
				v.Set("indexImage", "foo")
			})
			It("should not encounter an error if the value is set", func() {
				err := ensureIndexImageConfigIsSet(v)
				Expect(err).ToNot(HaveOccurred())
			})

			It("should encounter an error if the value is not set", func() {
				v.Set("indexImage", "")
				err := ensureIndexImageConfigIsSet(v)
				Expect(err).To(HaveOccurred())
			})
		})
	})

	When("testing positional arg parsing", func() {
		var v *viper.Viper
		var posArgs []string

		// failure cases are tested earlier in this file by running executeCommand.
		// This tests the success case using the standalone function in order
		// to prevent trying to run the entire RunE func in previous cases.
		BeforeEach(func() {
			v = viper.New()
			posArgs = append(posArgs, "first")
			v.Set("indexImage", "foo")
			if val, isSet := os.LookupEnv("KUBECONIFG"); isSet {
				DeferCleanup(os.Setenv, "KUBECONFIG", val)
			} else {
				DeferCleanup(os.Unsetenv, "KUBECONFIG")
			}
			os.Setenv("KUBECONFIG", "foo")
		})

		It("should succeed when all positional arg constraints and environment constraints are correct", func() {
			err := checkOperatorPositionalArgs(posArgs, v)
			Expect(err).ToNot(HaveOccurred())
		})
	})
})
