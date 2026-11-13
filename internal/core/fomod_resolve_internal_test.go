package core

// #614: the FOMOD resolve step, against hand-built extracted trees: what it
// installs, where, and when it asks or refuses instead (fomod_resolve.go's
// policy).

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testModuleConfig: a required file, one step with an exactly-one texture
// group (2K, 4K) and an optional extras group.
const testModuleConfig = `<?xml version="1.0"?>
<config>
  <moduleName>Test Installer</moduleName>
  <requiredInstallFiles>
    <file source="core\plugin.esp" destination="plugin.esp"/>
  </requiredInstallFiles>
  <installSteps order="Explicit">
    <installStep name="Main">
      <optionalFileGroups order="Explicit">
        <group name="Texture" type="SelectExactlyOne">
          <plugins order="Explicit">
            <plugin name="2K">
              <description>Light.</description>
              <files><file source="tex\2k.dds" destination="textures\t.dds"/></files>
              <typeDescriptor><type name="Optional"/></typeDescriptor>
            </plugin>
            <plugin name="4K">
              <files><file source="tex\4k.dds" destination="textures\t.dds"/></files>
              <typeDescriptor><type name="Optional"/></typeDescriptor>
            </plugin>
          </plugins>
        </group>
        <group name="Extras" type="SelectAny">
          <plugins order="Explicit">
            <plugin name="Readme">
              <files><file source="docs\readme.txt" destination="readme.txt"/></files>
              <typeDescriptor><type name="Optional"/></typeDescriptor>
            </plugin>
          </plugins>
        </group>
      </optionalFileGroups>
    </installStep>
  </installSteps>
</config>`

// fomodTree writes an extracted archive: the installer (under root, "" or
// a wrapper with a trailing '/') and its payload.
func fomodTree(t *testing.T, root, config string, extra map[string]string) string {
	t.Helper()
	tree := filepath.Join(t.TempDir(), "tree")
	files := map[string]string{
		"fomod/ModuleConfig.xml": config,
		"fomod/info.xml":         "<fomod/>",
		"core/plugin.esp":        "PLUGIN",
		"tex/2k.dds":             "2K",
		"tex/4k.dds":             "4K",
		"docs/readme.txt":        "README",
	}
	for k, v := range extra {
		files[k] = v
	}
	for rel, body := range files {
		p := filepath.Join(tree, filepath.FromSlash(root+rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	return tree
}

// fomodTreeFiles lists tree as rel -> content.
func fomodTreeFiles(t *testing.T, tree string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(tree, func(p string, d os.DirEntry, err error) error {
		require.NoError(t, err)
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(tree, p)
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	}))
	return out
}

var fomodTestMod = &domain.Mod{SourceID: "src", ID: "m1", Name: "Test Mod"}

func pick(texture string, extras ...string) domain.FomodChoices {
	ex := []fomod.OptionChoice{}
	for _, e := range extras {
		ex = append(ex, fomod.OptionChoice{Name: e})
	}
	return domain.FomodChoices{Steps: []fomod.StepChoice{{Name: "Main", Groups: []fomod.GroupChoice{
		{Name: "Texture", Choices: []fomod.OptionChoice{{Name: texture}}},
		{Name: "Extras", Choices: ex},
	}}}}
}

func TestResolveFomod_NotAnInstallerLeavesTheTree(t *testing.T) {
	tree := filepath.Join(t.TempDir(), "tree")
	require.NoError(t, os.MkdirAll(filepath.Join(tree, "Data"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(tree, "Data", "a.esp"), []byte("A"), 0o644))

	got, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", tree, fomodInput{})
	require.NoError(t, err)
	assert.Nil(t, got)
	assert.Equal(t, map[string]string{"Data/a.esp": "A"}, fomodTreeFiles(t, tree))
}

func TestResolveFomod_NoChoicesAsks(t *testing.T) {
	tree := fomodTree(t, "", testModuleConfig, nil)
	before := fomodTreeFiles(t, tree)

	_, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", tree, fomodInput{})
	var q *FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	require.Len(t, q.Mods, 1)
	question := q.Mods[0]
	assert.Equal(t, "src", question.SourceID)
	assert.Equal(t, "m1", question.ModID)
	assert.Equal(t, "Test Installer", question.ModuleName)
	assert.Equal(t, FomodReasonUnanswered, question.Reason)
	assert.Len(t, question.ConfigSHA256, 64)
	require.Len(t, question.Steps, 1)
	require.Len(t, question.Steps[0].Groups, 2)
	texture := question.Steps[0].Groups[0]
	assert.Equal(t, "SelectExactlyOne", texture.Type)
	assert.Equal(t, "Light.", texture.Options[0].Description)
	assert.True(t, texture.Options[0].Selected, "the preselection is the defaults (first of an exactly-one group)")
	assert.NotEmpty(t, question.Unanswered)
	assert.Equal(t, before, fomodTreeFiles(t, tree), "a question changes nothing on disk")
	assert.True(t, answerableRefusal(err), "the engine collects it before writing anything")
	assert.True(t, retainsDownload(err), "and keeps the download")
}

func TestResolveFomod_AnsweredInstallsTheChosenTree(t *testing.T) {
	tree := fomodTree(t, "", testModuleConfig, nil)

	got, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", tree, fomodInput{choices: pick("4K", "Readme"), answered: true})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, map[string]string{"plugin.esp": "PLUGIN", "textures/t.dds": "4K", "readme.txt": "README"}, fomodTreeFiles(t, tree),
		"exactly what the choices install, and never the installer's fomod/ directory")
	assert.Len(t, got.choices.ConfigSHA256, 64)
	assert.Equal(t, fomodVariant(got.choices), got.variant)
	assert.Len(t, got.variant, 12)

	// The canonical choices replay to the same tree and variant: what the
	// converge flows derive from a profile reference.
	tree2 := fomodTree(t, "", testModuleConfig, nil)
	again, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", tree2, fomodInput{choices: got.choices})
	require.NoError(t, err)
	assert.Equal(t, got.variant, again.variant)
	assert.Equal(t, fomodTreeFiles(t, tree), fomodTreeFiles(t, tree2))
}

func TestResolveFomod_DifferentChoicesAreDifferentVariants(t *testing.T) {
	a, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", fomodTree(t, "", testModuleConfig, nil), fomodInput{choices: pick("2K"), answered: true})
	require.NoError(t, err)
	b, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", fomodTree(t, "", testModuleConfig, nil), fomodInput{choices: pick("4K"), answered: true})
	require.NoError(t, err)
	assert.NotEqual(t, a.variant, b.variant)
	assert.NotContains(t, a.variant, cache.VariantSeparator)
}

func TestResolveFomod_WrapperRootMapsSources(t *testing.T) {
	tree := fomodTree(t, "Wrapped Mod/", testModuleConfig, nil)
	_, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", tree, fomodInput{choices: pick("2K"), answered: true})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"plugin.esp": "PLUGIN", "textures/t.dds": "2K"}, fomodTreeFiles(t, tree))
}

func TestResolveFomod_StaleRecordingAsksWithWhatDidNotFit(t *testing.T) {
	tree := fomodTree(t, "", testModuleConfig, nil)
	_, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", tree, fomodInput{choices: pick("8K")})
	var q *FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	assert.Equal(t, FomodReasonStale, q.Mods[0].Reason)
	require.Len(t, q.Mods[0].Unmatched, 1)
	assert.Equal(t, "8K", q.Mods[0].Unmatched[0].Option)
	assert.Equal(t, "no such option", q.Mods[0].Unmatched[0].Reason)
	assert.Contains(t, err.Error(), "no longer fit")
}

// TestResolveFomod_MissingSourcePolicy: a replay of recorded choices
// refuses a source the archive lacks (fidelity); a fresh answer, or the
// defaults, only warns, as Vortex does.
func TestResolveFomod_MissingSourcePolicy(t *testing.T) {
	config := strings.Replace(testModuleConfig, `source="docs\readme.txt"`, `source="docs\gone.txt"`, 1)

	_, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", fomodTree(t, "", config, nil), fomodInput{choices: pick("2K", "Readme")})
	var refusal *FomodInstallError
	require.True(t, errors.As(err, &refusal), "a replay refuses: %v", err)
	assert.Contains(t, refusal.Reason, `"docs/gone.txt"`)
	assert.False(t, answerableRefusal(err))

	got, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", fomodTree(t, "", config, nil), fomodInput{choices: pick("2K", "Readme"), answered: true})
	require.NoError(t, err)
	assert.NotEmpty(t, got.warnings)
	assert.Contains(t, strings.Join(got.warnings, "\n"), `"docs/gone.txt"`)
}

// TestResolveFomod_UnsafeMembersOnlyWarn (owner ruling 2026-11-12): a
// member that is not a safe path - here a file literally named
// "..\\evil.esp", which a Linux extraction writes as one file - is never
// read and never written, for a fresh answer and a replay alike, and only
// warns.
func TestResolveFomod_UnsafeMembersOnlyWarn(t *testing.T) {
	for name, in := range map[string]fomodInput{
		"fresh answer": {choices: pick("2K"), answered: true},
		"replay":       {choices: pick("2K")},
	} {
		t.Run(name, func(t *testing.T) {
			tree := fomodTree(t, "", testModuleConfig, map[string]string{`..\evil.esp`: "EVIL"})
			got, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", tree, in)
			require.NoError(t, err)
			assert.Equal(t, map[string]string{"plugin.esp": "PLUGIN", "textures/t.dds": "2K"}, fomodTreeFiles(t, tree),
				"the unsafe member is not installed")
			assert.Contains(t, strings.Join(got.warnings, "\n"), `evil.esp" is not a safe path and was not read`)
		})
	}
}

// TestResolveFomod_CopyBudget (#614 review should-fix 1): an installer that
// would copy far more than its archive holds is refused before anything is
// written.
func TestResolveFomod_CopyBudget(t *testing.T) {
	saved := fomodCopyBudget
	t.Cleanup(func() { fomodCopyBudget = saved })
	fomodCopyBudget = func(int64) int64 { return 4 } // fewer bytes than the choices copy

	tree := fomodTree(t, "", testModuleConfig, nil)
	before := fomodTreeFiles(t, tree)
	_, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", tree, fomodInput{choices: pick("4K", "Readme"), answered: true})
	var refusal *FomodInstallError
	require.True(t, errors.As(err, &refusal), "got %v", err)
	assert.Contains(t, refusal.Reason, "refuses an install that large")
	assert.Equal(t, before, fomodTreeFiles(t, tree), "nothing was written")
	_, statErr := os.Stat(tree + ".fomod")
	assert.True(t, os.IsNotExist(statErr), "no half-built tree is left")

	fomodCopyBudget = saved
	_, err = (&Service{}).resolveFomod(fomodTestMod, "mod.zip", fomodTree(t, "", testModuleConfig, nil), fomodInput{choices: pick("4K", "Readme"), answered: true})
	assert.NoError(t, err, "the real budget fits an ordinary installer")
}

func TestResolveFomod_RefusesWhatNoAnswerFixes(t *testing.T) {
	for name, tc := range map[string]struct {
		archive string
		build   func(t *testing.T) string
		want    string
	}{
		"scripted": {"mod.zip", func(t *testing.T) string {
			tree := filepath.Join(t.TempDir(), "tree")
			require.NoError(t, os.MkdirAll(filepath.Join(tree, "fomod"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(tree, "fomod", "script.cs"), []byte("class X {}"), 0o644))
			return tree
		}, "scripted installer"},
		"omod": {"mod.omod", func(t *testing.T) string { return fomodTree(t, "", testModuleConfig, nil) }, "scripted installer"},
		"two installers": {"mod.zip", func(t *testing.T) string {
			tree := fomodTree(t, "A/", testModuleConfig, nil)
			p := filepath.Join(tree, "B", "fomod", "ModuleConfig.xml")
			require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
			require.NoError(t, os.WriteFile(p, []byte(testModuleConfig), 0o644))
			return tree
		}, "more than one FOMOD installer"},
		"unreadable config": {"mod.zip", func(t *testing.T) string { return fomodTree(t, "", "not xml at all", nil) }, "could not be read"},
		"reserved destination": {"mod.zip", func(t *testing.T) string {
			return fomodTree(t, "", strings.Replace(testModuleConfig, `destination="plugin.esp"`, `destination=".lmm-file-x"`, 1), nil)
		}, "reserved namespace"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := (&Service{}).resolveFomod(fomodTestMod, tc.archive, tc.build(t), fomodInput{choices: pick("2K"), answered: true})
			var refusal *FomodInstallError
			require.True(t, errors.As(err, &refusal), "got %v", err)
			assert.Contains(t, refusal.Reason, tc.want)
			assert.False(t, answerableRefusal(err))
		})
	}
}

// TestResolveFomod_NothingToChooseInstallsTheDefaults: an installer whose
// every group is fixed asks nothing, and records what it installed.
func TestResolveFomod_NothingToChooseInstallsTheDefaults(t *testing.T) {
	config := `<config><moduleName>Fixed</moduleName><requiredInstallFiles><file source="core\plugin.esp" destination="plugin.esp"/></requiredInstallFiles></config>`
	tree := fomodTree(t, "", config, nil)
	got, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", tree, fomodInput{})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"plugin.esp": "PLUGIN"}, fomodTreeFiles(t, tree))
	assert.False(t, got.choices.IsZero(), "the config hash is recorded even with no steps")
	assert.NotEmpty(t, got.variant)
}

// TestResolveFomod_TrailingSeparatorMakesADirectory keeps Vortex's quirk:
// a <file> destination ending in a separator creates that directory and
// installs nothing.
func TestResolveFomod_TrailingSeparatorMakesADirectory(t *testing.T) {
	config := `<config><moduleName>Q</moduleName><requiredInstallFiles><file source="core\plugin.esp" destination="plugins\"/></requiredInstallFiles></config>`
	tree := fomodTree(t, "", config, nil)
	got, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", tree, fomodInput{})
	require.NoError(t, err)
	assert.Empty(t, fomodTreeFiles(t, tree))
	info, err := os.Stat(filepath.Join(tree, "plugins"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
	assert.NotEmpty(t, got.warnings)
}

func TestFomodVariant(t *testing.T) {
	assert.Empty(t, fomodVariant(domain.FomodChoices{}), "no choices is the plain tree")
	a := domain.FomodChoices{ConfigSHA256: "ab", Steps: nil}
	b := domain.FomodChoices{ConfigSHA256: "ab", Steps: []fomod.StepChoice{}}
	assert.Equal(t, fomodVariant(a), fomodVariant(b), "nil and empty steps are one tree")
	assert.NotEqual(t, fomodVariant(a), fomodVariant(domain.FomodChoices{ConfigSHA256: "cd"}), "the installer is a tree input")
	assert.Len(t, fomodVariant(a), 12)
}

func TestProfileFileStates(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Data"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "Data", "Skyrim.esm"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "Other.esp"), []byte("x"), 0o644))
	f := &profileFileStates{seen: map[string]fomod.FileState{}, dirs: map[string][]os.DirEntry{},
		load: func() (map[string]bool, map[string]bool, string) {
			return map[string]bool{"patch.esp": true}, map[string]bool{"other.esp": true}, root
		}}

	got := map[string]fomod.FileState{}
	for _, p := range []string{"data/skyrim.esm", "patch.esp", "other.esp", "missing.esp", "data", "../etc/passwd", ""} {
		got[p] = f.State(p)
	}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal(t, map[string]fomod.FileState{
		"data/skyrim.esm": fomod.FileActive,  // the game's own file, case-insensitively: present is active until P5
		"patch.esp":       fomod.FileActive,  // a mod of the target profile provides it
		"other.esp":       fomod.FileMissing, // on disk, but another profile's deployment put it there
		"missing.esp":     fomod.FileMissing,
		"data":            fomod.FileMissing, // a directory is not a file
		"../etc/passwd":   fomod.FileMissing,
		"":                fomod.FileMissing,
	}, got, "%v", keys)
}

// TestResolveFomod_AnIncompleteAnswerIsAskedAgainNotStale: answers that
// leave a visible group unanswered (a later step, or a group they skipped)
// are asked about again, with the answers as the preselection - and are
// not called a stale recording.
func TestResolveFomod_AnIncompleteAnswerIsAskedAgainNotStale(t *testing.T) {
	tree := fomodTree(t, "", testModuleConfig, nil)
	partial := domain.FomodChoices{Steps: []fomod.StepChoice{{Name: "Main", Groups: []fomod.GroupChoice{
		{Name: "Extras", Choices: []fomod.OptionChoice{{Name: "Readme"}}},
	}}}}
	_, err := (&Service{}).resolveFomod(fomodTestMod, "mod.zip", tree, fomodInput{choices: partial, answered: true})
	var q *FomodChoicesRequiredError
	require.True(t, errors.As(err, &q), "got %v", err)
	assert.Equal(t, FomodReasonUnanswered, q.Mods[0].Reason)
	require.Len(t, q.Mods[0].Unanswered, 1)
	assert.Equal(t, "Texture", q.Mods[0].Unanswered[0].Group)
	assert.NotContains(t, err.Error(), "no longer fit")
}
