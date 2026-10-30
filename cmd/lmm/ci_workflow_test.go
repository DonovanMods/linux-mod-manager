package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
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

// makeRaceTarget matches a workflow step delegating to a race target:
// `make test-race`, or one of the halves CI splits it into (#576).
var makeRaceTarget = regexp.MustCompile(`\bmake (test-race[\w-]*)`)

// makeTargetRecipe returns target's recipe lines in the Makefile, or "".
func makeTargetRecipe(makefile, target string) string {
	return regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(target) + `:\n(?:\t[^\n]*\n)+`).FindString(makefile)
}

// TestCIRaceJobHasATestTimeout is the ratchet behind #374: the Makefile
// raises Go's 10-minute per-BINARY default to TEST_TIMEOUT because the v2
// suite has outgrown it (cmd/lmm, internal/core and internal/serve each run
// 9-11 minutes under -race), but CI invoked `go test -race ./...` directly
// and so kept the default - the exact failure TEST_TIMEOUT exists to
// prevent, reported as "panic: test timed out after 10m0s" with no failing
// assertion anywhere.
//
// Either route satisfies it: a `make test-race*` target (which also picks
// up the project GOCACHE), in which case every target the workflow names
// must carry the timeout, or an explicit -timeout on the workflow's own
// command line.
func TestCIRaceJobHasATestTimeout(t *testing.T) {
	workflow, err := os.ReadFile(ciTestWorkflow)
	require.NoError(t, err, "reading the CI workflow")

	if targets := makeRaceTarget.FindAllStringSubmatch(string(workflow), -1); len(targets) > 0 {
		makefile, err := os.ReadFile(repoMakefile)
		require.NoError(t, err, "reading the Makefile")
		for _, m := range targets {
			recipe := makeTargetRecipe(string(makefile), m[1])
			require.NotEmpty(t, recipe, "%s has no %s target for %s to run", repoMakefile, m[1], ciTestWorkflow)
			assert.Contains(t, recipe, "-timeout",
				"%s delegates to `make %s`, but that target runs without a -timeout", ciTestWorkflow, m[1])
		}
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
// the timeout fix must not quietly drop -race, in the workflow or in any
// Makefile target it delegates to.
func TestCIRaceJobRunsTheRaceDetector(t *testing.T) {
	workflow, err := os.ReadFile(ciTestWorkflow)
	require.NoError(t, err, "reading the CI workflow")

	body := string(workflow)
	targets := makeRaceTarget.FindAllStringSubmatch(body, -1)
	require.True(t, len(targets) > 0 || strings.Contains(body, "go test -race"),
		"%s no longer runs the suite under the race detector", ciTestWorkflow)

	makefile, err := os.ReadFile(repoMakefile)
	require.NoError(t, err, "reading the Makefile")
	for _, m := range targets {
		assert.Contains(t, makeTargetRecipe(string(makefile), m[1]), "go test -race",
			"%s delegates to `make %s`, which does not run the race detector", ciTestWorkflow, m[1])
	}
}

// TestCIRunsBothHalvesOfTheE2ESplit is #576: CI runs the serve browser E2E
// tests in their own job, and everything else in another, so the two run
// side by side. The halves are only the whole suite together - a workflow
// that runs one without the other silently drops the rest - and only
// disjoint and complete if both select on the same pattern, with -run and
// -skip respectively.
func TestCIRunsBothHalvesOfTheE2ESplit(t *testing.T) {
	workflow, err := os.ReadFile(ciTestWorkflow)
	require.NoError(t, err, "reading the CI workflow")
	body := string(workflow)
	runsE2E := strings.Contains(body, "make test-race-e2e")
	runsRest := strings.Contains(body, "make test-race-no-e2e")
	if !runsE2E && !runsRest {
		t.Skip("CI does not split the suite")
	}
	require.True(t, runsE2E && runsRest,
		"%s runs only one half of the E2E split; it needs both `make test-race-e2e` and `make test-race-no-e2e`", ciTestWorkflow)

	makefile, err := os.ReadFile(repoMakefile)
	require.NoError(t, err, "reading the Makefile")
	assert.Contains(t, makeTargetRecipe(string(makefile), "test-race-e2e"), "-run '$(E2E_PATTERN)'",
		"test-race-e2e must select exactly the E2E_PATTERN tests")
	assert.Contains(t, makeTargetRecipe(string(makefile), "test-race-no-e2e"), "-skip '$(E2E_PATTERN)' ./...",
		"test-race-no-e2e must run every package, skipping exactly the E2E_PATTERN tests")
}

// TestCIRaceJobKeepsTheRestoredBuildCache is P1b review F7. Routing the race
// job through `make test-race` (#374) fixed the timeout but pinned
// GOCACHE to the project-local .go-mod/cache, which exists so the suite runs
// in sandboxes with no writable HOME. On CI that means the build cache
// actions/setup-go just restored at ~/.cache/go-build is never read: every
// run recompiles the module and its dependencies under -race from cold.
//
// The Makefile's GOCACHE_LOCAL must therefore be overridable, and the
// workflow must override it with the cache setup-go actually restored.
func TestCIRaceJobKeepsTheRestoredBuildCache(t *testing.T) {
	workflow, err := os.ReadFile(ciTestWorkflow)
	require.NoError(t, err, "reading the CI workflow")
	body := string(workflow)
	if !strings.Contains(body, "make test-race") {
		t.Skip("the race job no longer delegates to make; GOCACHE is Go's own default there")
	}

	makefile, err := os.ReadFile(repoMakefile)
	require.NoError(t, err, "reading the Makefile")
	assert.Regexp(t, `(?m)^GOCACHE_LOCAL \?=`, string(makefile),
		"GOCACHE_LOCAL must use ?= so CI can point it at the cache setup-go restored")

	steps := regexp.MustCompile(`(?m)^\s*run: make test-race.*$`).FindAllString(body, -1)
	require.NotEmpty(t, steps, "%s names make test-race but has no step running it", ciTestWorkflow)
	for _, line := range steps {
		assert.Contains(t, line, "GOCACHE_LOCAL=$(go env GOCACHE)",
			"%s runs %q without restoring setup-go's build cache, so every run recompiles from cold", ciTestWorkflow, strings.TrimSpace(line))
	}
}
