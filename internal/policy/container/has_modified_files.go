package container

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/check"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/image"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/log"
	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/rpm"

	"github.com/go-logr/logr"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	rpmdb "github.com/knqyf263/go-rpmdb/pkg"
	"github.com/spf13/afero"
)

var _ check.Check = &HasModifiedFilesCheck{}

// HasModifiedFilesCheck evaluates that no files from the base layer have been modified by
// subsequent layers by comparing the file list installed by Packages against the file list
// modified in subsequent layers.
type HasModifiedFilesCheck struct{}

const whiteoutPrefix = ".wh."

type packageMeta struct {
	Name        string
	Version     string
	Release     string
	Arch        string
	Vendor      string
	InstallTime int
}

func (pm packageMeta) Compare(other packageMeta) int {
	return 0
}

type packageFilesRef struct {
	// LayerFiles contains a slice of files created/modified in layer
	LayerFiles map[string]fileInfo
	// LayerPackages is a map of the packages in a layer. The key is
	// the NVR of the package. The value is metadata about the package
	// that we use for processing
	LayerPackages map[string]packageMeta
	// LayerPackageFiles maps files to a package name-version-release
	LayerPackageFiles map[string]string
	HasRPMDB          bool
}

// Validate runs the check of whether any Red Hat files were modified
func (p *HasModifiedFilesCheck) Validate(ctx context.Context, imgRef image.ImageReference) (bool, error) {
	fs := afero.NewOsFs()
	layerIDs, packageFiles, err := p.gatherDataToValidate(ctx, imgRef, fs)
	if err != nil {
		return false, fmt.Errorf("could not generate modified files list: %v", err)
	}

	packageDist, err := p.parsePackageDist(ctx, imgRef.ImageFSPath, fs)
	if err != nil {
		return false, fmt.Errorf("could not generate modified files list: %v", err)
	}

	return p.validate(ctx, layerIDs, packageFiles, packageDist)
}

// parsePackageDist returns the platform's distribution value from the
// os-release contents in the extracted image.
func (p *HasModifiedFilesCheck) parsePackageDist(_ context.Context, extractedImageFSPath string, fs afero.Fs) (string, error) {
	osRelease, err := fs.Open(filepath.Join(extractedImageFSPath, "etc", "os-release"))
	if err != nil {
		return "", fmt.Errorf("could not open os-release: %v", err)
	}
	defer osRelease.Close()
	scanner := bufio.NewScanner(osRelease)
	packageDist := "unknown"
	for scanner.Scan() {
		line := scanner.Text()
		r, _ := regexp.Compile(`PLATFORM_ID="platform:([[:alnum:]]+)"`)
		m := r.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		packageDist = m[1]
		break
	}

	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error while scanning for package dist: %v", err)
	}

	return packageDist, nil
}

// gatherDataToValidate returns a map from layer digest to a struct containing the list of files
// (packageFilesRef.LayerPackageFiles) installed via packages (packageFilesRef.LayerPackages)
// from the container image, and the list of files (packageFilesRef.LayerFiles) modified/added
// via layers in the image.
func (p *HasModifiedFilesCheck) gatherDataToValidate(ctx context.Context, imgRef image.ImageReference, fs afero.Fs) ([]string, map[string]packageFilesRef, error) {
	logger := logr.FromContextOrDiscard(ctx)

	layerDir, err := afero.TempDir(fs, "", "rpm-layers-")
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		_ = fs.RemoveAll(layerDir)
	}()

	if imgRef.ImageInfo == nil {
		return nil, nil, fmt.Errorf("image reference invalid")
	}

	layers, err := imgRef.ImageInfo.Layers()
	if err != nil {
		return nil, nil, err
	}

	layerIDs := make([]string, 0, len(layers))
	layerRefs := make(map[string]packageFilesRef, len(layers))

	// Uncompress each layer and build maps containing the packages,
	// the package files, and the files modified by each layer
	// Also generate a list of the layerIDs so we can keep the
	// order within the maps.
	for idx, layer := range layers {
		layerIDHash, err := layer.Digest()
		if err != nil {
			return nil, nil, fmt.Errorf("unable to retrieve diff id for layer: %w", err)
		}

		// Capture the diff ID to aid in debugging. We don't technically care if
		// there's an error returned here because we don't use the layerDiffID
		// value for anything meaningful.
		layerDiffID := "unknown"
		layerDiffHash, err := layer.DiffID()
		if err == nil && layerDiffHash.String() != "" {
			layerDiffID = layerDiffHash.String()
		}

		rawLayerID := layerIDHash.String()
		// Map everything using a combination of the layer index and the layer
		// ID to avoid problems when images have multiple scattered layers with
		// the same ID.
		layerID := fmt.Sprintf("%02d-%s", idx, rawLayerID)
		logger.V(log.TRC).Info("generating unique layer ID", "uniqueLayerID", layerID, "layerID", rawLayerID, "layerDiffID", layerDiffID)

		layerDir := filepath.Join(layerDir, layerID)
		err = fs.Mkdir(layerDir, 0o755)
		if err != nil {
			return nil, nil, fmt.Errorf("could not create layer directory: %w", err)
		}

		layerIDs = append(layerIDs, layerID)

		files, err := generateChangesFor(ctx, layer)
		if err != nil {
			return nil, nil, err
		}

		found, pkgList := findRPMDB(ctx, layer)
		if !found {
			logger.V(log.TRC).Info("could not find rpm database in layer", "layer", layerID)
			if idx > 0 {
				// Just make this is the same as last layer, since the RPM db was not modified
				lastLayer := layerIDs[idx-1]
				layerRefs[layerID] = packageFilesRef{
					LayerFiles:        files,
					LayerPackages:     layerRefs[lastLayer].LayerPackages,
					LayerPackageFiles: layerRefs[lastLayer].LayerPackageFiles,
					HasRPMDB:          false,
				}
				continue
			}

			// If it's the first layer, just make the pkgList empty.
			pkgList = make([]*rpmdb.PackageInfo, 0)
		}

		pkgNameList := extractPackageNameVersionRelease(pkgList)

		packageFiles, err := installedFileMapWithExclusions(ctx, pkgList)
		if err != nil {
			return nil, nil, err
		}

		layerRefs[layerID] = packageFilesRef{
			LayerFiles:        files,
			LayerPackages:     pkgNameList,
			LayerPackageFiles: packageFiles,
			HasRPMDB:          true,
		}
	}

	return layerIDs, layerRefs, nil
}

// validate compares the list of LayerFiles and PackageFiles to see what PackageFiles
// have been modified within the additional layers. packageDist is the value we expect
// to find in the base package's Release field.
func (p *HasModifiedFilesCheck) validate(ctx context.Context, layerIDs []string, packageFiles map[string]packageFilesRef, packageDist string) (bool, error) {
	logger := logr.FromContextOrDiscard(ctx)

	disallowedModifications := false
	for idx, layerID := range layerIDs {
		logger := logger.WithValues("layer", layerID)
		ref := packageFiles[layerID]
		for modifiedFile, modifiedFileInfo := range ref.LayerFiles {
			logger := logger.WithValues("file", modifiedFile)

			// If it's a modifiedFile but this layer has an RPM db, that's allowed, but only if the
			// package itself is updated.
			if idx == 0 && ref.HasRPMDB {
				// Just skip these in the first layer
				continue
			}
			if _, found := ref.LayerPackageFiles[modifiedFile]; !found {
				// Far as we can tell, this isn't from an RPM
				continue
			}
			previousPackageVersion, prevFound := packageFiles[layerIDs[idx-1]].LayerPackageFiles[modifiedFile]
			if !prevFound && ref.HasRPMDB {
				// This is a net-new package file. Pass it.
				continue
			}
			currentPackageVersion := ref.LayerPackageFiles[modifiedFile]
			previousPackage := packageFiles[layerIDs[idx-1]].LayerPackages[previousPackageVersion]
			currentPackage := ref.LayerPackages[currentPackageVersion]

			if previousPackageVersion == currentPackageVersion {
				previousFileInfo := packageFiles[layerIDs[idx-1]].LayerFiles[modifiedFile]
				// Something in the mode changed. The only thing we support is removal of setuid/setgid bits
				setUIDRemoved := previousFileInfo.Mode&fs.ModeSetuid > 0 && modifiedFileInfo.Mode&fs.ModeSetuid == 0
				setGIDRemoved := previousFileInfo.Mode&fs.ModeSetgid > 0 && modifiedFileInfo.Mode&fs.ModeSetgid == 0

				if setUIDRemoved || setGIDRemoved {
					logger.V(log.DBG).Info("setuid/setgid bit removed")
					continue
				}

				if !strings.Contains(currentPackage.Release, packageDist) && packageDist != "unknown" {
					// This means it's _probably_ not a RH package. If the file is changed, warn, but don't fail
					logger.Info("WARN: an rpm-installed file was modified outside of rpm, but appears to be from a third-party. This could be a failure in the future")
					continue
				}

				if currentPackage.Vendor != "Red Hat, Inc." && previousPackage.Vendor != "Red Hat, Inc." {
					// This means it's _probably_ not a RH package. If the file is changed, warn, but don't fail
					logger.Info("WARN: an rpm-installed file was modified outside of rpm, but appears to be from a third-party. This could be a failure in the future")
					continue
				}

				if currentPackage.InstallTime > previousPackage.InstallTime {
					// This _probably_ means that the package was either:
					// a) explicitly rpm -e then rpm -i
					// b) dnf reinstall
					// This should not trigger. Going to trace log this, but not always report
					logger.V(log.TRC).Info("package appears to have been re-installed or removed and installed in the same layer", "package", currentPackage.Name)
					continue
				}

				// Nope, nope, nope. File was modified without using RPM
				logger.Info("found disallowed modification in layer", "file", modifiedFile)
				disallowedModifications = true
				continue
			}

			// Check that release contains the same arch, this is to ensure that a package did not get rebuilt with
			// a different architecture
			previousOsRelease := strings.Contains(previousPackage.Release, packageDist)
			currentOsRelease := strings.Contains(currentPackage.Release, packageDist)

			if previousOsRelease && !currentOsRelease {
				logger.Info("mismatch in OS release", "file", modifiedFile)
				disallowedModifications = true
				continue
			}

			// Check that the architectures for previous version and current version of a given package match
			if previousPackage.Arch != currentPackage.Arch {
				logger.Info("mismatch in package architecture", "file", modifiedFile)
				disallowedModifications = true
				continue
			}

			// This appears like an update. This is allowed.
			// No further action required
		}
	}
	return !disallowedModifications, nil
}

func (p HasModifiedFilesCheck) Name() string {
	return "HasModifiedFiles"
}

func (p HasModifiedFilesCheck) Help() check.HelpText {
	return check.HelpText{
		Message:    "Check HasModifiedFiles encountered an error. Please review the preflight.log file for more information.",
		Suggestion: "Do not modify any files installed by RPM in the base Red Hat layer",
	}
}

func (p HasModifiedFilesCheck) Metadata() check.Metadata {
	return check.Metadata{
		Description:      "Checks that no files installed via RPM in the base Red Hat layer have been modified",
		Level:            "best",
		KnowledgeBaseURL: certDocumentationURL,
		CheckURL:         certDocumentationURL,
	}
}

func extractPackageNameVersionRelease(pkgList []*rpmdb.PackageInfo) map[string]packageMeta {
	pkgNameList := make(map[string]packageMeta, len(pkgList))
	for _, pkg := range pkgList {
		pkgNameList[strings.Join([]string{pkg.Name, pkg.Version, pkg.Release, pkg.Arch}, "-")] = packageMeta{
			Name:        pkg.Name,
			Version:     pkg.Version,
			Release:     pkg.Release,
			Arch:        pkg.Arch,
			Vendor:      pkg.Vendor,
			InstallTime: pkg.InstallTime,
		}
	}
	return pkgNameList
}

// findRPMDB attempts to extract a valid RPMDB from layers in the order
// they are provided. If found is false, pkglist should
// be disregarded as any value there will be invalid.
func findRPMDB(ctx context.Context, layer v1.Layer) (found bool, pkglist []*rpmdb.PackageInfo) {
	logger := logr.FromContextOrDiscard(ctx)
	var err error
	pkglist, err = extractRPMDB(ctx, layer)
	if err == nil {
		id, _ := layer.Digest()
		logger.V(log.TRC).Info("findRPMDB found an RPM db", "layer", id.String())
		found = true
		return
	}

	return found, pkglist
}

// directoryIsExcluded excludes a directory and any file contained in that directory.
func directoryIsExcluded(ctx context.Context, s string) bool {
	excl := map[string]struct{}{
		"etc":               {},
		"var":               {},
		"run":               {},
		"usr/lib/.build-id": {},
		"usr/tmp":           {},
	}

	for k := range excl {
		if strings.HasPrefix(s, filepath.Clean(k+"/")) || k == s {
			logger := logr.FromContextOrDiscard(ctx)
			logger.V(log.TRC).Info("directory excluded", "directory", s)
			return true
		}
	}

	return false
}

// pathIsExcluded checks if s is excluded explicitly as written.
func pathIsExcluded(ctx context.Context, s string) bool {
	excl := map[string]struct{}{
		"etc/resolv.conf": {},
		"etc/hostname":    {},
		// etc and etc/ are both required as both can present the directory
		// in a tarball. Same goes for other directories.
		"etc":  {},
		"etc/": {},
		"run":  {},
		"run/": {},
	}

	_, found := excl[s]
	if found {
		logger := logr.FromContextOrDiscard(ctx)
		logger.V(log.TRC).Info("file excluded", "file", s)
		return true
	}
	return found
}

// prefixAndSuffixIsExcluded will check both start and end of path
func prefixAndSuffixIsExcluded(ctx context.Context, s string) bool {
	excl := []struct {
		Prefix string
		Suffix string
	}{
		{Prefix: "usr/", Suffix: ".cache"},
	}

	for _, v := range excl {
		if strings.HasPrefix(s, v.Prefix) && strings.HasSuffix(s, v.Suffix) {
			logger := logr.FromContextOrDiscard(ctx)
			logger.V(log.TRC).Info("prefix and suffix excluded", "filename", s, "prefix", v.Prefix, "suffix", v.Suffix)
			return true
		}
	}

	return false
}

// normalize will clean a filepath of extraneous characters like ./, //, etc.
// and strip a leading slash. E.g. /foo/../baz --> baz
func normalize(s string) string {
	// for the root path, return the root path.
	if s == "/" {
		return s
	}
	return filepath.Clean(strings.TrimPrefix(s, "/"))
}

// installedFileMapWithExclusions gets a map of installed filenames that have been cleaned
// of extra slashes, dotslashes, and leading slashes.
func installedFileMapWithExclusions(ctx context.Context, pkglist []*rpmdb.PackageInfo) (map[string]string, error) {
	const okFlags = rpmdb.RPMFILE_CONFIG |
		rpmdb.RPMFILE_DOC |
		rpmdb.RPMFILE_LICENSE |
		rpmdb.RPMFILE_MISSINGOK |
		rpmdb.RPMFILE_README |
		rpmdb.RPMFILE_ARTIFACT |
		rpmdb.RPMFILE_GHOST
	m := map[string]string{}
	for _, pkg := range pkglist {
		files, err := pkg.InstalledFiles()
		if err != nil {
			return m, err
		}

		// converting directories to a map so we can filter them out quicker
		pkgDirNamesMap := make(map[string]struct{})
		for _, dir := range pkg.DirNames {
			pkgDirNamesMap[dir] = struct{}{}
		}

		for _, file := range files {
			if _, found := pkgDirNamesMap[file.Path]; found {
				// The file is a directory. Skip it.
				continue
			}

			if int32(file.Flags)&okFlags > 0 {
				// It is one of the ok flags. Skip it.
				continue
			}

			normalized := normalize(file.Path)
			if pathIsExcluded(ctx, normalized) || directoryIsExcluded(ctx, normalized) || prefixAndSuffixIsExcluded(ctx, normalized) {
				// It is either an explicitly excluded path or directory. Skip it.
				continue
			}

			// checking to see if the file is already in the map.
			// check to see if all attributes of the rpm match except architecture.
			// this is to support cross architecture file ownership,
			// the 2nd architecture we encounter, we can skip it.
			if val, found := m[normalized]; found {
				s := strings.Split(val, "-")
				name, version, release, arch := s[0], s[1], s[2], s[3]

				if name == pkg.Name && version == pkg.Version && release == pkg.Release && arch != pkg.Arch {
					continue
				}
			}

			m[normalized] = strings.Join([]string{pkg.Name, pkg.Version, pkg.Release, pkg.Arch}, "-")
		}
	}

	return m, nil
}

type fileInfo struct {
	Mode os.FileMode
}

// generateChangesFor will check layer for file changes, and will return a list of those.
func generateChangesFor(ctx context.Context, layer v1.Layer) (map[string]fileInfo, error) {
	logger := logr.FromContextOrDiscard(ctx)
	layerReader, err := layer.Uncompressed()
	if err != nil {
		return nil, fmt.Errorf("reading layer contents: %w", err)
	}
	defer layerReader.Close()
	tarReader := tar.NewReader(layerReader)
	// Use a map so we can remove items easily. Will turn this into a string slice before returning
	filelist := make(map[string]fileInfo)
	var links []string
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar: %w", err)
		}

		// Some tools prepend everything with "./", so if we don't Clean the
		// name, we may have duplicate entries, which angers tar-split.
		header.Name = filepath.Clean(header.Name)
		// force PAX format to remove Name/Linkname length limit of 100 characters
		// required by USTAR and to not depend on internal tar package guess which
		// prefers USTAR over PAX
		header.Format = tar.FormatPAX

		basename := filepath.Base(header.Name)
		dirname := filepath.Dir(header.Name)
		tombstone := strings.HasPrefix(basename, whiteoutPrefix)
		if tombstone {
			basename = basename[len(whiteoutPrefix):]
		}

		// If there is a capability entry, ignore the file
		if _, found := header.PAXRecords["SCHILY.xattr.security.capability"]; found {
			logger.V(log.TRC).Info("security capabilities found in layer tar, ignoring file", "file", header.Name)
			continue
		}

		switch {
		case (header.Typeflag == tar.TypeDir && tombstone) || header.Typeflag == tar.TypeReg:
			filelist[strings.TrimPrefix(filepath.Join(dirname, basename), "/")] = fileInfo{header.FileInfo().Mode()}
		case header.Typeflag == tar.TypeSymlink || header.Typeflag == tar.TypeLink:
			filelist[strings.TrimPrefix(header.Name, "/")] = fileInfo{header.FileInfo().Mode()}
			// Add the target to the links slice so we can remove them later
			links = append(links, strings.TrimPrefix(header.Linkname, "/"))
		default:
			// TODO: what do we do with other flags?
			continue
		}
	}

	// We have to process these after the fact, as the link could have come before
	// the target in the tarball
	// As it stands now, this really only works for links that the target is a fully
	// qualified path. If the link was relative, this probably doesn't work.
	for _, link := range links {
		delete(filelist, link)
	}

	return filelist, nil
}

// ExtractRPMDB copies /var/lib/rpm/* from the archive and derives a list of packages from
// the rpm database.
func extractRPMDB(ctx context.Context, layer v1.Layer) ([]*rpmdb.PackageInfo, error) {
	layerReader, err := layer.Uncompressed()
	if err != nil {
		return nil, fmt.Errorf("reading layer contents: %w", err)
	}
	defer layerReader.Close()

	fs := afero.NewOsFs()
	basepath, err := afero.TempDir(fs, "", "rpmdb")
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = fs.RemoveAll(basepath)
	}()

	tarReader := tar.NewReader(layerReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar: %w", err)
		}

		// Some tools prepend everything with "./", so if we don't Clean the
		// name, we may have duplicate entries, which angers tar-split.
		header.Name = filepath.Clean(header.Name)
		header.Format = tar.FormatPAX
		rpmdirname := "var/lib/rpm"
		basename := filepath.Base(header.Name)
		dirname := filepath.Dir(header.Name)
		tombstone := strings.HasPrefix(basename, whiteoutPrefix)

		// Not a file or directory? Continue...
		if header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg {
			continue
		}

		// Tombstone? Ignore...
		if tombstone {
			continue
		}

		// Not in the RPM directory. Ignore...
		if !strings.HasPrefix(filepath.Join(dirname, basename), rpmdirname) {
			continue
		}
		// a dir or file with the correct var/lib/rpm prefix that has not been marked with a tombstone is valid.
		if header.Typeflag == tar.TypeDir {
			err := os.MkdirAll(filepath.Join(basepath, dirname, basename), header.FileInfo().Mode())
			if err != nil {
				return nil, err
			}
			continue
		}

		f, err := fs.OpenFile(filepath.Join(basepath, dirname, basename), os.O_RDWR|os.O_CREATE|os.O_TRUNC, header.FileInfo().Mode())
		if err != nil {
			return nil, err
		}
		err = func() error {
			// closure here allows us to defer f.Close() in this iteration instead of
			// waiting for the parent function to complete.
			defer f.Close()
			_, err := io.Copy(f, tarReader)
			if err != nil {
				return err
			}
			return nil
		}()
		if err != nil {
			return nil, err
		}
	}

	packageList, err := rpm.GetPackageList(ctx, basepath)
	if err != nil {
		return nil, err
	}

	return packageList, nil
}
