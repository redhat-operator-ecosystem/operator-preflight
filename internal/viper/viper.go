// Package viper provides a package-specific instance of Viper to avoid
// the use of Viper's global instance, which can cause conflicts
package viper

import (
	"sync"

	spfviper "github.com/spf13/viper"
)

var (
	instance *spfviper.Viper
	mu       = sync.Mutex{}
)

// Instance provides the instance of Viper, or lazy-loads a new one
// if one has not been defined.
func Instance() *spfviper.Viper {
	if instance != nil {
		return instance
	}

	mu.Lock()
	defer mu.Unlock()
	if instance == nil {
		instance = spfviper.New()
	}
	return instance
}

// Reset creates a new Viper instance. This should really only be used
// for testing purposes.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	instance = spfviper.New()
}
