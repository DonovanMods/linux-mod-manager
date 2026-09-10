// This file puts the FAKE steamcmd on PATH.
//
// lmm shells out to steamcmd for an anonymous Steam Workshop download
// (#269 Tier 3). No test anywhere in this repository may run the real one:
// it self-bootstraps ~200 MB, talks to Valve, and will write into the
// user's real Steam library if lmm ever forgets to pin +force_install_dir.
// The stand-in lives at internal/source/steamworkshop/testdata/
// fakesteamcmd/steamcmd and asserts every one of those rules itself.
//
// Three packages need it - the source that runs the tool, cmd/lmm's
// end-to-end install, and internal/serve's install job - so the "copy it
// somewhere executable and prepend that to PATH" step lives here rather
// than three times over.
package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeSteamcmdRelPath is the script's location relative to the module root.
const fakeSteamcmdRelPath = "internal/source/steamworkshop/testdata/fakesteamcmd/steamcmd"

// FakeSteamcmdOnPath copies the fake steamcmd into a temp directory of its
// own and prepends that directory to PATH for the duration of the test.
//
// It is copied rather than used in place for two reasons: the executable
// bit is a property of the checkout rather than of the file's content (a
// zip export or a restrictive umask loses it), and prepending a testdata
// directory to PATH would put every other file that ever lands there on it
// too.
//
// The test is skipped when bash is unavailable - the same optional-tool
// rule the 7z and rar extraction tests follow.
func FakeSteamcmdOnPath(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/bin/bash"); err != nil {
		if _, err := os.Stat("/usr/bin/bash"); err != nil {
			t.Skip("bash is unavailable; the fake steamcmd is a bash script")
		}
	}

	root := moduleRoot(t)
	script, err := os.ReadFile(filepath.Join(root, fakeSteamcmdRelPath))
	if err != nil {
		t.Fatalf("reading the fake steamcmd: %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "steamcmd"), script, 0700); err != nil {
		t.Fatalf("installing the fake steamcmd: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// moduleRoot walks up from the test's working directory to the directory
// holding go.mod, so a helper called from any package finds the same
// checkout.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolving the working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s: cannot locate the module root", dir)
		}
		dir = parent
	}
}
