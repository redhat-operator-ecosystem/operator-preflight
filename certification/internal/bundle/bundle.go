package bundle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/blang/semver"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/certification/internal/cli"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

const ocpVerV1beta1Unsupported = "4.9"

// versionsKey is the OpenShift versions in annotations.yaml that lists the versions allowed for an operator
const versionsKey = "com.redhat.openshift.versions"

func Validate(ctx context.Context, engine cli.OperatorSdkEngine, imagePath string) (*cli.OperatorSdkBundleValidateReport, error) {
	selector := []string{"community", "operatorhub"}
	opts := cli.OperatorSdkBundleValidateOptions{
		Selector:        selector,
		Verbose:         true,
		ContainerEngine: "none",
		OutputFormat:    "json-alpha1",
	}

	log.Trace("reading annotations file from the bundle")
	log.Debug("image extraction directory is ", imagePath)
	// retrieve the operator metadata from bundle image
	annotationsFileName := filepath.Join(imagePath, "metadata", "annotations.yaml")
	annotationsFile, err := os.Open(annotationsFileName)
	if err != nil {
		return nil, fmt.Errorf("could not open annotations.yaml: %v", err)
	}
	annotations, err := GetAnnotations(ctx, annotationsFile)
	if err != nil {
		return nil, fmt.Errorf("unable to get annotations.yaml from the bundle: %v", err)
	}

	if versions, ok := annotations[versionsKey]; ok {
		// Check that the label range contains >= 4.9
		if isTarget49OrGreater(versions) {
			log.Debug("OpenShift 4.9 detected in annotations. Running with additional checks enabled.")
			opts.OptionalValues = make(map[string]string)
			opts.OptionalValues["k8s-version"] = "1.22"
		}
	}

	return engine.BundleValidate(imagePath, opts)
}

func isTarget49OrGreater(ocpLabelIndex string) bool {
	semVerOCPV1beta1Unsupported, _ := semver.ParseTolerant(ocpVerV1beta1Unsupported)
	// the OCP range informed cannot allow carry on to OCP 4.9+
	beginsEqual := strings.HasPrefix(ocpLabelIndex, "=")
	// It means that the OCP label is =OCP version
	if beginsEqual {
		version := cleanStringToGetTheVersionToParse(strings.Split(ocpLabelIndex, "=")[1])
		verParsed, err := semver.ParseTolerant(version)
		if err != nil {
			log.Errorf("unable to parse the value (%s) on (%s)", version, ocpLabelIndex)
			return false
		}

		if verParsed.GE(semVerOCPV1beta1Unsupported) {
			return true
		}
		return false
	}
	indexRange := cleanStringToGetTheVersionToParse(ocpLabelIndex)
	if len(indexRange) > 1 {
		// Bare version
		if !strings.Contains(indexRange, "-") {
			verParsed, err := semver.ParseTolerant(indexRange)
			if err != nil {
				log.Error("unable to parse the version")
				return false
			}
			if verParsed.GE(semVerOCPV1beta1Unsupported) {
				return true
			}
		}

		versions := strings.Split(indexRange, "-")
		version := versions[0]
		if len(versions) > 1 {
			version = versions[1]
			verParsed, err := semver.ParseTolerant(version)
			if err != nil {
				log.Error("unable to parse the version")
				return false
			}

			if verParsed.GE(semVerOCPV1beta1Unsupported) {
				return true
			}
			return false
		}

		verParsed, err := semver.ParseTolerant(version)
		if err != nil {
			log.Error("unable to parse the version")
			return false
		}

		if semVerOCPV1beta1Unsupported.GE(verParsed) {
			return true
		}
	}
	return false
}

// cleanStringToGetTheVersionToParse will remove the expected characters for
// we are able to parse the version informed.
func cleanStringToGetTheVersionToParse(value string) string {
	doubleQuote := "\""
	singleQuote := "'"
	value = strings.ReplaceAll(value, singleQuote, "")
	value = strings.ReplaceAll(value, doubleQuote, "")
	value = strings.ReplaceAll(value, "v", "")
	return value
}

// GetAnnotations accepts a context, and an io.Reader that is expected to provide
// the annotations.yaml, and parses the annotations from there
func GetAnnotations(ctx context.Context, r io.Reader) (map[string]string, error) {
	fileContents, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("fail to read metadata/annotation.yaml file in bundle: %v", err)
	}

	annotations, err := ExtractAnnotationsBytes(ctx, fileContents)
	if err != nil {
		return nil, fmt.Errorf("metadata/annotations.yaml found but is malformed: %v", err)
	}

	return annotations, nil
}

// extractAnnotationsBytes reads the annotation data read from a file and returns the expected format for that yaml
// represented as a map[string]string.
func ExtractAnnotationsBytes(ctx context.Context, annotationBytes []byte) (map[string]string, error) {
	type metadata struct {
		Annotations map[string]string
	}

	if len(annotationBytes) == 0 {
		return nil, errors.New("the annotations file was empty")
	}

	var bundleMeta metadata
	if err := yaml.Unmarshal(annotationBytes, &bundleMeta); err != nil {
		return nil, fmt.Errorf("metadata/annotations.yaml found but is malformed: %v", err)
	}

	return bundleMeta.Annotations, nil
}

func GetCsvFilePathFromBundle(mountedDir string) (string, error) {
	log.Trace("reading clusterserviceversion file from the bundle")
	log.Debug("mounted directory is ", mountedDir)
	matches, err := filepath.Glob(filepath.Join(mountedDir, "manifests", "*.clusterserviceversion.yaml"))
	if err != nil {
		return "", fmt.Errorf("glob pattern is malformed: %v", err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("unable to find clusterserviceversion file in the bundle image: %v", os.ErrNotExist)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("more than one CSV file detected in bundle")
	}
	log.Debugf("The path to csv file is %s", matches[0])
	return matches[0], nil
}

func GetSupportedInstallModes(ctx context.Context, csvReader io.Reader) (map[string]bool, error) {
	var csv ClusterServiceVersion
	bts, err := io.ReadAll(csvReader)
	if err != nil {
		return nil, fmt.Errorf("could not get CSV from reader: %v", err)
	}
	err = yaml.Unmarshal(bts, &csv)
	if err != nil {
		return nil, fmt.Errorf("malformed CSV detected: %v", err)
	}

	var installedModes map[string]bool = make(map[string]bool, len(csv.Spec.InstallModes))
	for _, v := range csv.Spec.InstallModes {
		if v.Supported {
			installedModes[v.Type] = true
		}
	}
	return installedModes, nil
}

type ClusterServiceVersion struct {
	Spec ClusterServiceVersionSpec `yaml:"spec"`
}

type ClusterServiceVersionSpec struct {
	// InstallModes specify supported installation types
	InstallModes []InstallMode `yaml:"installModes,omitempty"`
}

type InstallMode struct {
	Type      string `yaml:"type"`
	Supported bool   `yaml:"supported"`
}
