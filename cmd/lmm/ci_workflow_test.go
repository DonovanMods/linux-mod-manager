package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	// ciTestWorkflow is the CI job that has to agree with the Makefile
	// about how long a test binary may run.
	ciTestWorkflow = "../../.github/workflows/test.yml"
	repoMakefile   = "../../Makefile"
)

// goTestInvocation matches a real `go test` command line - "go test" followed
// by a flag or a package pattern - and not the workflow step *named* "go test
// (race)".
var goTestInvocation = regexp.MustCompile(`\bgo test\s+[-.][^\n]*`)

// TestCIRaceJobHasATestTimeout is the ratchet behind #374: the Makefile
// raises Go's 10-minute per-BINARY default to TEST_TIMEOUT because the v2
// suite has outgrown it (cmd/lmm, internal/core and internal/serve each run
// 9-11 minutes under -race), but CI invoked `go test -race ./...` directly
// and so kept the default - the exact failure TEST_TIMEOUT exists to
// prevent, reported as "panic: test timed out after 10m0s" with no failing
// assertion anywhere.
//
// Either route satisfies it: `make test-race` (which also picks up the
// project GOCACHE), in which case the Makefile target must carry the
// timeout, or an explicit -timeout on the workflow's own command line.
func TestCIRaceJobHasATestTimeout(t *testing.T) {
	workflow, err := os.ReadFile(ciTestWorkflow)
	require.NoError(t, err, "reading the CI workflow")

	if strings.Contains(string(workflow), "make test-race") {
		makefile, err := os.ReadFile(repoMakefile)
		require.NoError(t, err, "reading the Makefile")
		target := regexp.MustCompile(`(?m)^test-race:\n(?:\t[^\n]*\n)+`).FindString(string(makefile))
		require.NotEmpty(t, target, "%s has no test-race target for %s to run", repoMakefile, ciTestWorkflow)
		require.Contains(t, target, "-timeout",
			"%s delegates to `make test-race`, but that target runs without a -timeout", ciTestWorkflow)
		return
	}

	invocations := goTestInvocation.FindAllString(string(workflow), -1)
	require.NotEmpty(t, invocations, "%s neither runs `go test` nor delegates to `make test-race`", ciTestWorkflow)
	for _, line := range invocations {
		require.Contains(t, line, "-timeout",
			"%s runs %q with no -timeout: Go's 10-minute per-binary default is shorter than the v2 suite (see TEST_TIMEOUT in the Makefile). Use `make test-race`, or pass -timeout explicitly.",
			ciTestWorkflow, strings.TrimSpace(line))
	}
}

// TestCIRaceJobRunsTheRaceDetector guards the other half of the same step:
// the timeout fix must not quietly drop -race.
func TestCIRaceJobRunsTheRaceDetector(t *testing.T) {
	workflow, err := os.ReadFile(ciTestWorkflow)
	require.NoError(t, err, "reading the CI workflow")

	body := string(workflow)
	require.True(t,
		strings.Contains(body, "make test-race") || strings.Contains(body, "go test -race"),
		"%s no longer runs the suite under the race detector", ciTestWorkflow)
}
