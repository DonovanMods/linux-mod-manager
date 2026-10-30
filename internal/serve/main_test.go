package serve

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/testutil"
)

// noCoreDumpLimit is the soft RLIMIT_CORE TestMain gives this test binary,
// and through it every browser the E2E suite launches (#561).
//
// 1, not 0. Headless Chromium segfaults now and then on teardown, harmless
// to the tests, and with the piped core_pattern systemd ships
// (`|systemd-coredump`) the kernel ignores RLIMIT_CORE except for this one
// value: 1 is its recursion guard, and skips the pipe entirely ("RLIMIT_CORE
// is set to 1, aborting core"). Any other value too small to dump, 0
// included, still hands the crash to systemd-coredump, which stores no core
// but journals the crash anyway - and a desktop crash watcher turns every
// journalled coredump into a notification. With a plain-file core_pattern,
// 1 byte is too small for any dump, so it acts as 0 there.
const noCoreDumpLimit = 1

// TestMain lowers the soft core-dump limit before any test runs, so the
// browsers the E2E tests start inherit it and leave neither a core file
// nor a crash notification behind (#561). Only the soft limit moves; the
// hard limit is left as it was.
func TestMain(m *testing.M) {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &limit); err != nil {
		fmt.Fprintf(os.Stderr, "reading RLIMIT_CORE: %v\n", err)
		os.Exit(1)
	}
	// A hard limit of 0 (some containers) cannot be raised to 1 and already
	// stops file dumps; leave it rather than fail the run.
	if limit.Max >= noCoreDumpLimit {
		limit.Cur = noCoreDumpLimit
		if err := syscall.Setrlimit(syscall.RLIMIT_CORE, &limit); err != nil {
			fmt.Fprintf(os.Stderr, "setting RLIMIT_CORE: %v\n", err)
			os.Exit(1)
		}
	}
	// #568: a server built with a ReleaseChecker refreshes it in the
	// background, so nothing here may reach api.github.com: a request is
	// refused before it is sent, and any attempt fails the run.
	githubAttempts := testutil.GuardGitHub()
	code := m.Run()
	if attempts := githubAttempts(); len(attempts) > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: the test suite tried to reach GitHub:\n  %s\n", strings.Join(attempts, "\n  "))
		code = 1
	}
	os.Exit(code)
}

// TestMain_DisablesCoreDumps pins the limit TestMain sets (#561), so a
// change to 0 - which looks equivalent and is not, under a piped
// core_pattern - fails here rather than on somebody's desktop.
func TestMain_DisablesCoreDumps(t *testing.T) {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_CORE, &limit); err != nil {
		t.Fatalf("reading RLIMIT_CORE: %v", err)
	}
	if limit.Max == 0 {
		t.Skip("hard RLIMIT_CORE is 0; TestMain cannot raise the soft limit to 1")
	}
	if limit.Cur != 1 {
		t.Fatalf("soft RLIMIT_CORE = %d, want 1 (see noCoreDumpLimit)", limit.Cur)
	}
}
