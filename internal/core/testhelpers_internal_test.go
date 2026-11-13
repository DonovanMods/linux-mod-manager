package core

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

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

// relsOf is relsOf of the external test package, for internal tests: a
// deployed-file listing's paths relative to their roots (#629).
func relsOf(paths []domain.DeployPath, err error) ([]string, error) {
	if paths == nil {
		return nil, err
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = p.Rel
	}
	return out, err
}

// membersErr is a deployable listing's cache members, for a test that
// asserts on members by name.
func membersErr(items []deployItem, err error) ([]string, error) {
	if items == nil {
		return nil, err
	}
	return membersOf(items), err
}
