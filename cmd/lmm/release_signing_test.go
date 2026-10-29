package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/releasesig"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// release_signing_test.go - the release workflow signs checksums.txt
// (#569), which `lmm self-update` refuses a release without.

// checkKeyWorkflow is the manual "Check release signing key" workflow.
const checkKeyWorkflow = "../../.github/workflows/check-release-key.yml"

// TestRelease_GoreleaserSignsTheChecksums: one signs entry, over the
// checksum artifact, running tools/releasekey with the secret inherited
// from GoReleaser's environment, writing checksums.txt.sig.
func TestRelease_GoreleaserSignsTheChecksums(t *testing.T) {
	raw, err := os.ReadFile(goreleaserConfig)
	require.NoError(t, err)
	var cfg struct {
		Signs []struct {
			Artifacts string   `yaml:"artifacts"`
			Cmd       string   `yaml:"cmd"`
			Args      []string `yaml:"args"`
			Signature string   `yaml:"signature"`
			Env       []string `yaml:"env"`
		} `yaml:"signs"`
		Checksum struct {
			NameTemplate string `yaml:"name_template"`
		} `yaml:"checksum"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &cfg))
	require.Len(t, cfg.Signs, 1)
	s := cfg.Signs[0]
	assert.Equal(t, "checksum", s.Artifacts)
	assert.Equal(t, "go", s.Cmd)
	assert.Equal(t, []string{"run", "./tools/releasekey", "sign", "-in", "${artifact}", "-out", "${signature}"}, s.Args)
	assert.Equal(t, "${artifact}.sig", s.Signature)
	assert.Empty(t, s.Env, "the command inherits the key; a templated env entry cannot see it and fails the release")
	assert.Equal(t, "checksums.txt", cfg.Checksum.NameTemplate, "self-update downloads checksums.txt and checksums.txt.sig by name")
}

// TestRelease_WorkflowRequiresTheSigningKey: GoReleaser gets the secret,
// and the run fails before building anything when it is not set - an
// unsigned release is one every self-update refuses.
func TestRelease_WorkflowRequiresTheSigningKey(t *testing.T) {
	raw, err := os.ReadFile(releaseWorkflow)
	require.NoError(t, err)
	workflow := string(raw)
	assert.Equal(t, 2, strings.Count(workflow, "LMM_RELEASE_SIGNING_KEY: ${{ secrets.LMM_RELEASE_SIGNING_KEY }}"),
		"the guard step and the GoReleaser step each get the secret")
	guard := strings.Index(workflow, `if [ -z "${LMM_RELEASE_SIGNING_KEY}" ]`)
	require.NotEqual(t, -1, guard, "a step fails when the secret is empty")
	assert.Less(t, guard, strings.Index(workflow, "goreleaser/goreleaser-action"), "and it runs before GoReleaser")
}

// TestCheckReleaseKeyWorkflow_PrintsOnlyFAILForAKeyLmmDoesNotTrust runs
// the workflow's script as Actions would, with a key lmm does not trust:
// it fails, and prints FAIL and nothing of the key.
func TestCheckReleaseKeyWorkflow_PrintsOnlyFAILForAKeyLmmDoesNotTrust(t *testing.T) {
	if testing.Short() {
		t.Skip("builds tools/releasekey")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	raw, err := os.ReadFile(checkKeyWorkflow)
	require.NoError(t, err)
	var wf struct {
		On          map[string]any `yaml:"on"`
		Permissions map[string]string
		Jobs        map[string]struct {
			Steps []struct {
				Env map[string]string `yaml:"env"`
				Run string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &wf))
	require.Len(t, wf.On, 1)
	assert.Contains(t, wf.On, "workflow_dispatch", "it runs only when started by hand")
	assert.Empty(t, wf.Permissions, "each job names its own permissions")

	var script string
	for _, job := range wf.Jobs {
		for _, step := range job.Steps {
			if step.Env["LMM_RELEASE_SIGNING_KEY"] == "${{ secrets.LMM_RELEASE_SIGNING_KEY }}" {
				script = step.Run
			}
		}
	}
	require.NotEmpty(t, script, "a step runs with the secret")
	assert.Contains(t, script, `"$tool" trusted`, "each key compiled into lmm")
	assert.Contains(t, script, "verify -pub")

	run := func(key string) (string, error) {
		cmd := exec.Command(bash, "--noprofile", "--norc", "-eo", "pipefail", "-c", script)
		cmd.Dir = "../.."
		cmd.Env = append(os.Environ(), "LMM_RELEASE_SIGNING_KEY="+key, "RUNNER_TEMP="+t.TempDir(), "GITHUB_RUN_ID=1")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	priv, _, err := releasesig.Generate()
	require.NoError(t, err)
	out, err := run(priv)
	require.Error(t, err, "a key lmm does not trust fails the check")
	assert.Equal(t, "FAIL\n", out)
	assert.NotRegexp(t, regexp.MustCompile(regexp.QuoteMeta(priv)), out)

	out, err = run("")
	require.Error(t, err, "no secret at all fails too")
	assert.Equal(t, "FAIL\n", out)
}

// workflowJob is the part of a workflow job these tests read.
type workflowJob struct {
	If          string            `yaml:"if"`
	Needs       any               `yaml:"needs"`
	Environment any               `yaml:"environment"`
	Permissions map[string]string `yaml:"permissions"`
	Steps       []struct {
		Name string            `yaml:"name"`
		Env  map[string]string `yaml:"env"`
		Run  string            `yaml:"run"`
		Uses string            `yaml:"uses"`
	} `yaml:"steps"`
}

// readWorkflow parses a workflow file's top-level permissions and jobs.
// Permissions is a pointer so an explicit "permissions: {}" reads as an
// empty map, never as absent.
func readWorkflow(t *testing.T, path string) (*map[string]string, map[string]workflowJob, string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var wf struct {
		Permissions *map[string]string     `yaml:"permissions"`
		Jobs        map[string]workflowJob `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &wf))
	return wf.Permissions, wf.Jobs, string(raw)
}

// usesSigningKey reports whether any step of job is handed the secret.
func usesSigningKey(job workflowJob) bool {
	for _, step := range job.Steps {
		if strings.Contains(step.Env["LMM_RELEASE_SIGNING_KEY"], "secrets.LMM_RELEASE_SIGNING_KEY") {
			return true
		}
	}
	return false
}

// TestRelease_SigningRunsOnlyInTheReleaseEnvironment: the workflow grants
// nothing by default; the job holding the signing key runs in the
// "release" environment (where the secret lives, behind a reviewer) with
// contents: write alone, and only after a job without the key has checked
// the tagged commit is on main.
func TestRelease_SigningRunsOnlyInTheReleaseEnvironment(t *testing.T) {
	perms, jobs, _ := readWorkflow(t, releaseWorkflow)
	require.NotNil(t, perms, "top-level permissions are stated")
	assert.Empty(t, *perms, "and grant nothing")

	var keyed, guards []string
	for name, job := range jobs {
		if usesSigningKey(job) {
			keyed = append(keyed, name)
			continue
		}
		for _, step := range job.Steps {
			if strings.Contains(step.Run, `git merge-base --is-ancestor "$GITHUB_SHA" origin/main`) {
				guards = append(guards, name)
			}
		}
	}
	require.Len(t, keyed, 1, "one job holds the key")
	require.Len(t, guards, 1, "one job without the key checks the tag is on main")
	release, guard := jobs[keyed[0]], jobs[guards[0]]
	assert.Equal(t, "release", release.Environment)
	assert.Equal(t, map[string]string{"contents": "write"}, release.Permissions)
	assert.Contains(t, fmt.Sprint(release.Needs), guards[0], "the release waits on the main check")
	assert.Nil(t, guard.Environment, "the check needs no approval and no secret")
	assert.Equal(t, map[string]string{"contents": "read"}, guard.Permissions)
}

// mainGuardScript is the release workflow's main-reachability step.
func mainGuardScript(t *testing.T) string {
	t.Helper()
	_, jobs, _ := readWorkflow(t, releaseWorkflow)
	for _, job := range jobs {
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "git merge-base --is-ancestor") {
				return step.Run
			}
		}
	}
	t.Fatal("no main-reachability step")
	return ""
}

// TestRelease_RefusesATagWhoseCommitIsNotOnMain runs the guard step against
// a scratch repository whose origin has a main branch: a commit on main
// passes, a commit only on another branch fails.
func TestRelease_RefusesATagWhoseCommitIsNotOnMain(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	origin, work := filepath.Join(root, "origin.git"), filepath.Join(root, "work")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v: %s", args, out)
		return strings.TrimSpace(string(out))
	}
	git(root, "init", "-q", "--bare", "-b", "main", origin)
	git(root, "clone", "-q", origin, work)
	git(work, "commit", "-q", "--allow-empty", "-m", "on main")
	git(work, "push", "-q", "origin", "HEAD:main")
	onMain := git(work, "rev-parse", "HEAD")
	git(work, "checkout", "-q", "-b", "side")
	git(work, "commit", "-q", "--allow-empty", "-m", "off main")
	git(work, "push", "-q", "origin", "side")
	offMain := git(work, "rev-parse", "HEAD")

	script := mainGuardScript(t)
	run := func(sha string) (string, error) {
		cmd := exec.Command(bash, "--noprofile", "--norc", "-eo", "pipefail", "-c", script)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "GITHUB_SHA="+sha, "GITHUB_REF_NAME=v9.9.9", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	out, err := run(onMain)
	require.NoError(t, err, out)
	out, err = run(offMain)
	require.Error(t, err, "a tag on a commit main does not contain is refused")
	assert.Contains(t, out, "not on main")
}

// TestCheckReleaseKeyWorkflow_RunsOnlyFromMain: the key check runs in the
// release environment, from main only - a run dispatched from any other
// ref fails in a job that has no key - with read access alone.
func TestCheckReleaseKeyWorkflow_RunsOnlyFromMain(t *testing.T) {
	perms, jobs, _ := readWorkflow(t, checkKeyWorkflow)
	require.NotNil(t, perms)
	assert.Empty(t, *perms)

	var keyed, refusals []string
	for name, job := range jobs {
		if usesSigningKey(job) {
			keyed = append(keyed, name)
		} else {
			refusals = append(refusals, name)
		}
	}
	require.Len(t, keyed, 1)
	check := jobs[keyed[0]]
	assert.Equal(t, "release", check.Environment)
	assert.Equal(t, map[string]string{"contents": "read"}, check.Permissions)
	assert.Equal(t, "github.ref == 'refs/heads/main'", check.If)

	require.Len(t, refusals, 1, "another ref is refused, loudly, by a job without the key")
	refuse := jobs[refusals[0]]
	assert.Equal(t, "github.ref != 'refs/heads/main'", refuse.If)
	assert.Nil(t, refuse.Environment)
	assert.Empty(t, refuse.Permissions)
	require.Len(t, refuse.Steps, 1)
	assert.Contains(t, refuse.Steps[0].Run, "exit 1")
}
