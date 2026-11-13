package main

// #615: the CLI's FOMOD answers - the terminal walk, --fomod-choices,
// --fomod-defaults, and the refusal when nothing can answer - through
// `lmm install`, `lmm mod fomod` and the update batch's skip line.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliFomodConfig: step one picks a texture (2K, 4K; 4K sets a flag), step
// two is visible only for 4K and recommends a readme.
const cliFomodConfig = `<config><moduleName>CLI Installer</moduleName>
<requiredInstallFiles><file source="core\plugin.esp" destination="plugin.esp"/></requiredInstallFiles>
<installSteps order="Explicit">
<installStep name="Size"><optionalFileGroups order="Explicit">
<group name="Texture" type="SelectExactlyOne"><plugins order="Explicit">
<plugin name="2K"><description>Light on VRAM.</description><files><file source="tex\2k.dds" destination="textures\t.dds"/></files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
<plugin name="4K"><description>Heavy.</description><files><file source="tex\4k.dds" destination="textures\t.dds"/></files><conditionFlags><flag name="big">on</flag></conditionFlags><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
</plugins></group></optionalFileGroups></installStep>
<installStep name="Extras"><visible><flagDependency flag="big" value="on"/></visible><optionalFileGroups order="Explicit">
<group name="Docs" type="SelectAny"><plugins order="Explicit">
<plugin name="Readme"><files><file source="docs\readme.txt" destination="readme.txt"/></files><typeDescriptor><type name="Recommended"/></typeDescriptor></plugin>
</plugins></group></optionalFileGroups></installStep>
</installSteps></config>`

func cliFomodZip(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mod.zip")
	createTestArchive(t, path, map[string]string{
		"fomod/ModuleConfig.xml": cliFomodConfig,
		"core/plugin.esp":        "PLUGIN",
		"tex/2k.dds":             "2K",
		"tex/4k.dds":             "4K",
		"docs/readme.txt":        "README",
	})
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return b
}

// setupFomodInstall serves mod1 as a FOMOD archive and resets the FOMOD
// flags and the walk's seams.
func setupFomodInstall(t *testing.T) (*core.Service, *domain.Game, *fakeInstallSource) {
	t.Helper()
	svc, game, src := setupDoInstallTest(t)
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", FileName: "mod1.zip", IsPrimary: true}})
	src.AddDownload("main", cliFomodZip(t))
	oldPath, oldDefaults, oldIn, oldTerm, oldJSON := fomodChoicesPath, fomodDefaults, fomodStdin, fomodIsTerminal, jsonOutput
	fomodChoicesPath, fomodDefaults, jsonOutput = "", false, false
	fomodIsTerminal = func() bool { return false }
	t.Cleanup(func() {
		fomodChoicesPath, fomodDefaults, fomodStdin, fomodIsTerminal, jsonOutput = oldPath, oldDefaults, oldIn, oldTerm, oldJSON
	})
	return svc, game, src
}

// terminalInput makes the walk read lines from a fake terminal.
func terminalInput(lines ...string) {
	fomodIsTerminal = func() bool { return true }
	fomodStdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
}

func deployedFile(t *testing.T, game *domain.Game, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(game.ModPath, filepath.FromSlash(rel)))
	if os.IsNotExist(err) {
		return ""
	}
	require.NoError(t, err)
	return string(b)
}

func TestDoInstall_FomodWithNoTerminalAndNoFlagRefuses(t *testing.T) {
	svc, game, _ := setupFomodInstall(t)

	_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	assert.NotZero(t, exitCodeFor(err), "never silent defaults: a non-zero exit")
	assert.Empty(t, deployedFile(t, game, "plugin.esp"), "nothing was installed")

	// The text error names the remedies; --json carries the document.
	stderr := captureStderr(t, func() { reportError(err) })
	assert.Contains(t, stderr, `Group "Texture" - choose exactly one: "2K"* "4K"`)
	assert.Contains(t, stderr, "--fomod-choices <file.json> or --fomod-defaults")

	jsonOutput = true
	out := captureStdout(t, func() error { reportError(err); return nil })
	var env struct {
		Error   string                         `json:"error"`
		Details core.FomodChoicesRequiredError `json:"details"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &env), out)
	require.Len(t, env.Details.Mods, 1)
	assert.Equal(t, "CLI Installer", env.Details.Mods[0].ModuleName)
}

func TestDoInstall_FomodDefaultsInstallsThePreselection(t *testing.T) {
	svc, game, _ := setupFomodInstall(t)
	fomodDefaults = true

	_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Equal(t, "2K", deployedFile(t, game, "textures/t.dds"), "the first of an exactly-one group")
	assert.Equal(t, "PLUGIN", deployedFile(t, game, "plugin.esp"))
}

func TestDoInstall_FomodChoicesFileAnswers(t *testing.T) {
	for name, body := range map[string]string{
		"vortex array": `[{"name":"Size","groups":[{"name":"Texture","choices":[{"name":"4K","idx":1}]}]},
		                  {"name":"Extras","groups":[{"name":"Docs","choices":[]}]}]`,
		"profile shape": `{"steps":[{"name":"Size","groups":[{"name":"Texture","choices":[{"name":"4K","idx":1}]}]},
		                  {"name":"Extras","groups":[{"name":"Docs","choices":[]}]}]}`,
		"keyed by mod": `{"test-src:mod1":[{"name":"Size","groups":[{"name":"Texture","choices":[{"name":"4K","idx":1}]}]},
		                  {"name":"Extras","groups":[{"name":"Docs","choices":[]}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			svc, game, src := setupFomodInstall(t)
			fomodChoicesPath = filepath.Join(t.TempDir(), "choices.json")
			require.NoError(t, os.WriteFile(fomodChoicesPath, []byte(body), 0o600))

			_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
			require.NoError(t, err)
			assert.Equal(t, "4K", deployedFile(t, game, "textures/t.dds"))
			assert.Empty(t, deployedFile(t, game, "readme.txt"), "the file chose no docs")
			assert.Equal(t, 1, src.DownloadCount(), "the answer re-run used the kept download")
		})
	}
}

func TestDoInstall_FomodChoicesFileThatDoesNotSettleRefuses(t *testing.T) {
	svc, game, _ := setupFomodInstall(t)
	fomodChoicesPath = filepath.Join(t.TempDir(), "choices.json")
	// 4K makes the Extras step visible, which the file does not answer.
	require.NoError(t, os.WriteFile(fomodChoicesPath, []byte(`[{"name":"Size","groups":[{"name":"Texture","choices":[{"name":"4K","idx":1}]}]}]`), 0o600))

	_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	assert.Equal(t, []core.FomodGroupRef{{Step: "Extras", Group: "Docs"}}, q.Mods[0].Unanswered)
}

// TestDoInstall_FomodWalk: the walk keeps going while the user keeps the
// preselection, and goes back to core when a pick changes what later steps
// show - here 4K reveals the Extras step, asked in a second round with its
// Recommended option preselected.
func TestDoInstall_FomodWalk(t *testing.T) {
	svc, game, src := setupFomodInstall(t)
	terminalInput("2", "") // 4K, then keep Extras' preselection

	out, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.NoError(t, err, out)
	assert.Contains(t, out, "Mod One: CLI Installer")
	assert.Contains(t, out, "Step 1 of 1: Size")
	assert.Contains(t, out, "Light on VRAM.", "option descriptions are shown")
	assert.Contains(t, out, "Mod One: CLI Installer - continued")
	assert.Contains(t, out, "Step 2 of 2: Extras")
	assert.Contains(t, out, "1) [x] Readme (recommended)")
	assert.Equal(t, "4K", deployedFile(t, game, "textures/t.dds"))
	assert.Equal(t, "README", deployedFile(t, game, "readme.txt"))
	assert.Equal(t, 1, src.DownloadCount())
}

func TestDoInstall_FomodWalkEnforcesTheGroupRule(t *testing.T) {
	svc, game, _ := setupFomodInstall(t)
	terminalInput("1,2", "none", "1") // two, then none, then a valid one

	out, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.NoError(t, err, out)
	assert.Equal(t, 2, strings.Count(out, "Choose exactly one."), out)
	assert.Equal(t, "2K", deployedFile(t, game, "textures/t.dds"))
}

func TestDoInstall_FomodWalkCancels(t *testing.T) {
	svc, game, _ := setupFomodInstall(t)
	terminalInput("q")

	_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	assert.ErrorIs(t, err, ErrCancelled)
	assert.Empty(t, deployedFile(t, game, "plugin.esp"))
}

// TestDoInstallBatch_FomodOnTheResult: a dependency install reports the
// question on its result (nothing written); with no answer the summary
// shows it and the exit is non-zero, and --fomod-defaults answers it.
func TestDoInstallBatch_FomodOnTheResult(t *testing.T) {
	svc, game, src := setupFomodInstall(t)
	src.AddMod(&domain.Mod{ID: "root", SourceID: "test-src", Name: "Root", Version: "1.0", GameID: "g1",
		Dependencies: []domain.ModReference{{SourceID: "test-src", ModID: "mod1"}}},
		[]domain.DownloadableFile{{ID: "root-file", FileName: "root.esp", IsPrimary: true}})
	src.AddDownload("root-file", []byte("ROOT"))
	installModID = "root"

	out, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.ErrorIs(t, err, ErrReported)
	assert.NotZero(t, exitCodeFor(err))
	assert.Contains(t, out, "Nothing was installed")
	assert.Contains(t, out, `Group "Texture"`)
	assert.Empty(t, deployedFile(t, game, "root.esp"))

	fomodDefaults = true
	_, err = captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	assert.Equal(t, "ROOT", deployedFile(t, game, "root.esp"))
	assert.Equal(t, "2K", deployedFile(t, game, "textures/t.dds"))
}

func TestDoModFomod_ReconfiguresTheInstalledMod(t *testing.T) {
	svc, game, _ := setupFomodInstall(t)
	fomodDefaults = true
	_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	require.Equal(t, "2K", deployedFile(t, game, "textures/t.dds"))
	fomodDefaults = false

	oldSource := modSource
	modSource = "test-src"
	t.Cleanup(func() { modSource = oldSource })

	// No terminal, no flag: the question, preselected from the recording.
	_, err = captureStdoutErr(t, func() error { return doModFomod(context.Background(), svc, game, "mod1") })
	var q *core.FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	assert.Equal(t, core.FomodReasonReconfigure, q.Mods[0].Reason)
	assert.Equal(t, "2K", deployedFile(t, game, "textures/t.dds"), "nothing changed")

	terminalInput("2", "none")
	out, err := captureStdoutErr(t, func() error { return doModFomod(context.Background(), svc, game, "mod1") })
	require.NoError(t, err, out)
	assert.Contains(t, out, "your recorded ones are preselected")
	assert.Contains(t, out, "✓ Reconfigured: Mod One 1.0")
	assert.Equal(t, "4K", deployedFile(t, game, "textures/t.dds"))
	assert.Empty(t, deployedFile(t, game, "readme.txt"))
}

func TestParseFomodChoicesFile(t *testing.T) {
	keyed, single, err := parseFomodChoicesFile([]byte(`{"nexusmods:1":{"config_sha256":"ab","steps":[]},"nexusmods:2":[]}`))
	require.NoError(t, err)
	assert.Nil(t, single)
	assert.Len(t, keyed, 2)
	assert.Equal(t, "ab", keyed["nexusmods:1"].ConfigSHA256)

	for name, body := range map[string]string{
		"empty":         ``,
		"not a mod key": `{"Main": []}`,
		"unknown field": `{"steps": [], "colour": 1}`,
		"not json":      `steps`,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := parseFomodChoicesFile([]byte(body))
			assert.Error(t, err)
		})
	}
}

func TestPrintBatchSkips_FomodReanswer(t *testing.T) {
	out := captureStdout(t, func() error {
		printBatchSkips([]core.UpdateApplyResult{{Name: "Mod One", Mod: domain.ModReference{SourceID: "nexusmods", ModID: "12604"},
			Status: core.UpdateSkipped, Reason: core.ReasonFomodReanswer, FomodReanswer: true}})
		return nil
	})
	assert.Contains(t, out, "1 mod(s) not applied — their installer (FOMOD) needs answering")
	assert.Contains(t, out, "lmm update 12604 --source nexusmods")
	assert.NotContains(t, out, "Steam Workshop")
}

// fiveStepConfig: five steps, each an exactly-one group of A (the default)
// and B, each installing its pick as s<N>.txt.
func fiveStepConfig() string {
	var b strings.Builder
	b.WriteString(`<config><moduleName>Five Steps</moduleName><installSteps order="Explicit">`)
	for i := 1; i <= 5; i++ {
		n := strconv.Itoa(i)
		b.WriteString(`<installStep name="S` + n + `"><optionalFileGroups order="Explicit"><group name="G` + n + `" type="SelectExactlyOne"><plugins order="Explicit">`)
		for _, opt := range []string{"A", "B"} {
			b.WriteString(`<plugin name="` + opt + `"><files><file source="` + strings.ToLower(opt) + `.txt" destination="s` + n + `.txt"/></files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>`)
		}
		b.WriteString(`</plugins></group></optionalFileGroups></installStep>`)
	}
	b.WriteString(`</installSteps></config>`)
	return b.String()
}

// TestDoModFomod_ChangingOneStepKeepsTheOtherRecordedPicks: a reconfigure
// that changes step 1 of 5 and accepts the rest keeps steps 2-5's recorded
// picks - the re-asked steps are preselected from the recording, not from
// the installer's defaults (#615 review item 3).
func TestDoModFomod_ChangingOneStepKeepsTheOtherRecordedPicks(t *testing.T) {
	svc, game, src := setupDoInstallTest(t)
	path := filepath.Join(t.TempDir(), "five.zip")
	createTestArchive(t, path, map[string]string{"fomod/ModuleConfig.xml": fiveStepConfig(), "a.txt": "A", "b.txt": "B"})
	archive, err := os.ReadFile(path)
	require.NoError(t, err)
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Five", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", FileName: "five.zip", IsPrimary: true}})
	src.AddDownload("main", archive)
	oldPath, oldDefaults, oldIn, oldTerm := fomodChoicesPath, fomodDefaults, fomodStdin, fomodIsTerminal
	t.Cleanup(func() {
		fomodChoicesPath, fomodDefaults, fomodStdin, fomodIsTerminal = oldPath, oldDefaults, oldIn, oldTerm
	})
	oldSource := modSource
	modSource = "test-src"
	t.Cleanup(func() { modSource = oldSource })

	// Record B everywhere.
	terminalInput("2", "2", "2", "2", "2")
	_, err = captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	for i := 1; i <= 5; i++ {
		require.Equal(t, "B", deployedFile(t, game, "s"+strconv.Itoa(i)+".txt"))
	}

	// Reconfigure: change step 1 to A, press Enter through the rest.
	terminalInput("1", "", "", "", "")
	out, err := captureStdoutErr(t, func() error { return doModFomod(context.Background(), svc, game, "mod1") })
	require.NoError(t, err, out)
	assert.Equal(t, "A", deployedFile(t, game, "s1.txt"))
	for i := 2; i <= 5; i++ {
		assert.Equal(t, "B", deployedFile(t, game, "s"+strconv.Itoa(i)+".txt"), "step %d keeps its recorded pick", i)
	}
}

// TestApplyUpdateBatch_FomodFlagsAnswerTheSkips: the update batch never
// asks, but --fomod-defaults answers the questions its skips carry and the
// batch runs again over just those mods (#615 review item 5).
func TestApplyUpdateBatch_FomodFlagsAnswerTheSkips(t *testing.T) {
	svc, game, src := setupFomodInstall(t)
	fomodDefaults = true
	_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	require.Equal(t, "2K", deployedFile(t, game, "textures/t.dds"))

	// 1.1 renames the recorded option: the recording no longer fits.
	path := filepath.Join(t.TempDir(), "v11.zip")
	createTestArchive(t, path, map[string]string{
		"fomod/ModuleConfig.xml": strings.Replace(cliFomodConfig, `plugin name="2K"`, `plugin name="Small"`, 1),
		"core/plugin.esp":        "PLUGIN11", "tex/2k.dds": "2K11", "tex/4k.dds": "4K11", "docs/readme.txt": "README",
	})
	archive, err := os.ReadFile(path)
	require.NoError(t, err)
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.1", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main11", FileName: "mod1-1.1.zip", Version: "1.1", IsPrimary: true}})
	src.AddDownload("main11", archive)
	row, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "default")
	require.NoError(t, err)
	updates := []domain.Update{{InstalledMod: *row, NewVersion: "1.1"}}
	keys := []string{"test-src:mod1"}

	// Without an answer the mod is skipped, with its question.
	fomodDefaults = false
	var result *core.UpdateBatchResult
	_, err = captureStdoutErr(t, func() error {
		var e error
		result, e = applyUpdateBatch(context.Background(), svc, game, "default", updates, keys, 1)
		return e
	})
	require.NoError(t, err)
	require.Len(t, result.Skipped, 1)
	require.NotNil(t, result.Skipped[0].FomodChoicesRequired)
	assert.Equal(t, "2K", deployedFile(t, game, "textures/t.dds"))

	// --fomod-defaults answers it, and the batch applies the update.
	fomodDefaults = true
	_, err = captureStdoutErr(t, func() error {
		var e error
		result, e = applyUpdateBatch(context.Background(), svc, game, "default", updates, keys, 1)
		return e
	})
	require.NoError(t, err)
	assert.Empty(t, result.Skipped)
	require.Len(t, result.Applied, 1)
	assert.Equal(t, "2K11", deployedFile(t, game, "textures/t.dds"), "the installer's default (its first option) at 1.1")
}

// TestDoModFomod_DefaultsResetToTheInstallersOwn: --fomod-defaults on a
// reconfigure installs the installer's defaults, not the recorded choices
// the question is preselected with (#615 review item 7).
func TestDoModFomod_DefaultsResetToTheInstallersOwn(t *testing.T) {
	svc, game, _ := setupFomodInstall(t)
	fomodChoicesPath = filepath.Join(t.TempDir(), "choices.json")
	require.NoError(t, os.WriteFile(fomodChoicesPath, []byte(`[{"name":"Size","groups":[{"name":"Texture","choices":[{"name":"4K","idx":1}]}]},
		{"name":"Extras","groups":[{"name":"Docs","choices":[]}]}]`), 0o600))
	_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	require.Equal(t, "4K", deployedFile(t, game, "textures/t.dds"))

	oldSource := modSource
	modSource = "test-src"
	t.Cleanup(func() { modSource = oldSource })
	fomodChoicesPath, fomodDefaults = "", true
	_, err = captureStdoutErr(t, func() error { return doModFomod(context.Background(), svc, game, "mod1") })
	require.NoError(t, err)
	assert.Equal(t, "2K", deployedFile(t, game, "textures/t.dds"), "the installer's default, not the recorded 4K")
	assert.Empty(t, deployedFile(t, game, "readme.txt"), "the Extras step is gone with 4K")
}

// TestFomodWalk_SanitisesInstallerTextButAnswersRawNames: an installer's
// names and descriptions are untrusted - a C1 CSI (U+009B), a bidi override
// or an ESC sequence never reaches the terminal - while the answer echoes
// the raw names, which is what the installer matches (#615 review item 6).
func TestFomodWalk_SanitisesInstallerTextButAnswersRawNames(t *testing.T) {
	rlo := string(rune(0x202E)) // a bidi override, spelled so no tool rewrites it
	evil := "Opt\u009b2J" + rlo + "A\x1b]0;pwned\x07"
	q := core.FomodQuestion{
		SourceID: "src", ModID: "m", Name: "Mod\x1b[31m", ModuleName: "Inst" + rlo, ConfigSHA256: "ab",
		Steps: []core.FomodQuestionStep{{Name: "Step\u009b", Groups: []core.FomodQuestionGroup{{
			Name: "Group\x1b[2J", Type: "SelectExactlyOne",
			Options: []core.FomodQuestionOption{
				{Name: evil, Type: "Optional", Selected: true, Description: "line one\u009b\nline" + rlo + " two"},
				{Name: "Plain", Type: "Optional"},
			},
		}}}},
		Choices: []domain.FomodStepChoice{{Name: "Step\u009b", Groups: []domain.FomodGroupChoice{{Name: "Group\x1b[2J",
			Choices: []domain.FomodOptionChoice{{Name: evil, Idx: 0}}}}}},
	}
	var out bytes.Buffer
	f := &fomodAnswerer{interactive: true, in: bufio.NewReader(strings.NewReader("\n")), out: &out,
		answers: map[string]domain.FomodChoices{}, walked: map[string]int{}, sent: map[string]string{}}
	a, err := f.walk(q)
	require.NoError(t, err)
	text := out.String()
	for _, bad := range []string{"\u009b", rlo, "\x1b", "\x07"} {
		assert.NotContains(t, text, bad, "the walk printed a raw control")
	}
	assert.Contains(t, text, "line one")
	require.Len(t, a.Steps, 1)
	assert.Equal(t, "Step\u009b", a.Steps[0].Name, "answers carry the raw names")
	assert.Equal(t, evil, a.Steps[0].Groups[0].Choices[0].Name)

	var summary bytes.Buffer
	printFomodQuestionSummary(&summary, &core.FomodChoicesRequiredError{Mods: []core.FomodQuestion{q}})
	for _, bad := range []string{"\u009b", rlo, "\x1b", "\x07"} {
		assert.NotContains(t, summary.String(), bad, "the summary printed a raw control")
	}
}

// TestDoModShow_SaysHowAFomodModsInstallerWasAnswered: lmm mod show names
// the reconfigure command for a FOMOD install.
func TestDoModShow_SaysHowAFomodModsInstallerWasAnswered(t *testing.T) {
	svc, game, _ := setupFomodInstall(t)
	fomodDefaults = true
	_, err := captureStdoutErr(t, func() error { return doInstall(context.Background(), svc, game, nil) })
	require.NoError(t, err)
	oldSource := modSource
	modSource = "test-src"
	t.Cleanup(func() { modSource = oldSource })

	out, err := captureStdoutErr(t, func() error { return doModShow(context.Background(), svc, game, "mod1") })
	require.NoError(t, err)
	assert.Contains(t, out, "Installer: FOMOD - 'lmm mod fomod mod1' changes its choices")
}
