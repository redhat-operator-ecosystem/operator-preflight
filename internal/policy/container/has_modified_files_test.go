package container

import (
	"bytes"
	"context"
	"io/fs"
	"path"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/spf13/afero"

	"github.com/redhat-openshift-ecosystem/openshift-preflight/internal/image"

	"github.com/bombsimon/logrusr/v4"
	"github.com/go-logr/logr"
	rpmdb "github.com/knqyf263/go-rpmdb/pkg"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

const (
	fileMask            = fs.ModePerm & 0o644
	fileMaskWithSetuid  = fileMask | fs.ModeSetuid
	fileMaskWithSetgid  = fileMask | fs.ModeSetgid
	fileMaskWithBothSet = fileMaskWithSetuid | fileMaskWithSetgid
)

func deepCopyPackage(pkgs map[string]packageFilesRef) map[string]packageFilesRef {
	newPkgs := make(map[string]packageFilesRef)
	for k, v := range pkgs {
		newPkgs[k] = v
	}

	return newPkgs
}

var _ = Describe("HasModifiedFiles", func() {
	var (
		hasModifiedFiles HasModifiedFilesCheck
		layers           []string
		pkgRef           map[string]packageFilesRef
		dist             string
	)

	BeforeEach(func() {
		pkgRef = make(map[string]packageFilesRef)
		pkgRef["firstlayer"] = packageFilesRef{
			LayerFiles: map[string]fileInfo{
				"this":       {fileMask},
				"is":         {fileMask},
				"not":        {fileMask},
				"prohibited": {fileMask},
				"uidgidset":  {fileMaskWithBothSet},
			},
			LayerPackages: map[string]packageMeta{
				"foo-1.0-1.d9": {
					Name:    "foo",
					Version: "1.0",
					Release: "1.d9",
					Arch:    "fooarch",
					Vendor:  "Red Hat, Inc.",
				},
				"bar-1.0-1.d9": {
					Name:    "bar",
					Version: "1.0",
					Release: "1.d9",
					Arch:    "fooarch",
					Vendor:  "Red Hat, Inc.",
				},
				"baz-2.0-1.d9": {
					Name:    "baz",
					Version: "1.0",
					Release: "1.d9",
					Arch:    "fooarch",
					Vendor:  "Red Hat, Inc.",
				},
			},
			LayerPackageFiles: map[string]string{
				"this":      "foo-1.0-1.d9",
				"is":        "bar-1.0-1.d9",
				"not":       "baz-2.0-1.d9",
				"uidgidset": "baz-2.0-1.d9",
			},
			HasRPMDB: true,
		}

		pkgRef["secondlayer"] = packageFilesRef{
			LayerFiles: map[string]fileInfo{
				"there":      {fileMask},
				"are":        {fileMask},
				"no":         {fileMask},
				"prohibited": {fileMask},
				"duplicates": {fileMask},
			},
			LayerPackages: map[string]packageMeta{
				"foo-1.0-1.d9": {
					Name:    "foo",
					Version: "1.0",
					Release: "1.d9",
					Arch:    "fooarch",
					Vendor:  "Red Hat, Inc.",
				},
				"bar-1.0-1.d9": {
					Name:    "bar",
					Version: "1.0",
					Release: "1.d9",
					Arch:    "fooarch",
					Vendor:  "Red Hat, Inc.",
				},
				"baz-2.0-1.d9": {
					Name:    "baz",
					Version: "2.0",
					Release: "1.d9",
					Arch:    "fooarch",
					Vendor:  "Red Hat, Inc.",
				},
				"boz-3.0-1.d9": {
					Name:    "boz",
					Version: "3.0",
					Release: "1.d9",
					Arch:    "fooarch",
					Vendor:  "Red Hat, Inc.",
				},
			},
			LayerPackageFiles: map[string]string{
				"this":      "foo-1.0-1.d9",
				"is":        "bar-1.0-1.d9",
				"not":       "baz-2.0-1.d9",
				"uidgidset": "baz-2.0-1.d9",
				"no":        "boz-3.0-1.d9",
				"there":     "boz-3.0-1.d9",
			},
			HasRPMDB: true,
		}
		pkgRef["lastlayer"] = packageFilesRef{
			LayerFiles: map[string]fileInfo{
				"prohibited": {fileMask},
			},
			LayerPackages: map[string]packageMeta{
				"foo-1.0-1.d9": {
					Name:    "foo",
					Version: "1.0",
					Release: "1.d9",
					Arch:    "fooarch",
					Vendor:  "Red Hat, Inc.",
				},
				"bar-1.0-1.d9": {
					Name:    "bar",
					Version: "1.0",
					Release: "1.d9",
					Arch:    "fooarch",
					Vendor:  "Red Hat, Inc.",
				},
				"baz-2.0-1.d9": {
					Name:    "baz",
					Version: "2.0",
					Release: "1.d9",
					Arch:    "fooarch",
					Vendor:  "Red Hat, Inc.",
				},
				"boz-3.0-1.d9": {
					Name:    "boz",
					Version: "3.0",
					Release: "1.d9",
					Arch:    "fooarch",
					Vendor:  "Red Hat, Inc.",
				},
			},
			LayerPackageFiles: map[string]string{
				"this":      "foo-1.0-1.d9",
				"is":        "bar-1.0-1.d9",
				"not":       "baz-2.0-1.d9",
				"uidgidset": "baz-2.0-1.d9",
			},
		}
		layers = []string{
			"firstlayer",
			"secondlayer",
			"lastlayer",
		}
		dist = "d9"
	})

	Context("Checking if it has any modified RPM files", func() {
		When("there are no modified RPM files found", func() {
			It("should pass validate", func() {
				ok, err := hasModifiedFiles.validate(context.Background(), layers, pkgRef, dist)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeTrue())
			})
		})
		When("there is a modified RPM file found", func() {
			var pkgs map[string]packageFilesRef
			BeforeEach(func() {
				pkgs = deepCopyPackage(pkgRef)
			})
			When("only the file is changed", func() {
				BeforeEach(func() {
					pkgSecondLayer := pkgs["secondlayer"]
					pkgSecondLayer.LayerFiles["this"] = fileInfo{
						Mode: fileMask,
					}
					pkgs["secondlayer"] = pkgSecondLayer
				})
				It("should not pass Validate", func() {
					ok, err := hasModifiedFiles.validate(context.Background(), layers, pkgs, dist)
					Expect(err).ToNot(HaveOccurred())
					Expect(ok).To(BeFalse())
				})
			})
			When("setuid is removed", func() {
				BeforeEach(func() {
					pkgSecondLayer := pkgs["secondlayer"]
					pkgSecondLayer.LayerFiles["uidgidset"] = fileInfo{
						Mode: fileMaskWithSetgid,
					}
					pkgs["secondlayer"] = pkgSecondLayer
				})
				It("should pass Validate", func() {
					ok, err := hasModifiedFiles.validate(context.Background(), layers, pkgs, dist)
					Expect(err).ToNot(HaveOccurred())
					Expect(ok).To(BeTrue())
				})
			})
			When("setgid is removed", func() {
				BeforeEach(func() {
					pkgSecondLayer := pkgs["secondlayer"]
					pkgSecondLayer.LayerFiles["uidgidset"] = fileInfo{
						Mode: fileMaskWithSetuid,
					}
					pkgs["secondlayer"] = pkgSecondLayer
				})
				It("should pass Validate", func() {
					ok, err := hasModifiedFiles.validate(context.Background(), layers, pkgs, dist)
					Expect(err).ToNot(HaveOccurred())
					Expect(ok).To(BeTrue())
				})
			})
			When("setuid/setgid are removed", func() {
				BeforeEach(func() {
					pkgSecondLayer := pkgs["secondlayer"]
					pkgSecondLayer.LayerFiles["uidgidset"] = fileInfo{
						Mode: fileMask,
					}
					pkgs["secondlayer"] = pkgSecondLayer
				})
				It("should pass Validate", func() {
					ok, err := hasModifiedFiles.validate(context.Background(), layers, pkgs, dist)
					Expect(err).ToNot(HaveOccurred())
					Expect(ok).To(BeTrue())
				})
			})
			When("setuid is added", func() {
				BeforeEach(func() {
					pkgSecondLayer := pkgs["secondlayer"]
					pkgSecondLayer.LayerFiles["this"] = fileInfo{
						Mode: fileMaskWithSetuid,
					}
					pkgs["secondlayer"] = pkgSecondLayer
				})
				It("should not pass Validate", func() {
					ok, err := hasModifiedFiles.validate(context.Background(), layers, pkgs, dist)
					Expect(err).ToNot(HaveOccurred())
					Expect(ok).To(BeFalse())
				})
			})
			When("setgid is added", func() {
				BeforeEach(func() {
					pkgSecondLayer := pkgs["secondlayer"]
					pkgSecondLayer.LayerFiles["this"] = fileInfo{
						Mode: fileMaskWithSetgid,
					}
					pkgs["secondlayer"] = pkgSecondLayer
				})
				It("should not pass Validate", func() {
					ok, err := hasModifiedFiles.validate(context.Background(), layers, pkgs, dist)
					Expect(err).ToNot(HaveOccurred())
					Expect(ok).To(BeFalse())
				})
			})
			When("setuid/setgid are added", func() {
				BeforeEach(func() {
					pkgSecondLayer := pkgs["secondlayer"]
					pkgSecondLayer.LayerFiles["this"] = fileInfo{
						Mode: fileMaskWithBothSet,
					}
					pkgs["secondlayer"] = pkgSecondLayer
				})
				It("should not pass Validate", func() {
					ok, err := hasModifiedFiles.validate(context.Background(), layers, pkgs, dist)
					Expect(err).ToNot(HaveOccurred())
					Expect(ok).To(BeFalse())
				})
			})
		})
		When("a package is updated", func() {
			var pkgs map[string]packageFilesRef
			BeforeEach(func() {
				pkgs = deepCopyPackage(pkgRef)
				pkgSecondLayer := pkgs["secondlayer"]
				pkgSecondLayerPackageFiles := pkgSecondLayer.LayerPackageFiles
				pkgSecondLayerPackageFiles["this"] = "foo-2.0-d9"
				pkgSecondLayer.LayerPackageFiles = pkgSecondLayerPackageFiles
				pkgs["secondlayer"] = pkgSecondLayer
			})
			It("should pass validate", func() {
				ok, err := hasModifiedFiles.validate(context.Background(), layers, pkgs, dist)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeTrue())
			})
		})
		When("a package is removed", func() {
			var pkgs map[string]packageFilesRef
			BeforeEach(func() {
				pkgs = deepCopyPackage(pkgRef)
				pkgSecondLayer := pkgs["secondlayer"]
				pkgSecondLayerPackageFiles := pkgSecondLayer.LayerPackageFiles
				delete(pkgSecondLayerPackageFiles, "this")
				pkgSecondLayer.LayerPackageFiles = pkgSecondLayerPackageFiles
				pkgs["secondlayer"] = pkgSecondLayer
			})
			It("should pass validate", func() {
				ok, err := hasModifiedFiles.validate(context.Background(), layers, pkgs, dist)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeTrue())
			})
		})
		When("the package release dist changes", func() {
			var pkgs map[string]packageFilesRef
			BeforeEach(func() {
				pkgs = deepCopyPackage(pkgRef)

				pkgSecondLayerPackageFiles := pkgs["secondlayer"].LayerPackageFiles
				delete(pkgSecondLayerPackageFiles, "this")
				pkgSecondLayerPackageFiles["this"] = "foo-1.0-1.d10"

				pkgSecondLayerPackages := pkgs["secondlayer"].LayerPackages
				delete(pkgSecondLayerPackages, "foo-1.0-1.d9")
				pkgSecondLayerPackages["foo-1.0-1.d10"] = packageMeta{
					Name:    "foo",
					Version: "1.0",
					Release: "1.d10",
					Arch:    "fooarch",
				}

				pkgSecondLayerFiles := pkgs["secondlayer"].LayerFiles
				pkgSecondLayerFiles["this"] = fileInfo{
					Mode: fs.ModePerm,
				}
				pkgs["secondlayer"] = packageFilesRef{
					LayerPackages:     pkgSecondLayerPackages,
					LayerPackageFiles: pkgSecondLayerPackageFiles,
					LayerFiles:        pkgSecondLayerFiles,
					HasRPMDB:          true,
				}
			})
			It("should fail because of different release dist", func() {
				ok, err := hasModifiedFiles.validate(context.Background(), layers, pkgs, dist)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeFalse())
			})
		})
		When("the package architecture changes", func() {
			var pkgs map[string]packageFilesRef
			BeforeEach(func() {
				pkgs = deepCopyPackage(pkgRef)

				pkgSecondLayerPackageFiles := pkgs["secondlayer"].LayerPackageFiles
				delete(pkgSecondLayerPackageFiles, "this")
				pkgSecondLayerPackageFiles["this"] = "foo-1.0-1.d10"

				pkgSecondLayerPackages := pkgs["secondlayer"].LayerPackages
				delete(pkgSecondLayerPackages, "foo-1.0-1.d9")
				pkgSecondLayerPackages["foo-1.0-1.d10"] = packageMeta{
					Name:    "foo",
					Version: "1.0",
					Release: "1.d9",
					Arch:    "differentarch",
				}

				pkgSecondLayerFiles := pkgs["secondlayer"].LayerFiles
				pkgSecondLayerFiles["this"] = fileInfo{
					Mode: fs.ModePerm,
				}
				pkgs["secondlayer"] = packageFilesRef{
					LayerPackages:     pkgSecondLayerPackages,
					LayerPackageFiles: pkgSecondLayerPackageFiles,
					LayerFiles:        pkgSecondLayerFiles,
					HasRPMDB:          true,
				}
			})
			It("should fail because of different architectures dist", func() {
				ok, err := hasModifiedFiles.validate(context.Background(), layers, pkgs, dist)
				Expect(err).ToNot(HaveOccurred())
				Expect(ok).To(BeFalse())
			})
		})
		When("release dist does not match installed OS", func() {
			When("package is a net-new", func() {
				When("a file is modified", func() {
					var pkgs map[string]packageFilesRef
					var ctx context.Context
					var logOutput bytes.Buffer
					BeforeEach(func() {
						pkgs = deepCopyPackage(pkgRef)

						pkgSecondLayerPackages := pkgs["secondlayer"].LayerPackages
						pkgs["secondlayer"].LayerPackages["other-1.0-1.oth"] = packageMeta{
							Name:    "other",
							Version: "1.0",
							Release: "1.oth",
							Arch:    "x86_64",
						}
						pkgSecondLayerPackageFiles := pkgs["secondlayer"].LayerPackageFiles
						pkgSecondLayerPackageFiles["otherfile"] = "other-1.0-1.oth"
						pkgSecondLayerFiles := pkgs["secondlayer"].LayerFiles
						pkgSecondLayerFiles["otherfile"] = fileInfo{
							Mode: fs.ModePerm,
						}
						pkgs["secondlayer"] = packageFilesRef{
							LayerPackages:     pkgSecondLayerPackages,
							LayerPackageFiles: pkgSecondLayerPackageFiles,
							LayerFiles:        pkgSecondLayerFiles,
							HasRPMDB:          true,
						}

						pkgLastLayerFiles := pkgs["lastlayer"].LayerFiles
						pkgLastLayerFiles["otherfile"] = fileInfo{
							Mode: fs.ModePerm,
						}
						pkgs["lastlayer"] = packageFilesRef{
							LayerPackages:     pkgs["secondlayer"].LayerPackages,
							LayerPackageFiles: pkgs["secondlayer"].LayerPackageFiles,
							LayerFiles:        pkgLastLayerFiles,
							HasRPMDB:          false,
						}

						l := logrus.New()
						l.SetLevel(logrus.DebugLevel)
						l.SetOutput(&logOutput)
						logger := logrusr.New(l)
						ctx = logr.NewContext(context.Background(), logger)
					})
					It("should warn but not fail", func() {
						ok, err := hasModifiedFiles.validate(ctx, layers, pkgs, dist)
						Expect(err).ToNot(HaveOccurred())
						Expect(ok).To(BeTrue())
						Expect(logOutput.String()).To(ContainSubstring("WARN"))
					})
				})
			})
		})
	})

	When("the first layer is empty", func() {
		var zeroPkgRef map[string]packageFilesRef
		var zeroLayers []string
		BeforeEach(func() {
			zeroPkgRef = deepCopyPackage(pkgRef)
			zeroPkgRef["zerolayer"] = packageFilesRef{
				LayerFiles:        map[string]fileInfo{},
				LayerPackages:     make(map[string]packageMeta),
				LayerPackageFiles: make(map[string]string),
				HasRPMDB:          false,
			}
			zeroLayers = append([]string{"zerolayer"}, layers...)
		})
		It("should ignore it", func() {
			ok, err := hasModifiedFiles.validate(context.Background(), zeroLayers, zeroPkgRef, dist)
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
		})
	})

	When("building the installed file list for installed packages", func() {
		const (
			basename = "foobasename"
			dirname  = "foodirname"
			dirindex = 0
		)
		var goodPkgList []*rpmdb.PackageInfo

		BeforeEach(func() {
			// goodPkgList represents three mock RPM's,
			// the first is a basic one to cover the happy path.
			// the second is need to test our filtering logic for files from another architecture.
			// the third is to test our filtering logic for directories.
			goodPkgList = []*rpmdb.PackageInfo{
				{
					BaseNames:  []string{basename},
					DirIndexes: []int32{dirindex},
					DirNames:   []string{dirname},
					Name:       "foo",
					Version:    "1.0.0",
					Release:    "100",
					Arch:       "x86_64",
				},
				{
					BaseNames:  []string{basename},
					DirIndexes: []int32{dirindex},
					DirNames:   []string{dirname},
					Name:       "foo",
					Version:    "1.0.0",
					Release:    "100",
					Arch:       "i686",
				},
				{
					BaseNames:  []string{""},
					DirIndexes: []int32{dirindex},
					DirNames:   []string{"/"},
					Name:       "just-dirs",
					Version:    "1.0.0",
					Release:    "100",
					Arch:       "x86_64",
				},
			}
		})
		It("should contain all files installed by the package according to its metadata", func() {
			files, err := installedFileMapWithExclusions(context.TODO(), goodPkgList)
			Expect(err).ToNot(HaveOccurred())

			_, ok := files[path.Join(dirname, basename)]
			Expect(ok).To(BeTrue())
		})

		It("should fail if the rpm is invalid", func() {
			badPkgList := goodPkgList
			badPkgList[0].DirNames = []string{dirname, "extradir"}
			_, err := installedFileMapWithExclusions(context.TODO(), badPkgList)
			Expect(err).To(HaveOccurred())
		})
	})

	When("multiple no-op layers with the same IDs split layers containing RPMDB modifications", func() {
		// Test case ensures that we properly deduplicate layer hashes in our file mapping to avoid cases where
		// a later layer with the same ID as an earlier layer doesn't overwrite the earlier layer's file mapping.
		var img image.ImageReference
		var actualLayerCount int
		BeforeEach(func() {
			// TODO: The containerfile that generates this test fixture is stored in-repo tests/containerfiles.
			// The external call here avoids having to store the image locally. A crane-built image runs into
			// issues because we cannot run `microdnf` commands using Crane, and need to have multiple layers
			// containing RPMDBs to test this issue correctly.
			const dupeLayerTestFixture = "quay.io/opdev/preflight-test-fixture:duplicate-layers"
			cImg, pullError := crane.Pull(dupeLayerTestFixture)
			Expect(pullError).ToNot(HaveOccurred())
			img = image.ImageReference{
				ImageInfo: cImg,
			}

			layers, err := img.ImageInfo.Layers()
			Expect(err).ToNot(HaveOccurred())
			actualLayerCount = len(layers)
		})

		It("should validate and have matching layer counts", func() {
			fs := afero.NewOsFs()
			layerIDs, layerRefs, err := hasModifiedFiles.gatherDataToValidate(context.TODO(), img, fs)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(layerIDs)).To(Equal(actualLayerCount))
			Expect(len(layerRefs)).To(Equal(actualLayerCount))
		})
	})

	When("calling the top level Validate", func() {
		It("should fail with an invalid ImageReference", func() {
			passed, err := hasModifiedFiles.Validate(context.TODO(), image.ImageReference{})
			Expect(err).To(HaveOccurred())
			Expect(passed).To(BeFalse())
		})
	})

	AssertMetaData(&hasModifiedFiles)
})
