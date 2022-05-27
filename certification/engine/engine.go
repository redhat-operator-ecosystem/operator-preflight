// Package engine contains the interfaces necessary to implement policy execution.
package engine

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/redhat-openshift-ecosystem/openshift-preflight/certification"
	internal "github.com/redhat-openshift-ecosystem/openshift-preflight/certification/internal/engine"
	containerpol "github.com/redhat-openshift-ecosystem/openshift-preflight/certification/internal/policy/container"
	operatorpol "github.com/redhat-openshift-ecosystem/openshift-preflight/certification/internal/policy/operator"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/certification/policy"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/certification/pyxis"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/certification/runtime"
)

// CheckEngine defines the functionality necessary to run all checks for a policy,
// and return the results of that check execution.
type CheckEngine interface {
	// ExecuteChecks should execute all checks in a policy and internally
	// store the results. Errors returned by ExecuteChecks should reflect
	// errors in pre-validation tasks, and not errors in individual check
	// execution itself.
	ExecuteChecks(context.Context) error
	// Results returns the outcome of executing all checks.
	Results(context.Context) runtime.Results
}

func NewForConfig(ctx context.Context, cfg certification.Config) (CheckEngine, error) {
	checks, err := initializeChecks(ctx, cfg.Policy(), cfg)
	if err != nil {
		return nil, fmt.Errorf("error initializing checks: %v", err)
	}

	engine := &internal.CraneEngine{
		Config:    cfg,
		Image:     cfg.Image(),
		Checks:    checks,
		IsBundle:  cfg.IsBundle(),
		IsScratch: cfg.IsScratch(),
	}

	return engine, nil
}

// initializeChecks configures checks for a given policy p using cfg as needed.
func initializeChecks(ctx context.Context, p policy.Policy, cfg certification.Config) ([]certification.Check, error) {
	switch p {
	case policy.PolicyOperator:
		return []certification.Check{
			operatorpol.NewScorecardBasicSpecCheck(internal.NewOperatorSdkEngine(cfg.ScorecardImage()), cfg.Namespace(), cfg.ServiceAccount(), cfg.Kubeconfig(), cfg.ScorecardWaitTime()),
			operatorpol.NewScorecardOlmSuiteCheck(internal.NewOperatorSdkEngine(cfg.ScorecardImage()), cfg.Namespace(), cfg.ServiceAccount(), cfg.Kubeconfig(), cfg.ScorecardWaitTime()),
			operatorpol.NewDeployableByOlmCheck(internal.NewOperatorSdkEngine(cfg.ScorecardImage()), cfg.IndexImage(), cfg.DockerConfig(), cfg.Channel()),
			operatorpol.NewValidateOperatorBundleCheck(internal.NewOperatorSdkEngine(cfg.ScorecardImage())),
		}, nil
	case policy.PolicyContainer:
		return []certification.Check{
			&containerpol.HasLicenseCheck{},
			containerpol.NewHasUniqueTagCheck(cfg.DockerConfig()),
			&containerpol.MaxLayersCheck{},
			&containerpol.HasNoProhibitedPackagesCheck{},
			&containerpol.HasRequiredLabelsCheck{},
			&containerpol.RunAsNonRootCheck{},
			&containerpol.HasModifiedFilesCheck{},
			containerpol.NewBasedOnUbiCheck(pyxis.NewPyxisClient(
				certification.DefaultPyxisHost,
				cfg.PyxisAPIToken(),
				cfg.CertificationProjectID(),
				&http.Client{Timeout: 60 * time.Second})),
		}, nil
	case policy.PolicyRoot:
		return []certification.Check{
			&containerpol.HasLicenseCheck{},
			containerpol.NewHasUniqueTagCheck(cfg.DockerConfig()),
			&containerpol.MaxLayersCheck{},
			&containerpol.HasNoProhibitedPackagesCheck{},
			&containerpol.HasRequiredLabelsCheck{},
			&containerpol.HasModifiedFilesCheck{},
		}, nil
	case policy.PolicyScratch:
		return []certification.Check{
			&containerpol.HasLicenseCheck{},
			containerpol.NewHasUniqueTagCheck(cfg.DockerConfig()),
			&containerpol.MaxLayersCheck{},
			&containerpol.HasRequiredLabelsCheck{},
			&containerpol.RunAsNonRootCheck{},
		}, nil
	}

	return nil, fmt.Errorf("provided policy %s is unknown", p)
}

// makeCheckList returns a list of check names.
func makeCheckList(checks []certification.Check) []string {
	checkNames := make([]string, len(checks))

	for i, check := range checks {
		checkNames[i] = check.Name()
	}

	return checkNames
}

// checkNamesFor produces a slice of names for checks in the requested policy.
func checkNamesFor(p policy.Policy) []string {
	// stub the config. We don't technically need the policy here, but why not.
	c := &runtime.Config{Policy: p}
	checks, _ := initializeChecks(context.TODO(), p, c.ReadOnly())
	return makeCheckList(checks)
}

// OperatorPolicy returns the names of checks in the operator policy.
func OperatorPolicy() []string {
	return checkNamesFor(policy.PolicyOperator)
}

// ContainerPolicy returns the names of checks in the container policy.
func ContainerPolicy() []string {
	return checkNamesFor(policy.PolicyContainer)
}

// ScratchContainerPolicy returns the names of checks in the
// container policy with scratch exception.
func ScratchContainerPolicy() []string {
	return checkNamesFor(policy.PolicyScratch)
}

// RootExceptionContainerPolicy returns the names of checks in the
// container policy with root exception.
func RootExceptionContainerPolicy() []string {
	return checkNamesFor(policy.PolicyRoot)
}
