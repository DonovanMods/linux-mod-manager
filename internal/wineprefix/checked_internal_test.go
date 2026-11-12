package wineprefix

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #605 re-review: FindUser fails CLOSED. Any error checking the picked
// directory - not only ErrUserOutsidePrefix, but an EvalSymlinks failure or
// a link swapped between FindUser's Stat and the check - leaves the user
// unresolved, carrying the error. Such errors cannot be produced reliably
// from a temp tree (the Stat before the check already filters a dangling
// link), so the check is replaced through its seam.
func TestFindUser_AnyCheckErrorLeavesTheUserUnresolved(t *testing.T) {
	pfx := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(pfx, "drive_c", "users", "dyoung"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pfx, "system.reg"), nil, 0o644))

	boom := errors.New("lstat /prefix/drive_c/users/dyoung: permission denied")
	was := checkUserDir
	t.Cleanup(func() { checkUserDir = was })
	checkUserDir = func(string, string) error { return boom }

	got := FindUser(pfx)
	assert.Empty(t, got.Name, "never picked on an error")
	assert.Equal(t, "dyoung", got.Refused)
	assert.ErrorIs(t, got.Err, boom)
}
