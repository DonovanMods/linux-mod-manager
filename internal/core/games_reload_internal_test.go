package core

// #376 + P2 review Minor 3. ReloadGames is the one path that can change a
// game's ModPath/InstallPath under a LIVE Service, and it deliberately does
// not take beginOp - taking it would put every HTTP request behind the
// single mutation slot, and would drop the verify memo on every request.
// beginOp is where every other mutation's dropVerifyMemo() lives, so this
// path has to drop it itself.
//
// The memo cannot notice the change on its own: verifyMemoKey is
// (game, profile, tier) and carries nothing about the game's
// configuration, and verifyFingerprint walks game.ModPath without ever
// hashing the game record. Repoint mod_path at a directory whose stat-only
// tree fingerprint matches the old one - "both empty", which is exactly the
// state right after a repoint - and the Health card keeps answering with
// the previous verdict for the previous directory.
//
// An internal test because the memo is internal: the seams asserted here
// (verifyMemoStore/verifyMemoLookup) are the ones verify.go itself uses.

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newMemoReloadService(t *testing.T) (*Service, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	configDir := t.TempDir()
	svc, err := NewService(ServiceConfig{
		ConfigDir: configDir,
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	return svc, configDir
}

// TestReloadGames_DropsTheVerifyMemoWhenTheGameSetChanges pins both
// directions: a reload that happened invalidates, and a stat that found
// nothing to do does not. The second half is what keeps this cheap -
// internal/serve calls ReloadGames on every wrapped request, and dropping
// the memo unconditionally there would be the very per-request invalidation
// not taking beginOp exists to avoid.
func TestReloadGames_DropsTheVerifyMemoWhenTheGameSetChanges(t *testing.T) {
	svc, configDir := newMemoReloadService(t)

	dir := t.TempDir()
	body := "games:\n" +
		"    g1:\n" +
		"        name: Fixture\n" +
		"        install_path: " + dir + "\n" +
		"        mod_path: " + dir + "\n" +
		"        link_method: symlink\n"
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "games.yaml"), []byte(body), 0o644))
	reloaded, err := svc.ReloadGames()
	require.NoError(t, err)
	require.True(t, reloaded)

	key := verifyMemoKey("g1", "default", VerifyLocal)
	const fingerprint = "fp"
	svc.verifyMemoStore(key, fingerprint, &VerifyResult{Checked: 7})
	require.NotNil(t, svc.verifyMemoLookup(key, fingerprint),
		"the memo must be filed, or the assertions below prove nothing")

	reloaded, err = svc.ReloadGames()
	require.NoError(t, err)
	require.False(t, reloaded, "an unmoved games.yaml is not a change")
	assert.NotNil(t, svc.verifyMemoLookup(key, fingerprint),
		"a request that found nothing to re-read must not cost the memo")

	// The repoint the memo cannot see: a different mod_path whose tree
	// fingerprints identically to the old one, because both are empty.
	moved := t.TempDir()
	body = "games:\n" +
		"    g1:\n" +
		"        name: Fixture\n" +
		"        install_path: " + moved + "\n" +
		"        mod_path: " + moved + "\n" +
		"        link_method: symlink\n"
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "games.yaml"), []byte(body), 0o644))

	reloaded, err = svc.ReloadGames()
	require.NoError(t, err)
	require.True(t, reloaded)
	assert.Nil(t, svc.verifyMemoLookup(key, fingerprint),
		"a game repointed at another directory must not keep the previous directory's verdict")
}
