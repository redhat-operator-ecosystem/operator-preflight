package lib

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/redhat-openshift-ecosystem/openshift-preflight/artifacts"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/check"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/log"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/policy"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/pyxis"

	"github.com/go-logr/logr"
)

// ResultWriter defines methods associated with writing check results.
type ResultWriter interface {
	OpenFile(name string) (io.WriteCloser, error)
	io.WriteCloser
}

// ResultSubmitter defines methods associated with submitting results to Red HAt.
type ResultSubmitter interface {
	Submit(context.Context, string) error
}

// PyxisClient defines pyxis API interactions that are relevant to check executions in cmd.
type PyxisClient interface {
	FindImagesByDigest(ctx context.Context, digests []string) ([]pyxis.CertImage, error)
	GetProject(context.Context) (*pyxis.CertProject, error)
	SubmitResults(context.Context, *pyxis.CertificationInput) (*pyxis.CertificationResults, error)
}

// NewPyxisClient initializes a pyxisClient with relevant information from cfg.
// If the the CertificationProjectID, PyxisAPIToken, or PyxisHost are empty, then nil is returned.
// Callers should treat a nil pyxis client as an indicator that pyxis calls should not be made.
func NewPyxisClient(ctx context.Context, projectID, token, host string) PyxisClient {
	if projectID == "" || token == "" || host == "" {
		return nil
	}

	return pyxis.NewPyxisClient(
		host,
		token,
		projectID,
		&http.Client{Timeout: 60 * time.Second},
	)
}

// ContainerCertificationSubmitter submits container results to Pyxis, and implements
// a ResultSubmitter.
type ContainerCertificationSubmitter struct {
	CertificationProjectID string
	Pyxis                  PyxisClient
	DockerConfig           string
	PreflightLogFile       string
}

func (s *ContainerCertificationSubmitter) Submit(ctx context.Context, pyxisEnv string) error {
	logger := logr.FromContextOrDiscard(ctx)
	logger.Info("preparing results that will be submitted to Red Hat")

	// get the project info from pyxis
	certProject, err := s.Pyxis.GetProject(ctx)
	if err != nil {
		return fmt.Errorf("could not retrieve project: %w", err)
	}

	// Ensure that a certProject was returned. In theory we would expect pyxis
	// to throw an error if no project is returned, but in the event that it doesn't
	// we need to confirm before we proceed in order to prevent a runtime panic
	// setting the DockerConfigJSON below.
	if certProject == nil {
		return fmt.Errorf("no certification project was returned from pyxis")
	}

	logger.V(log.TRC).Info("certification project id", "project", certProject)

	// only read the dockerfile if the user provides a location for the file
	// at this point in the flow, if `cfg.DockerConfig` is empty we know the repo is public and can continue the submission flow
	if s.DockerConfig != "" {
		dockerConfigJSONBytes, err := os.ReadFile(s.DockerConfig)
		if err != nil {
			return fmt.Errorf("could not open file for submission: %s: %w",
				s.DockerConfig,
				err,
			)
		}

		certProject.Container.DockerConfigJSON = string(dockerConfigJSONBytes)
	}

	// the below code is for the edge case where a partner has a DockerConfig in pyxis, but does not send one to preflight.
	// when we call pyxis's GetProject API, we get back the DockerConfig as a PGP encrypted string and not JSON,
	// if we were to send what pyixs just sent us in a update call, pyxis would throw a validation error saying it's not valid json
	// the below code aims to set the DockerConfigJSON to an empty string, and since this field is `omitempty` when we marshall it
	// we will not get a validation error
	if s.DockerConfig == "" {
		certProject.Container.DockerConfigJSON = ""
	}

	// no longer set DockerConfigJSON for registries which Red Hat hosts, this prevents the user from sending an invalid
	// docker file that systems like clair and registry-proxy cannot use to pull the image
	if certProject.Container.HostedRegistry {
		certProject.Container.DockerConfigJSON = ""
	}

	// We need to get the artifact writer to know where our artifacts were written. We also need the
	// Filesystem Writer here to make sure we can get the configured path.
	// TODO: This needs to be rethought. Submission is not currently in scope for library implementations
	// but the current implementation of this makes it impossible because the MapWriter would obviously
	// not work here.
	artifactWriter, ok := artifacts.WriterFromContext(ctx).(*artifacts.FilesystemWriter)
	if artifactWriter == nil || !ok {
		return errors.New("the artifact writer was either missing or was not supported, so results cannot be submitted")
	}

	certImage, err := os.Open(path.Join(artifactWriter.Path(), check.DefaultCertImageFilename))
	if err != nil {
		return fmt.Errorf("could not open file for submission: %s: %w",
			check.DefaultCertImageFilename,
			err,
		)
	}
	defer certImage.Close()

	preflightResults, err := os.Open(path.Join(artifactWriter.Path(), check.DefaultTestResultsFilename))
	if err != nil {
		return fmt.Errorf(
			"could not open file for submission: %s: %w",
			check.DefaultTestResultsFilename,
			err,
		)
	}
	defer preflightResults.Close()

	logfile, err := os.Open(s.PreflightLogFile)
	if err != nil {
		return fmt.Errorf(
			"could not open file for submission: %s: %w",
			s.PreflightLogFile,
			err,
		)
	}
	defer logfile.Close()

	options := []pyxis.CertificationInputOption{
		pyxis.WithCertImage(certImage),
		pyxis.WithPreflightResults(preflightResults),
		pyxis.WithArtifact(logfile, filepath.Base(s.PreflightLogFile)),
	}

	pol := policy.PolicyContainer

	if certProject.ScratchProject() {
		pol = policy.PolicyScratchNonRoot
	}

	// only read the rpm manifest file off of disk if the policy executed is not scratch
	// scratch images do not have rpm manifests, the rpm-manifest.json file is not written to disk by the engine during execution
	if pol != policy.PolicyScratchNonRoot {
		rpmManifest, err := os.Open(path.Join(artifactWriter.Path(), check.DefaultRPMManifestFilename))
		if err != nil {
			return fmt.Errorf(
				"could not open file for submission: %s: %w",
				check.DefaultRPMManifestFilename,
				err,
			)
		}
		defer rpmManifest.Close()

		options = append(options, pyxis.WithRPMManifest(rpmManifest))
	}

	submission, err := pyxis.NewCertificationInput(ctx, certProject, options...)
	if err != nil {
		return fmt.Errorf("unable to finalize data that would be sent to pyxis: %w", err)
	}

	certResults, err := s.Pyxis.SubmitResults(ctx, submission)
	if err != nil {
		return fmt.Errorf("could not submit to pyxis: %w", err)
	}

	logger.Info("Test results have been submitted to Red Hat.")
	logger.Info("These results will be reviewed by Red Hat for final certification.")
	logger.Info(fmt.Sprintf("The container's image id is: %s.", certResults.CertImage.ID))
	logger.Info(fmt.Sprintf("Please check %s to view test results.", BuildTestResultsURL(s.CertificationProjectID, certResults.TestResults.ID, pyxisEnv)))
	logger.Info(fmt.Sprintf("Please check %s to view security vulnerabilities.", BuildVulnerabilitiesURL(s.CertificationProjectID, certResults.CertImage.ID, pyxisEnv)))
	logger.Info(fmt.Sprintf("Please check %s to monitor the progress.", BuildImagesURL(s.CertificationProjectID, pyxisEnv)))

	return nil
}

// NoopSubmitter is a no-op ResultSubmitter that optionally logs a message
// and a reason as to why results were not submitted.
type NoopSubmitter struct {
	emitLog bool
	reason  string
	log     *logr.Logger
}

func NewNoopSubmitter(emitLog bool, log *logr.Logger) *NoopSubmitter {
	return &NoopSubmitter{
		emitLog: emitLog,
		log:     log,
	}
}

var _ ResultSubmitter = &NoopSubmitter{}

func (s *NoopSubmitter) Submit(ctx context.Context, _ string) error {
	if s.emitLog {
		msg := "Results are not being sent for submission."
		if s.reason != "" {
			msg = fmt.Sprintf("%s Reason: %s.", msg, s.reason)
		}

		s.log.Info(msg)
	}

	return nil
}

func (s *NoopSubmitter) SetEmitLog(emitLog bool) {
	s.emitLog = emitLog
}

func (s *NoopSubmitter) SetReason(reason string) {
	s.reason = reason
}

func BuildConnectURL(projectID, pyxisEnv string) string {
	connectURL := fmt.Sprintf("https://connect.redhat.com/component/view/%s", projectID)

	if len(pyxisEnv) > 0 && pyxisEnv != "prod" {
		connectURL = fmt.Sprintf("https://connect.%s.redhat.com/component/view/%s", pyxisEnv, projectID)
	}

	return connectURL
}

func BuildImagesURL(projectID, pyxisEnv string) string {
	return fmt.Sprintf("%s/images", BuildConnectURL(projectID, pyxisEnv))
}

func BuildTestResultsURL(projectID, testResultsID, pyxisEnv string) string {
	return fmt.Sprintf("%s/certification/test-results/%s", BuildConnectURL(projectID, pyxisEnv), testResultsID)
}

func BuildVulnerabilitiesURL(projectID, imageID, pyxisEnv string) string {
	return fmt.Sprintf("%s/security/vulnerabilities/%s", BuildConnectURL(projectID, pyxisEnv), imageID)
}
