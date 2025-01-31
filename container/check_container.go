package container

import (
	"context"
	"fmt"
	"net/http"
	goruntime "runtime"
	"time"

	"github.com/redhat-openshift-ecosystem/openshift-preflight/certification"
	preflighterr "github.com/redhat-openshift-ecosystem/openshift-preflight/errors"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/check"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/engine"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/lib"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/policy"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/pyxis"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/runtime"
)

type Option = func(*containerCheck)

// NewCheck is a check that runs preflight's Container Policy.
func NewCheck(image string, opts ...Option) *containerCheck {
	c := &containerCheck{
		image:     image,
		pyxisHost: check.DefaultPyxisHost,
		platform:  goruntime.GOARCH,
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// Run executes the check and returns the results. Policy exceptions will be resolved if the proper
// pyxis information is provided. Calls should add a relevant ArtifactWriter to the context if they
// wish to work with artifact files written by checks.
func (c *containerCheck) Run(ctx context.Context) (certification.Results, error) {
	err := c.resolve(ctx)
	if err != nil {
		return certification.Results{}, err
	}

	cfg := runtime.Config{
		Image:              c.image,
		DockerConfig:       c.dockerconfigjson,
		Scratch:            c.policy == policy.PolicyScratchNonRoot || c.policy == policy.PolicyScratchRoot,
		Bundle:             false,
		Insecure:           c.insecure,
		Platform:           c.platform,
		ManifestListDigest: c.manifestListDigest,
	}
	eng, err := engine.New(ctx, c.checks, nil, cfg)
	if err != nil {
		return certification.Results{}, err
	}

	if err := eng.ExecuteChecks(ctx); err != nil {
		return certification.Results{}, err
	}

	return eng.Results(ctx), nil
}

func (c *containerCheck) resolve(ctx context.Context) error {
	if c.resolved {
		return nil
	}

	if c.image == "" {
		return preflighterr.ErrImageEmpty
	}

	c.policy = policy.PolicyContainer

	// If we have enough Pyxis information, resolve the policy.
	if c.hasPyxisData() {
		p := pyxis.NewPyxisClient(
			c.pyxisHost,
			c.pyxisToken,
			c.certificationProjectID,
			&http.Client{Timeout: 60 * time.Second},
		)

		override, err := lib.GetContainerPolicyExceptions(ctx, p)
		if err != nil {
			return fmt.Errorf("%w: %s", preflighterr.ErrCannotResolvePolicyException, err)
		}

		c.policy = override
	}

	newChecks, err := engine.InitializeContainerChecks(ctx, c.policy, engine.ContainerCheckConfig{
		DockerConfig:           c.dockerconfigjson,
		PyxisAPIToken:          c.pyxisToken,
		CertificationProjectID: c.certificationProjectID,
		PyxisHost:              c.pyxisHost,
	})
	if err != nil {
		return fmt.Errorf("%w: %s", preflighterr.ErrCannotInitializeChecks, err)
	}
	c.checks = newChecks
	c.resolved = true

	return nil
}

// List the available container checks. Policy exceptions will be resolved if the proper
// pyxis information is provided.
func (c *containerCheck) List(ctx context.Context) (policy.Policy, []check.Check, error) {
	return c.policy, c.checks, c.resolve(ctx)
}

// hasPyxisData returns true of the values necessary to make a pyxis
// API call are not empty. This does not check the validity of the input values.
func (c *containerCheck) hasPyxisData() bool {
	return (c.certificationProjectID != "" && c.pyxisToken != "" && c.pyxisHost != "")
}

func WithDockerConfigJSONFromFile(s string) Option {
	return func(cc *containerCheck) {
		cc.dockerconfigjson = s
	}
}

// DEPRECATED: Use WithCertificationProject instead
// WithCertificationProject adds the project's id and pyxis token to the check
// allowing for the project's metadata to change the certification (if necessary).
// An example might be the Scratch or Privileged flags on a project allowing for
// the corresponding policy to be executed.
func WithCertificationProject(id, token string) Option {
	return withCertificationProject(id, token)
}

// WithCertificationComponent adds the component's id and pyxis token to the check
// allowing for the copmonent's metadata to change the certification (if necessary).
// An example might be the Scratch or Privileged flags on a project allowing for
// the corresponding policy to be executed.
func WithCertificationComponent(id, token string) Option {
	return withCertificationProject(id, token)
}

func withCertificationProject(id, token string) Option {
	return func(cc *containerCheck) {
		cc.pyxisToken = token
		cc.certificationProjectID = id
	}
}

// WithPyxisHost explicitly sets the pyxis host for pyxis interactions.
// Useful for debugging or testing against a very specific pyxis endpoint.
// Most callers should use the "WithPyxisEnv" option instead.
func WithPyxisHost(host string) Option {
	return func(cc *containerCheck) {
		cc.pyxisHost = host
	}
}

// WithPyxisEnv will set the pyxis host for interactions and submission based
// on the provided value of env. If the selected env is unknown, prod is used.
// Choose from [prod, uat, qa, stage].
func WithPyxisEnv(env string) Option {
	return func(cc *containerCheck) {
		cc.pyxisHost = runtime.PyxisHostLookup(env, "")
	}
}

// WithPlatform will define for what platform the image should be pulled.
// E.g. amd64, s390x.
func WithPlatform(platform string) Option {
	return func(cc *containerCheck) {
		cc.platform = platform
	}
}

// WithInsecureConnection allows for preflight to connect to an insecure registry
// to pull images.
func WithInsecureConnection() Option {
	return func(cc *containerCheck) {
		cc.insecure = true
	}
}

// WithManifestListDigest signifies that we have a manifest list and should add
// this digest to any Pyxis calls.
// This is only valid when submitting to Pyxis. Otherwise, it will be ignored.
func WithManifestListDigest(manifestListDigest string) Option {
	return func(cc *containerCheck) {
		cc.manifestListDigest = manifestListDigest
	}
}

type containerCheck struct {
	image                  string
	dockerconfigjson       string
	certificationProjectID string
	pyxisToken             string
	pyxisHost              string
	platform               string
	insecure               bool
	manifestListDigest     string
	checks                 []check.Check
	resolved               bool
	policy                 policy.Policy
}
