package cmd

import (
	"os"

	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/lib"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("cmd package check command", func() {
	BeforeEach(createAndCleanupDirForArtifactsAndLogs)

	Describe("Test Connect URL builders", func() {
		var (
			projectID = "this-is-my-project-id"
			imageID   = "my-image-id"
			resultsID = "my-results-id"
		)
		Context("Regular Connect URL", func() {
			It("should return a URL with just a project ID", func() {
				expected := "https://connect.redhat.com/component/view/this-is-my-project-id"
				actual := lib.BuildConnectURL(projectID, "prod")
				Expect(expected).To(Equal(actual))
			})
		})
		Context("QA Connect URL", func() {
			It("should return a URL for QA", func() {
				expected := "https://connect.qa.redhat.com/component/view/this-is-my-project-id"
				actual := lib.BuildConnectURL(projectID, "qa")
				Expect(expected).To(Equal(actual))
			})
		})
		Context("UAT Test Results URL", func() {
			BeforeEach(func() {
				os.Setenv("PFLT_PYXIS_ENV", "uat")
			})
			It("should return a URL for UAT", func() {
				expected := "https://connect.uat.redhat.com/component/view/this-is-my-project-id/certification/test-results/my-results-id"
				actual := lib.BuildTestResultsURL(projectID, resultsID, "uat")
				Expect(expected).To(Equal(actual))
			})
		})
		Context("QA Images URL", func() {
			BeforeEach(func() {
				os.Setenv("PFLT_PYXIS_ENV", "qa")
			})
			It("should return a URL for QA", func() {
				expected := "https://connect.qa.redhat.com/component/view/this-is-my-project-id/images"
				actual := lib.BuildImagesURL(projectID, "qa")
				Expect(expected).To(Equal(actual))
			})
		})
		Context("Override Pyxis Host", func() {
			BeforeEach(func() {
				os.Setenv("PFLT_PYXIS_HOST", "my.pyxis.host/some/path")
			})
			It("should return a Prod Images URL", func() {
				expected := "https://connect.redhat.com/component/view/this-is-my-project-id/images"
				actual := lib.BuildImagesURL(projectID, "prod")
				Expect(expected).To(Equal(actual))
			})
			It("should return a Prod Test Results URL", func() {
				expected := "https://connect.redhat.com/component/view/this-is-my-project-id/certification/test-results/my-results-id"
				actual := lib.BuildTestResultsURL(projectID, resultsID, "prod")
				Expect(expected).To(Equal(actual))
			})
			It("should return a Prod Vulnerabilities URL", func() {
				expected := "https://connect.redhat.com/component/view/this-is-my-project-id/security/vulnerabilities/my-image-id"
				actual := lib.BuildVulnerabilitiesURL(projectID, imageID, "prod")
				Expect(expected).To(Equal(actual))
			})
		})
	})
})
