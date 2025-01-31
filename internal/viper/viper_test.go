package viper

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Viper tests", func() {
	Context("Lazy Loading Viper", func() {
		When("the viper instance hasn't been initialized", func() {
			instance = nil
			It("should be initialized when calling for it", func() {
				_ = Instance() // we don't care about this return value for this test.
				Expect(instance).ToNot(BeNil())
			})
		})
	})

	Context("Getting the project-specific Viper instance", func() {
		When("Requesting the viper instance for the project", func() {
			It("Should return a non-empty viper instance", func() {
				packageV := Instance()
				packageV.Set("foo", "bar")
				Expect(Instance().Get("foo")).To(Equal("bar"))
			})
		})
	})

	When("Resetting the project-specific Viper instance", func() {
		It("should properly clear the instance", func() {
			packageV := Instance()
			packageV.Set("foo", "bar")
			Expect(Instance().Get("foo")).To(Equal("bar"))
			Reset()
			Expect(Instance().Get("foo")).To(BeNil())
		})
	})
})
