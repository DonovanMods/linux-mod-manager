package app

// Package-wide test hermeticity for credentials (Track B review N9).
//
// A developer's shell exports real NEXUSMODS_API_KEY/CURSEFORGE_API_KEY
// (.envrc does), and since #356 the environment OUTRANKS a stored token -
// so an ambient key changes what AuthStatus reports for a built-in source,
// which several tests here assert on. Individual tests were blanking it
// one by one; clearing it once for the package is the version that a test
// added later cannot forget. A test that wants a key sets its own with
// t.Setenv, which still works and still restores.

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// builtinAPIKeyEnvVars are the credential variables the built-in sources
// honour - the two a developer's own shell is likely to have exported.
var builtinAPIKeyEnvVars = []string{"NEXUSMODS_API_KEY", "CURSEFORGE_API_KEY"}

func TestMain(m *testing.M) {
	for _, key := range builtinAPIKeyEnvVars {
		if err := os.Unsetenv(key); err != nil {
			fmt.Fprintf(os.Stderr, "unsetting %s: %v\n", key, err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

// TestNoAmbientAPIKeys fails if that guard is removed, rather than letting
// the suite quietly start depending on the developer's own credentials.
func TestNoAmbientAPIKeys(t *testing.T) {
	for _, key := range builtinAPIKeyEnvVars {
		assert.Empty(t, os.Getenv(key),
			"TestMain must clear %s: a real key in the environment outranks a stored token "+
				"and changes what the auth surfaces report", key)
	}
}
