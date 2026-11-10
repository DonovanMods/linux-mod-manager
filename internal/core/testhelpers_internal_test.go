package core

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// newBareService returns a Service over three fresh temp directories, with
// nothing seeded and no source registered, closed when the test ends. It is
// the package-internal twin of core_test's helper of the same name
// (testhelpers_test.go): the two test packages cannot share unexported code.
func newBareService(t *testing.T) *Service {
	t.Helper()
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc
}
