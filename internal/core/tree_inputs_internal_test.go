package core

// #610: the acquire step for a reference's exact file lists and binary
// patches, the variant they name, and the checks a stranger's are held to.

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTreeInputService(t *testing.T) (*Service, *domain.Game) {
	t.Helper()
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc, &domain.Game{ID: "g", Name: "Game", ModPath: t.TempDir()}
}

func md5Of(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func crcHex(s string) string { return fmt.Sprintf("%08X", crc32.ChecksumIEEE([]byte(s))) }

// writeTree writes files (slash paths) under a new directory.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	return dir
}

func treeContents(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	rels, err := relativeFileMembers(dir)
	require.NoError(t, err)
	for _, rel := range rels {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		require.NoError(t, err)
		out[filepath.ToSlash(rel)] = string(b)
	}
	return out
}

var treeMod = &domain.Mod{ID: "10", SourceID: "nexusmods", Name: "Ten"}

func TestTreeInputsVariant(t *testing.T) {
	list := domain.TreeInputs{FileList: []domain.FileListEntry{{Path: `a\b`, MD5: md5Of("x")}}}
	other := domain.TreeInputs{FileList: []domain.FileListEntry{{Path: `a\c`, MD5: md5Of("x")}}}
	assert.Equal(t, "", treeInputsVariant("", nil), "no inputs: the plain tree")
	assert.Equal(t, "abc123def456", treeInputsVariant("abc123def456", map[string]domain.TreeInputs{"1": {}}), "zero inputs keep the FOMOD variant")
	v := treeInputsVariant("", map[string]domain.TreeInputs{"1": list})
	assert.Len(t, v, 12)
	assert.Equal(t, v, treeInputsVariant("", map[string]domain.TreeInputs{"1": list}), "deterministic")
	assert.NotEqual(t, v, treeInputsVariant("", map[string]domain.TreeInputs{"1": other}), "two lists never share an entry")
	assert.NotEqual(t, v, treeInputsVariant("", map[string]domain.TreeInputs{"2": list}), "the file it is for counts")
	assert.NotEqual(t, v, treeInputsVariant("abc123def456", map[string]domain.TreeInputs{"1": list}), "over the FOMOD variant")
	patched := domain.TreeInputs{Patches: []domain.FilePatch{{Path: "a", CRC32: "deadbeef", SHA256: strings.Repeat("a", 64)}}}
	assert.Equal(t, treeInputsVariant("", map[string]domain.TreeInputs{"1": patched}),
		treeInputsVariant("", map[string]domain.TreeInputs{"1": {Patches: []domain.FilePatch{{Path: "a", CRC32: "DEADBEEF", SHA256: strings.Repeat("a", 64)}}}}),
		"a CRC32's case is not an input")
}

func TestRefVariant_IncludesTreeInputsOfPinnedFilesOnly(t *testing.T) {
	plain := domain.ModReference{SourceID: "s", ModID: "m", FileIDs: []string{"1"}}
	assert.Equal(t, "", refVariant(&plain))
	listed := plain
	listed.FileList = domain.FileLists{"1": {{Path: "a", MD5: md5Of("a")}}}
	assert.NotEmpty(t, refVariant(&listed))
	inert := plain
	inert.FileList = domain.FileLists{"9": {{Path: "a", MD5: md5Of("a")}}}
	assert.Equal(t, "", refVariant(&inert), "a list for a file the ref does not pin names nothing")
	fomodOnly := plain
	fomodOnly.Fomod = domain.FomodChoices{ConfigSHA256: strings.Repeat("ab", 32)}
	both := fomodOnly
	both.FileList = listed.FileList
	assert.NotEqual(t, refVariant(&fomodOnly), refVariant(&both))
	assert.Equal(t, treeInputsVariant(refVariant(&fomodOnly), refTreeInputs(&both)), refVariant(&both))
}

func TestApplyTreeInputs_FileListKeepsOnlyListedFilesAndChecksMD5(t *testing.T) {
	svc, game := newTreeInputService(t)
	tree := writeTree(t, map[string]string{
		"r6/scripts/A.reds": "a", "r6/scripts/b.reds": "b", "r6/tweaks/extra.yaml": "x", "readme.txt": "r",
	})
	in := domain.TreeInputs{FileList: []domain.FileListEntry{
		{Path: `r6\scripts\a.reds`, MD5: md5Of("a")}, // another case, the other separator
		{Path: "r6/scripts/b.reds", MD5: strings.ToUpper(md5Of("b"))},
	}}
	require.NoError(t, svc.applyTreeInputs(game, treeMod, "100", tree, in))
	assert.Equal(t, map[string]string{"r6/scripts/A.reds": "a", "r6/scripts/b.reds": "b"}, treeContents(t, tree))
	assert.NoDirExists(t, filepath.Join(tree, "r6", "tweaks"), "a directory the list emptied is removed")
}

func TestApplyTreeInputs_Refusals(t *testing.T) {
	svc, game := newTreeInputService(t)
	for name, c := range map[string]struct {
		files map[string]string
		in    domain.TreeInputs
		want  string
	}{
		"a listed file is missing": {map[string]string{"a": "a"},
			domain.TreeInputs{FileList: []domain.FileListEntry{{Path: "b", MD5: md5Of("b")}}}, "holds no such file"},
		"a listed file's md5 differs": {map[string]string{"a": "changed"},
			domain.TreeInputs{FileList: []domain.FileListEntry{{Path: "a", MD5: md5Of("a")}}}, "md5 is"},
		"two files differ only in case": {map[string]string{"Data/a.esp": "1", "data/A.esp": "2"},
			domain.TreeInputs{FileList: []domain.FileListEntry{{Path: `data\a.esp`, MD5: md5Of("1")}}}, "differing only in case"},
		"a patch's target is missing": {map[string]string{"a": "a"},
			domain.TreeInputs{Patches: []domain.FilePatch{{Path: "b", CRC32: crcHex("b"), SHA256: strings.Repeat("a", 64)}}}, "holds no such file"},
		"a patch's baseline differs": {map[string]string{"m/manifest.json": "edited by the user"},
			domain.TreeInputs{Patches: []domain.FilePatch{{Path: `m\manifest.json`, CRC32: crcHex(manifestV40), SHA256: strings.Repeat("a", 64)}}}, "Vortex would install it unpatched"},
		"a patch is not kept": {map[string]string{"m/manifest.json": manifestV40},
			domain.TreeInputs{Patches: []domain.FilePatch{{Path: `m\manifest.json`, CRC32: crcHex(manifestV40), SHA256: strings.Repeat("a", 64)}}}, "not kept on this machine"},
	} {
		t.Run(name, func(t *testing.T) {
			tree := writeTree(t, c.files)
			err := svc.applyTreeInputs(game, treeMod, "100", tree, c.in)
			require.ErrorIs(t, err, ErrTreeInput)
			var fpe *FilePatchError
			require.ErrorAs(t, err, &fpe)
			assert.Equal(t, "100", fpe.FileID)
			assert.Contains(t, err.Error(), c.want)
		})
	}
}

// TestApplyTreeInputs_PatchesOntoTheBaselineThenChecksTheList: the patch
// applies to the file whose CRC32 is the baseline, from the patch store;
// the file list's md5 is the patched file's (the curator's file as
// installed).
func TestApplyTreeInputs_PatchesOntoTheBaselineThenChecksTheList(t *testing.T) {
	svc, game := newTreeInputService(t)
	patch, err := os.ReadFile(filepath.Join("testdata", "bsdiff", "same-length-edit.patch"))
	require.NoError(t, err)
	sha, err := svc.keepPatch(game, patch)
	require.NoError(t, err)
	again, err := svc.keepPatch(game, patch)
	require.NoError(t, err)
	assert.Equal(t, sha, again, "kept once, by its sha256")

	tree := writeTree(t, map[string]string{"Mod/manifest.json": manifestV40, "Mod/other.json": "{}"})
	in := domain.TreeInputs{
		FileList: []domain.FileListEntry{{Path: `Mod\manifest.json`, MD5: md5Of(manifestV41)}},
		Patches:  []domain.FilePatch{{Path: `Mod\manifest.json`, CRC32: strings.ToLower(crcHex(manifestV40)), SHA256: sha}},
	}
	// Kept, but no import admitted it for this file: refused, nothing
	// written (#610 review B1).
	err = svc.applyTreeInputs(game, treeMod, "100", tree, in)
	require.ErrorIs(t, err, ErrTreeInput)
	assert.Contains(t, err.Error(), "no import admitted this patch for this mod's file")
	tree = writeTree(t, map[string]string{"Mod/manifest.json": manifestV40, "Mod/other.json": "{}"})
	// Admitted for another file: still refused here.
	admit(t, svc, game, domain.FilePatch{Path: "Mod/other.json", SHA256: sha}, false)
	require.ErrorIs(t, svc.applyTreeInputs(game, treeMod, "100", tree, in), ErrTreeInput)
	tree = writeTree(t, map[string]string{"Mod/manifest.json": manifestV40, "Mod/other.json": "{}"})
	admit(t, svc, game, in.Patches[0], false)
	require.NoError(t, svc.applyTreeInputs(game, treeMod, "100", tree, in))
	assert.Equal(t, map[string]string{"Mod/manifest.json": manifestV41}, treeContents(t, tree))
	info, err := os.Stat(filepath.Join(tree, "Mod", "manifest.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm(), "a patched file is never executable")

	// A damaged copy in the store is refused, not applied.
	require.NoError(t, os.WriteFile(filepath.Join(svc.patchStoreDir(game), sha+".diff"), append([]byte(nil), patch[:len(patch)-1]...), 0o644))
	tree = writeTree(t, map[string]string{"Mod/manifest.json": manifestV40})
	err = svc.applyTreeInputs(game, treeMod, "100", tree, domain.TreeInputs{Patches: in.Patches})
	require.ErrorIs(t, err, ErrPatchNotKept)
	assert.Equal(t, manifestV40, treeContents(t, tree)["Mod/manifest.json"], "nothing is written on a refusal")
}

func TestCheckTreeInputs_RefusesWhatItCannotMatchAsWritten(t *testing.T) {
	md := md5Of("x")
	sha := strings.Repeat("ab", 32)
	list := func(p string) domain.FileLists { return domain.FileLists{"1": {{Path: p, MD5: md}}} }
	for name, c := range map[string]struct {
		lists   domain.FileLists
		patches domain.FilePatches
	}{
		"traversal":          {lists: list(`..\..\evil.dll`)},
		"inner traversal":    {lists: list("a/../../b")},
		"absolute":           {lists: list("/etc/passwd")},
		"absolute backslash": {lists: list(`\Windows\x.dll`)},
		"drive":              {lists: list(`C:\x.dll`)},
		"dot segment":        {lists: list("a/./b")},
		"empty segment":      {lists: list(`a\\b`)},
		"empty":              {lists: list("")},
		"bidi":               {lists: list("a\u202eb.txt")},
		"control":            {lists: list("a\x1bb")},
		"newline":            {lists: list("a\nb")},
		"too long":           {lists: list(strings.Repeat("a", maxTreeInputPath+1))},
		"listed twice":       {lists: domain.FileLists{"1": {{Path: `A\b`, MD5: md}, {Path: "a/B", MD5: md}}}},
		"short md5":          {lists: domain.FileLists{"1": {{Path: "a", MD5: "abc"}}}},
		"bad crc":            {patches: domain.FilePatches{"1": {{Path: "a", CRC32: "XYZ12345", SHA256: sha}}}},
		"upper sha":          {patches: domain.FilePatches{"1": {{Path: "a", CRC32: "DEADBEEF", SHA256: strings.ToUpper(sha)}}}},
		"patched twice":      {patches: domain.FilePatches{"1": {{Path: "a", CRC32: "DEADBEEF", SHA256: sha}, {Path: "A", CRC32: "DEADBEEF", SHA256: sha}}}},
		"bidi file id":       {lists: domain.FileLists{"1\u202e": {{Path: "a", MD5: md}}}},
		"too many files": {lists: func() domain.FileLists {
			l := make([]domain.FileListEntry, maxFileListEntries+1)
			for i := range l {
				l[i] = domain.FileListEntry{Path: fmt.Sprint(i), MD5: md}
			}
			return domain.FileLists{"1": l}
		}()},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, checkTreeInputs(c.lists, c.patches))
		})
	}
	require.NoError(t, checkTreeInputs(domain.FileLists{"1": {{Path: `r6\scripts\[CP] x's (v1).reds`, MD5: strings.ToUpper(md)}}},
		domain.FilePatches{"1": {{Path: "Grandpa's Tools/[CP]Grandpa's Tools/content.json", CRC32: "cabddfc4", SHA256: sha}}}),
		"brackets, apostrophes, spaces and either case of hex are a real recipe's")
}

func TestRecipeFilesBudget_ChecksTheTreesBeforeExtracting(t *testing.T) {
	file := func(p, size string) map[string]string {
		return map[string]string{"Path": p, "Size": size, "Attributes": "A"}
	}
	ok := []map[string]string{
		{"Path": "bundled", "Attributes": "D"},
		file("bundled/Bundled - Mod (v1.0.0)/[CP] Mod/content.json", "10"),
		file("patches/Mod/a/content.json.diff", "158"),
		file("collection.json", "999999999"),
		file("other/../../x", "1"), // outside the two trees: never extracted, so never checked here
	}
	var b recipeFilesBudget
	for _, f := range ok {
		require.NoError(t, b.check(f))
	}
	for name, f := range map[string]map[string]string{
		"traversal":  file("bundled/x/../../../etc/passwd", "1"),
		"backslash":  file(`patches/x\..\y.diff`, "1"),
		"control":    file("bundled/x/a\x1bb", "1"),
		"symlink":    {"Path": "bundled/x/link", "Size": "1", "Attributes": "A lrwxrwxrwx"},
		"named link": {"Path": "bundled/x/link", "Size": "1", "Symbolic Link": "/etc/passwd"},
		"hard link":  {"Path": "bundled/x/link", "Size": "1", "Hard Link": "bundled/x/a"},
		"device":     {"Path": "bundled/x/dev", "Size": "0", "Attributes": "A crw-r--r--"},
		"no size":    file("bundled/x/a", ""),
		"big file":   file("bundled/x/a", fmt.Sprint(maxBundleFileBytes+1)),
		"big patch":  file("patches/x/a.diff", fmt.Sprint(maxPatchBytes+1)),
		"empty seg":  file("bundled//a", "1"),
	} {
		t.Run(name, func(t *testing.T) {
			var b recipeFilesBudget
			require.Error(t, b.check(f))
		})
	}
	t.Run("total over the cap", func(t *testing.T) {
		var b recipeFilesBudget
		for i := 0; i < 5; i++ {
			err := b.check(file(fmt.Sprintf("bundled/x/%d", i), fmt.Sprint(maxBundleFileBytes)))
			if i < 4 {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		}
	})
}

func TestUniqueBundleName(t *testing.T) {
	names := map[string]bool{}
	assert.Equal(t, "stardew-valley-very-expanded-collection-v2-0-0", uniqueBundleName("Bundled - Stardew Valley VERY Expanded Collection (v2.0.0)", names))
	assert.Equal(t, "stardew-valley-very-expanded-collection-v2-0-0-2", uniqueBundleName("Bundled - Stardew Valley VERY Expanded Collection (v2.0.0)", names))
	assert.Equal(t, "bundled", uniqueBundleName("Bundled - ☃", names), "nothing of it survives")
	long := uniqueBundleName("Bundled - "+strings.Repeat("Long Name ", 20), names)
	assert.True(t, validBundleItem(long), long)
	again := uniqueBundleName("Bundled - "+strings.Repeat("Long Name ", 20), names)
	assert.True(t, validBundleItem(again), again)
	assert.NotEqual(t, long, again)
}

func TestPinnedOnly(t *testing.T) {
	lists := domain.FileLists{"1": {{Path: "a"}}, "2": {{Path: "b"}}}
	assert.Equal(t, domain.FileLists{"1": {{Path: "a"}}}, pinnedOnly(lists, []string{"1", "3"}))
	assert.Nil(t, pinnedOnly(lists, nil), "a ref that pins nothing installs by no list")
	assert.Nil(t, pinnedOnly(lists, []string{"9"}))
}

func TestPatchCodeReason(t *testing.T) {
	assert.NotEmpty(t, patchCodeReason(`Ridgeside Village\RidgesideVillage\RidgesideVillage.dll`))
	assert.NotEmpty(t, patchCodeReason(`BepInEx\doorstop_config.ini`))
	assert.Empty(t, patchCodeReason(`Ridgeside Village\RidgesideVillage\config.json`))
}

// TestApplyTreeInputs_CodeOutputNeedsAnAdmissionThatAllowsCode: a patch
// whose OUTPUT is native code - whatever the file's name - applies only
// where its admission allowed code (--allow-any-overrides at the import
// that admitted it), and to a file named as code likewise (#610 review).
func TestApplyTreeInputs_CodeOutputNeedsAnAdmissionThatAllowsCode(t *testing.T) {
	if _, err := os.Stat(filepath.Join("testdata", "bsdiff", "make-elf.patch")); err != nil {
		t.Skip("run TestBSDIFFFixtures with -update-bsdiff-fixtures")
	}
	svc, game := newTreeInputService(t)
	patch, err := os.ReadFile(filepath.Join("testdata", "bsdiff", "make-elf.patch"))
	require.NoError(t, err)
	sha, err := svc.keepPatch(game, patch)
	require.NoError(t, err)
	p := domain.FilePatch{Path: `Mod\data.bin`, CRC32: crcHex(elfFixtureOld), SHA256: sha}
	in := domain.TreeInputs{Patches: []domain.FilePatch{p}}

	admit(t, svc, game, p, false)
	tree := writeTree(t, map[string]string{"Mod/data.bin": elfFixtureOld})
	err = svc.applyTreeInputs(game, treeMod, "100", tree, in)
	require.ErrorIs(t, err, ErrTreeInput)
	assert.Contains(t, err.Error(), "native code")
	assert.Equal(t, elfFixtureOld, treeContents(t, tree)["Mod/data.bin"], "nothing written")

	admit(t, svc, game, p, true)
	tree = writeTree(t, map[string]string{"Mod/data.bin": elfFixtureOld})
	require.NoError(t, svc.applyTreeInputs(game, treeMod, "100", tree, in))
	assert.Equal(t, elfFixtureNew, treeContents(t, tree)["Mod/data.bin"])
}

// TestKeepRecipeBundle_AdmissionHoldsOnlyForTheSameContent (#610 review): an
// item an earlier import admitted is not admitted again when the recipe's
// files under that name change, unless this import admits it.
func TestKeepRecipeBundle_AdmissionHoldsOnlyForTheSameContent(t *testing.T) {
	svc, game := newTreeInputService(t)
	recipeDir := filepath.Join(svc.collectionsRoot(game), "nexusmods-abcdef", "3")
	keep := func(content string, admit bool) keptItem {
		t.Helper()
		item := &bundledItem{name: "marker", display: "Bundled - Marker", members: []bundleMember{{rel: "a.txt", size: int64(len(content)),
			open: func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(content)), nil }}}}
		require.NoError(t, item.read())
		b := &pendingBundle{collectionID: "nexusmods-abcdef", recipeDir: recipeDir, names: map[string]string{"marker": "Marker"},
			bundle: &collectionBundle{items: map[string]*bundledItem{"marker": item}}}
		require.NoError(t, os.MkdirAll(recipeDir, 0o755))
		require.NoError(t, svc.keepRecipeBundle(game, b, map[string]bool{"marker": admit}))
		index, err := readKeptIndex(recipeDir + keptIndexSuffix)
		require.NoError(t, err)
		return index.Items["marker"]
	}
	assert.True(t, keep("one", true).Admitted)
	assert.True(t, keep("one", false).Admitted, "the same content stays admitted")
	assert.False(t, keep("two", false).Admitted, "other content is this import's to admit")
	assert.True(t, keep("two", true).Admitted)
}

// admit admits p for treeMod's file, code allowed when code is.
func admit(t *testing.T, svc *Service, game *domain.Game, p domain.FilePatch, code bool) {
	t.Helper()
	_, err := svc.admitPatches(game, []patchUse{{sourceID: treeMod.SourceID, modID: treeMod.ID, patch: p}}, code)
	require.NoError(t, err)
}

// TestPatchAdmission_IsPerModAndUndoable (#610 re-review): an admission
// for one mod's file is not one for the same path in another mod, and its
// undo puts the record back as it was.
func TestPatchAdmission_IsPerModAndUndoable(t *testing.T) {
	svc, game := newTreeInputService(t)
	sha := strings.Repeat("ab", 32)
	p := domain.FilePatch{Path: `Mod\manifest.json`, CRC32: "CABDDFC4", SHA256: sha}
	mine := patchUse{sourceID: "nexusmods", modID: "21", patch: p}
	theirs := patchUse{sourceID: "nexusmods", modID: "99", patch: p}
	_, err := svc.admitPatches(game, []patchUse{mine}, false)
	require.NoError(t, err)
	rec := svc.readPatchAdmission(game, sha)
	assert.Contains(t, rec, mine.key())
	assert.NotContains(t, rec, theirs.key(), "the same path in another mod is not admitted")

	undo, err := svc.admitPatches(game, []patchUse{theirs}, true)
	require.NoError(t, err)
	assert.Contains(t, svc.readPatchAdmission(game, sha), theirs.key())
	undo()
	assert.Equal(t, map[string]bool{mine.key(): false}, svc.readPatchAdmission(game, sha), "undo restores the record")

	other := strings.Repeat("cd", 32)
	undo, err = svc.admitPatches(game, []patchUse{{sourceID: "s", modID: "m", patch: domain.FilePatch{Path: "a", SHA256: other}}}, false)
	require.NoError(t, err)
	undo()
	assert.Nil(t, svc.readPatchAdmission(game, other), "undo removes a record it created")
}

// TestAdmitImportPatches_OnlyWhatWasBroughtOrListed (#610 re-review): an
// import admits the uses it brings, and with --allow-any-overrides only
// the uses its plan listed - never another patch of the profile.
func TestAdmitImportPatches_OnlyWhatWasBroughtOrListed(t *testing.T) {
	svc, game := newTreeInputService(t)
	sha := strings.Repeat("ab", 32)
	ref := func(mod string) domain.ModReference {
		return domain.ModReference{SourceID: "s", ModID: mod, FileIDs: []string{"1"},
			Patches: domain.FilePatches{"1": {{Path: "a.json", CRC32: "CABDDFC4", SHA256: sha}}}}
	}
	refs := []domain.ModReference{ref("brought"), ref("listed"), ref("other")}
	brought := broughtUses(refs[:1], map[string]bool{sha: true})
	listed := listedPatches(refs[1:2])
	_, err := svc.admitImportPatches(game, refs, brought, listed, true)
	require.NoError(t, err)
	rec := svc.readPatchAdmission(game, sha)
	assert.Contains(t, rec, admissionKey("s", "brought", "a.json"))
	assert.Contains(t, rec, admissionKey("s", "listed", "a.json"))
	assert.NotContains(t, rec, admissionKey("s", "other", "a.json"), "a use the plan did not list")

	assert.Len(t, broughtUses(refs, map[string]bool{sha: true}), 3, "brought is per use")
	assert.Empty(t, broughtUses(refs, nil))
}
