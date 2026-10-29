package main

import (
	"os"
	"os/exec"
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
	assert.Equal(t, map[string]string{"contents": "read"}, wf.Permissions)

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
