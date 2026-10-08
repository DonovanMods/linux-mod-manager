package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// releaseWorkflow publishes a pushed tag; its "Extract release notes"
	// step cuts the tag's own section out of CHANGELOG.md for the GitHub
	// release body.
	releaseWorkflow = "../../.github/workflows/release.yml"
	repoChangelog   = "../../CHANGELOG.md"
)

// releaseNotesScript is the release_notes step's `run: |` block, dedented:
// the shell the workflow actually runs, so the tests below run it too rather
// than a copy that could drift.
func releaseNotesScript(t *testing.T) string {
	t.Helper()
	workflow, err := os.ReadFile(releaseWorkflow)
	require.NoError(t, err, "reading the release workflow")
	block := regexp.MustCompile(`(?s)id: release_notes\n\s*run: \|\n(.*?)\n\n`).FindStringSubmatch(string(workflow))
	require.Len(t, block, 2, "%s has no release_notes step with a run: | block", releaseWorkflow)
	lines := strings.Split(block[1], "\n")
	indent := len(lines[0]) - len(strings.TrimLeft(lines[0], " "))
	for i, line := range lines {
		if len(line) >= indent {
			lines[i] = line[indent:]
		}
	}
	return strings.Join(lines, "\n")
}

// runReleaseNotes runs the step for tag against changelog in a scratch
// workspace, the way Actions does: bash -eo pipefail, GITHUB_REF_NAME,
// RUNNER_TEMP and GITHUB_OUTPUT set. It returns the extracted notes (empty
// when the step failed) and the step's error.
func runReleaseNotes(t *testing.T, tag string, changelog []byte) (string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), changelog, 0o644))
	output := filepath.Join(dir, "github_output")
	cmd := exec.Command(bash, "--noprofile", "--norc", "-eo", "pipefail", "-c", releaseNotesScript(t))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GITHUB_REF_NAME="+tag, "RUNNER_TEMP="+dir, "GITHUB_OUTPUT="+output)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return "", &releaseNotesError{err: runErr, output: string(out)}
	}
	kv, err := os.ReadFile(output)
	require.NoError(t, err)
	path := strings.TrimPrefix(strings.TrimSpace(string(kv)), "path=")
	notes, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(notes), nil
}

// releaseNotesError carries the step's own output next to its exit status.
type releaseNotesError struct {
	err    error
	output string
}

func (e *releaseNotesError) Error() string { return e.err.Error() + ": " + e.output }

// TestReleaseNotes_ExtractsASectionThatIsNotTheLast is the step's main job:
// a release's section sits above every older one, so the extraction must
// stop at the next "## [" heading and SUCCEED. A large tail after the
// section is what makes a `tail | awk '{exit}'` pipeline die of SIGPIPE
// under pipefail (exit 141) - the shape this guards against.
func TestReleaseNotes_ExtractsASectionThatIsNotTheLast(t *testing.T) {
	var b strings.Builder
	b.WriteString("# Changelog\n\n## [Unreleased]\n\n- pending\n\n## [2.0.0] - 2026-08-30\n\n### Added\n\n- the new thing\n\n")
	for i := range 20000 {
		b.WriteString("## [1.0.")
		b.WriteString(strings.Repeat("9", i%3+1))
		b.WriteString("] - older\n\n- an older entry with enough text to fill the pipe buffer\n\n")
	}

	notes, err := runReleaseNotes(t, "v2.0.0", []byte(b.String()))
	require.NoError(t, err)
	assert.Contains(t, notes, "- the new thing")
	assert.NotContains(t, notes, "## [", "the section ends at the next heading")
	assert.NotContains(t, notes, "older entry")
}

// TestReleaseNotes_ExtractsTheNewestReleaseFromTheRealChangelog runs the step
// against the repository's own CHANGELOG.md for its newest released version.
func TestReleaseNotes_ExtractsTheNewestReleaseFromTheRealChangelog(t *testing.T) {
	changelog, err := os.ReadFile(repoChangelog)
	require.NoError(t, err)
	newest := regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\]`).FindSubmatch(changelog)
	require.Len(t, newest, 2, "CHANGELOG.md has no released version heading")

	notes, err := runReleaseNotes(t, "v"+string(newest[1]), changelog)
	require.NoError(t, err)
	assert.NotEmpty(t, strings.TrimSpace(notes))
	assert.NotContains(t, notes, "\n## [")
}

// TestReleaseNotes_FailsForATagWithNoSection keeps the step's refusal: a tag
// with no CHANGELOG section must fail the release, never publish an empty or
// generated body.
func TestReleaseNotes_FailsForATagWithNoSection(t *testing.T) {
	_, err := runReleaseNotes(t, "v9.9.9", []byte("# Changelog\n\n## [1.0.0] - 2026-01-01\n\n- one\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "No '## [9.9.9]' heading")
}
