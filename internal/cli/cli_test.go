package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/redhat-openshift-ecosystem/openshift-preflight/artifacts"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/certification"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/check"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/formatters"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/image"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/lib"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/log"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/runtime"

	// This file imports logrus instead of internal/log because a standalone logger is used
	// for test specs defined here.
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CLI Library function", func() {
	When("invoking preflight using the CLI library", func() {
		Context("without passing in an artifact writer ", func() {
			It("should throw an error", func() {
				err := RunPreflight(context.TODO(), func(ctx context.Context) (certification.Results, error) { return certification.Results{}, nil }, CheckConfig{}, nil, nil, nil, "prod")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("no artifact writer"))
			})
		})

		Context("with a preconfigured artifact writer", func() {
			var testcontext context.Context
			var artifactWriter *artifacts.FilesystemWriter
			var testFormatter formatters.ResponseFormatter

			BeforeEach(func() {
				tmpDir, err := os.MkdirTemp("", "lib-execute-*")
				Expect(err).ToNot(HaveOccurred())
				artifactWriter, err = artifacts.NewFilesystemWriter(artifacts.WithDirectory(tmpDir))
				Expect(err).ToNot(HaveOccurred())
				testcontext = artifacts.ContextWithWriter(context.Background(), artifactWriter)
				DeferCleanup(os.RemoveAll, tmpDir)

				testFormatter, err = formatters.NewByName(formatters.DefaultFormat)
				Expect(err).ToNot(HaveOccurred())
			})

			It("Should return an error if unable to successfully check execution encounters an error", func() {
				err := RunPreflight(testcontext, func(ctx context.Context) (certification.Results, error) {
					return certification.Results{}, errors.New("some error")
				}, CheckConfig{}, testFormatter, &runtime.ResultWriterFile{}, nil, "prod")
				Expect(err).To(HaveOccurred())
			})

			It("Should throw an error writing formatted results if the formatter returns an error", func() {
				var err error
				testFormatter, err = formatters.New("test", "test", func(ctx context.Context, r certification.Results) (response []byte, formattingError error) {
					return []byte{}, errors.New("unable to format")
				})
				Expect(err).ToNot(HaveOccurred())

				err = RunPreflight(testcontext, func(ctx context.Context) (certification.Results, error) { return certification.Results{}, nil }, CheckConfig{}, testFormatter, &runtime.ResultWriterFile{}, nil, "prod")
				Expect(err).To(HaveOccurred())
			})

			When("JUnit results are requested", func() {
				It("Should write the junit results as an artifact", func() {
					c := CheckConfig{
						IncludeJUnitResults: true,
					}

					err := RunPreflight(testcontext, func(ctx context.Context) (certification.Results, error) {
						return certification.Results{
							TestedImage:   "testWithJUnit",
							PassedOverall: true,
							Passed: []certification.Result{
								{
									Check: check.NewGenericCheck(
										"testJUnitWritten",
										func(ctx context.Context, ir image.ImageReference) (bool, error) { return true, nil },
										check.Metadata{},
										check.HelpText{},
									),
									ElapsedTime: 1,
								},
							},
							Failed: []certification.Result{},
							Errors: []certification.Result{},
						}, nil
					}, c, testFormatter, &runtime.ResultWriterFile{}, nil, "prod")
					Expect(err).ToNot(HaveOccurred())
					expectedJUnitResultFile := filepath.Join(artifactWriter.Path(), "results-junit.xml")
					Expect(expectedJUnitResultFile).To(BeAnExistingFile())
				})
			})

			When("Submission is requested", func() {
				It("Should call the submitter", func() {
					c := CheckConfig{
						SubmitResults: true,
					}

					buf := bytes.NewBuffer([]byte{})
					submitterTestLogger := logr.Logger{}.WithSink(log.NewBufferSink(buf))
					testSubmitter := lib.NewNoopSubmitter(true, &submitterTestLogger)

					err := RunPreflight(testcontext, func(ctx context.Context) (certification.Results, error) {
						return certification.Results{
							TestedImage:   "testSubmission",
							PassedOverall: true,
							Passed: []certification.Result{
								{
									Check: check.NewGenericCheck(
										"testSubmission",
										func(ctx context.Context, ir image.ImageReference) (bool, error) { return true, nil },
										check.Metadata{},
										check.HelpText{},
									),
									ElapsedTime: 1,
								},
							},
							Failed: []certification.Result{},
							Errors: []certification.Result{},
						}, nil
					}, c, testFormatter, &runtime.ResultWriterFile{}, testSubmitter, "prod")
					Expect(err).ToNot(HaveOccurred())

					contents, err := io.ReadAll(buf)
					Expect(err).ToNot(HaveOccurred())
					Expect(string(contents)).To(ContainSubstring("Results are not being sent for submission"))
				})

				It("Should return an error if the submitter is unable to submit", func() {
					c := CheckConfig{
						SubmitResults: true,
					}

					submissionError := "unable to submit"

					err := RunPreflight(testcontext, func(ctx context.Context) (certification.Results, error) {
						return certification.Results{
							TestedImage:   "testSubmission",
							PassedOverall: true,
							Passed: []certification.Result{
								{
									Check: check.NewGenericCheck(
										"testSubmission",
										func(ctx context.Context, ir image.ImageReference) (bool, error) { return true, nil },
										check.Metadata{},
										check.HelpText{},
									),
									ElapsedTime: 1,
								},
							},
							Failed: []certification.Result{},
							Errors: []certification.Result{},
						}, nil
					}, c, testFormatter, &runtime.ResultWriterFile{}, &badResultSubmitter{submissionError}, "prod")
					Expect(err).To(HaveOccurred())
					Expect(err.Error()).To(ContainSubstring(submissionError))
				})
			})
		})
	})
})

var _ = Describe("JUnit", func() {
	var results *certification.Results
	var junitfile string
	var artifactWriter *artifacts.FilesystemWriter
	var testcontext context.Context

	BeforeEach(func() {
		tmpDir, err := os.MkdirTemp("", "junit-*")
		Expect(err).ToNot(HaveOccurred())
		artifactWriter, err = artifacts.NewFilesystemWriter(artifacts.WithDirectory(tmpDir))
		Expect(err).ToNot(HaveOccurred())
		testcontext = artifacts.ContextWithWriter(context.Background(), artifactWriter)
		DeferCleanup(os.RemoveAll, tmpDir)

		results = &certification.Results{
			TestedImage:       "registry.example.com/example/image:0.0.1",
			PassedOverall:     true,
			TestedOn:          runtime.UnknownOpenshiftClusterVersion(),
			CertificationHash: "sha256:deadb33f",
			Passed:            []certification.Result{},
			Failed:            []certification.Result{},
			Errors:            []certification.Result{},
		}
		junitfile = filepath.Join(artifactWriter.Path(), "results-junit.xml")
	})

	When("The additional JUnitXML results file is requested", func() {
		It("should be written to the artifacts directory without error", func() {
			Expect(writeJUnit(testcontext, *results)).To(Succeed())
			_, err := os.Stat(junitfile)
			Expect(err).ToNot(HaveOccurred())
		})
	})
})

var _ = DescribeTable("Checking overall pass/fail",
	func(result bool, expected string) {
		Expect(convertPassedOverall(result)).To(Equal(expected))
	},
	Entry("when passing true", true, "PASSED"),
	Entry("when passing false", false, "FAILED"),
)
