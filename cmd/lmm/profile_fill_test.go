package main

// #608: `lmm profile fill` - the CLI's half. The profile and the cached
// recipe are written directly (what a free account's collection import
// leaves behind), so these tests need no 7z.

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type profileFillFixture struct {
	svc       *core.Service
	game      *domain.Game
	downloads string
	archives  map[string][]byte
}

// newProfileFillFixture saves "Three Mods" (active) pinning 10 (exact,
// locked) and 12 (latest), neither installed, with the recipe cached.
func newProfileFillFixture(t *testing.T) *profileFillFixture {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(&nexusCollectionDouble{})
	game := &domain.Game{ID: "skyrimse", Name: "Skyrim Special Edition", ModPath: t.TempDir(), LinkMethod: domain.LinkCopy,
		SourceIDs: map[string]string{"nexusmods": "skyrimspecialedition"}}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	f := &profileFillFixture{svc: svc, game: game, downloads: t.TempDir(), archives: map[string][]byte{}}

	type recipeMod struct {
		Name, Version, SourceType, ModID, FileID, UpdatePolicy, MD5 string
		SizeBytes                                                   int64
	}
	var mods []recipeMod
	for i, m := range []struct{ id, name, policy string }{{"10", "Ten", "exact"}, {"12", "Twelve", "latest"}} {
		path := filepath.Join(t.TempDir(), m.id+".zip")
		createTestArchive(t, path, map[string]string{"mod" + m.id + ".esp": fmt.Sprintf("plugin %s %*s", m.id, 50*i, "")})
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		f.archives[m.id] = data
		sum := md5.Sum(data)
		mods = append(mods, recipeMod{Name: m.name, Version: "1.0", SourceType: "nexus", ModID: m.id, FileID: m.id + "0",
			UpdatePolicy: m.policy, MD5: hex.EncodeToString(sum[:]), SizeBytes: int64(len(data))})
	}
	recipe, err := json.Marshal(map[string]any{"Name": "Three Mods", "Mods": mods})
	require.NoError(t, err)
	dir := filepath.Join(svc.GlobalCacheDir(), game.ID, "collections", "nexusmods-abcdef", "3")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "collection.json"), recipe, 0o644))
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), &domain.Profile{
		Name: "Three Mods", GameID: game.ID, IsDefault: true,
		Mods: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "10", Version: "1.0", FileIDs: []string{"100"}, Locked: true},
			{SourceID: "nexusmods", ModID: "12", Version: "1.0", FileIDs: []string{"120"}},
		},
		Collection: &domain.CollectionInfo{ID: "nexusmods:abcdef", Origin: &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abcdef", Revision: 3}},
	}))
	return f
}

func (f *profileFillFixture) download(t *testing.T, modID, name string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.downloads, name), f.archives[modID], 0o644))
}

func withFillFlags(t *testing.T, yes, dryRun bool) {
	t.Helper()
	oldYes, oldDry, oldJSON, oldPath, oldDefaults := profileFillYes, profileFillDryRun, jsonOutput, fomodChoicesPath, fomodDefaults
	profileFillYes, profileFillDryRun, fomodChoicesPath, fomodDefaults = yes, dryRun, "", false
	t.Cleanup(func() {
		profileFillYes, profileFillDryRun, jsonOutput, fomodChoicesPath, fomodDefaults = oldYes, oldDry, oldJSON, oldPath, oldDefaults
	})
}

func TestDoProfileFill_InstallsWhatItFindsAndSaysWhatIsLeft(t *testing.T) {
	f := newProfileFillFixture(t)
	withFillFlags(t, true, false)
	f.download(t, "12", "Twelve-12-1-0.zip")
	require.NoError(t, os.WriteFile(filepath.Join(f.downloads, "unrelated.pdf"), []byte("pdf"), 0o644))

	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doProfileFill(context.Background(), f.svc, f.game, "Three Mods", []string{f.downloads})
	})
	require.NoError(t, err, stdout)
	assert.Contains(t, stdout, "Profile: Three Mods (collection abcdef, revision 3)")
	assert.Contains(t, stdout, "✓ 1 to install:")
	assert.Contains(t, stdout, "Twelve (nexusmods:12) ← Twelve-12-1-0.zip (updates automatically)")
	assert.Contains(t, stdout, "⇣ 1 still pending:")
	assert.Contains(t, stdout, "Ten (nexusmods:10): not found")
	assert.Contains(t, stdout, "1 other file(s) match no pending file")
	assert.Contains(t, stdout, "✓ Installed: Twelve")
	assert.Contains(t, stdout, "Installed: 1")
	assert.Contains(t, stdout, "Still pending: 1")

	row, err := f.svc.GetInstalledMod(context.Background(), "nexusmods", "12", f.game.ID, "Three Mods")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateAuto, row.UpdatePolicy)
	_, err = os.Stat(filepath.Join(f.downloads, "Twelve-12-1-0.zip"))
	assert.NoError(t, err, "the download is left where it was")
}

func TestDoProfileFill_DryRunChangesNothing(t *testing.T) {
	f := newProfileFillFixture(t)
	withFillFlags(t, false, true)
	f.download(t, "10", "ten.zip")
	stdout, _, err := captureStdoutStderrErr(t, func() error {
		return doProfileFill(context.Background(), f.svc, f.game, "Three Mods", []string{f.downloads})
	})
	require.NoError(t, err)
	assert.Contains(t, stdout, "Ten (nexusmods:10) ← ten.zip")
	assert.NotContains(t, stdout, "Installed:")
	_, err = f.svc.GetInstalledMod(context.Background(), "nexusmods", "10", f.game.ID, "Three Mods")
	assert.Error(t, err)
}

func TestDoProfileFill_DeclinedIsCancelled(t *testing.T) {
	f := newProfileFillFixture(t)
	withFillFlags(t, false, false)
	f.download(t, "10", "ten.zip")
	var err error
	withStdin(t, "n\n", func() {
		_, _, err = captureStdoutStderrErr(t, func() error {
			return doProfileFill(context.Background(), f.svc, f.game, "Three Mods", []string{f.downloads})
		})
	})
	assert.ErrorIs(t, err, ErrCancelled)
}

func TestDoProfileFill_NotACollectionProfile(t *testing.T) {
	f := newProfileFillFixture(t)
	withFillFlags(t, true, false)
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), &domain.Profile{Name: "plain", GameID: f.game.ID}))
	_, _, err := captureStdoutStderrErr(t, func() error {
		return doProfileFill(context.Background(), f.svc, f.game, "plain", []string{f.downloads})
	})
	assert.ErrorIs(t, err, core.ErrProfileNotFillable)
}

func TestJSONGolden_ProfileFill(t *testing.T) {
	t.Run("dry_run", func(t *testing.T) {
		f := newProfileFillFixture(t)
		withFillFlags(t, false, true)
		withJSONOutput(t)
		f.download(t, "10", "ten.zip")
		stdout, _, err := captureStdoutStderrErr(t, func() error {
			return doProfileFill(context.Background(), f.svc, f.game, "Three Mods", []string{f.downloads})
		})
		require.NoError(t, err)
		var md5s []string
		for _, id := range []string{"10", "12"} {
			sum := md5.Sum(f.archives[id])
			md5s = append(md5s, hex.EncodeToString(sum[:]), "<MD5-"+id+">")
		}
		assertJSONCLIGolden(t, "profile_fill_dry_run", stdout, md5s...)
	})
	t.Run("result", func(t *testing.T) {
		f := newProfileFillFixture(t)
		withFillFlags(t, true, false)
		withJSONOutput(t)
		f.download(t, "10", "ten.zip")
		stdout, _, err := captureStdoutStderrErr(t, func() error {
			return doProfileFill(context.Background(), f.svc, f.game, "Three Mods", []string{f.downloads})
		})
		require.NoError(t, err)
		assertJSONCLIGolden(t, "profile_fill_result", stdout)
	})
}

// TestDoProfileFill_JSON_AFailedModIsTheEnvelope: a mod that cannot be
// installed - its download changed since the plan - reaches --json as the
// error envelope carrying the whole ProfileFillResult.
func TestDoProfileFill_JSON_AFailedModIsTheEnvelope(t *testing.T) {
	f := newProfileFillFixture(t)
	withFillFlags(t, true, false)
	withJSONOutput(t)
	f.download(t, "10", "ten.zip")
	plan, err := f.svc.PlanProfileFill(context.Background(), f.game, core.ProfileFillRequest{Profile: "Three Mods", Paths: []string{f.downloads}})
	require.NoError(t, err)
	changed := append([]byte(nil), f.archives["10"]...)
	changed[0] ^= 0xff
	require.NoError(t, os.WriteFile(filepath.Join(f.downloads, "ten.zip"), changed, 0o644))
	_, err = f.svc.ApplyProfileFill(context.Background(), f.game, plan, core.ProfileFillOptions{}, nil)
	require.Error(t, err)

	envelope := captureStdout(t, func() error { reportError(err); return nil })
	var doc struct {
		Error   string `json:"error"`
		Details struct {
			Failed    int                `json:"failed"`
			Remaining int                `json:"remaining"`
			Failures  []core.ItemFailure `json:"failures"`
		} `json:"details"`
	}
	require.NoError(t, json.Unmarshal([]byte(envelope), &doc), envelope)
	assert.Equal(t, err.Error(), doc.Error)
	assert.Equal(t, 1, doc.Details.Failed)
	assert.Equal(t, 2, doc.Details.Remaining)
	require.Len(t, doc.Details.Failures, 1)
	assert.Equal(t, "10", doc.Details.Failures[0].ModID)
}
