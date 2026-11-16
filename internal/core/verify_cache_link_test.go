package core_test

// #659: a symlink or special file left inside a cache entry - by an
// extraction from before #658's hardening - is a cache_link finding naming
// the mod and the entry-relative path. verify never follows it while
// looking, --fix removes it, and a removed member that stood where one of
// the row's recorded files belongs is rebuilt by the existing
// cache_incomplete re-download.

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// verifyWithin runs verify, failing the test instead of hanging when the
// run blocks - which it does if anything opens the FIFO a planted link
// points at.
func verifyWithin(t *testing.T, svc *core.Service, game *domain.Game, opts core.VerifyOptions) *core.VerifyResult {
	t.Helper()
	type outcome struct {
		result *core.VerifyResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := svc.VerifyForTest(context.Background(), game, "default", opts, nil)
		done <- outcome{r, err}
	}()
	select {
	case o := <-done:
		require.NoError(t, o.err)
		return o.result
	case <-time.After(60 * time.Second):
		t.Fatal("verify blocked: it opened a link's target or a special file")
		return nil
	}
}

// outsideTargets makes, outside the cache, a FIFO that blocks whoever opens
// it and a directory nobody may list or enter, each holding something
// that would itself be a finding if verify got there.
func outsideTargets(t *testing.T) (fifo, lockedDir string) {
	t.Helper()
	outside := t.TempDir()
	fifo = filepath.Join(outside, "pipe")
	require.NoError(t, syscall.Mkfifo(fifo, 0o644))
	lockedDir = filepath.Join(outside, "locked")
	require.NoError(t, os.MkdirAll(lockedDir, 0o755))
	require.NoError(t, os.Symlink("/etc/passwd", filepath.Join(lockedDir, "inner-link")))
	require.NoError(t, syscall.Mkfifo(filepath.Join(lockedDir, "inner-pipe"), 0o644))
	require.NoError(t, os.Chmod(lockedDir, 0))
	t.Cleanup(func() { _ = os.Chmod(lockedDir, 0o755) })
	return fifo, lockedDir
}

func cacheLinkFindings(result *core.VerifyResult) map[string]core.VerifyFinding {
	out := make(map[string]core.VerifyFinding)
	for _, f := range result.Findings {
		if f.Status == core.VerifyStatusCacheLink || f.Status == core.VerifyStatusFixedCacheLink {
			out[f.FileID] = f
		}
	}
	return out
}

// plantLinks puts, in mod1's entry, a symlink to an outside FIFO, a
// symlink to an outside directory, a symlink to a file that does not
// exist, and a FIFO in a subdirectory - none of them a recorded member.
func plantLinks(t *testing.T, entry string) (fifo string) {
	t.Helper()
	fifo, lockedDir := outsideTargets(t)
	require.NoError(t, os.Symlink(fifo, filepath.Join(entry, "evil-pipe-link")))
	require.NoError(t, os.Symlink(lockedDir, filepath.Join(entry, "evil-dir")))
	require.NoError(t, os.Symlink("../../../nowhere", filepath.Join(entry, "dangling")))
	require.NoError(t, os.MkdirAll(filepath.Join(entry, "sub"), 0o755))
	require.NoError(t, syscall.Mkfifo(filepath.Join(entry, "sub", "pipe"), 0o644))
	return fifo
}

func TestVerify_CacheLink_ReportedWithoutFollowing(t *testing.T) {
	svc, game, _ := installSwapMod(t, domain.LinkCopy, "20", "21")
	c := svc.GetGameCache(game)
	k := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	plantLinks(t, c.ModPath(k))

	result := verifyWithin(t, svc, game, core.VerifyOptions{})
	links := cacheLinkFindings(result)
	require.Len(t, links, 4, "%+v", result.Findings)
	for path, kind := range map[string]string{
		"evil-pipe-link": "symbolic link",
		"evil-dir":       "symbolic link",
		"dangling":       "symbolic link",
		"sub/pipe":       "named pipe",
	} {
		f, ok := links[path]
		require.True(t, ok, "no finding for %s: %+v", path, result.Findings)
		assert.Equal(t, core.VerifyStatusCacheLink, f.Status)
		assert.Equal(t, "mod1", f.ModID)
		assert.Equal(t, "Mod One", f.ModName)
		assert.Contains(t, f.Note, path)
		assert.Contains(t, f.Note, kind)
		assert.True(t, f.Fixable, "--fix removes it")
		assert.Empty(t, f.FixableReason)
	}
	for path := range links {
		assert.NotContains(t, path, "inner", "the walk never descends through a directory link")
	}
	assert.Equal(t, 4, result.Warnings, "%+v", result.Findings)
	assert.Equal(t, 0, result.Issues, "%+v", result.Findings)
	assert.Equal(t, "ok", modFinding(t, result, "20").Status)
	assert.Equal(t, "ok", modFinding(t, result, "21").Status)
}

func TestVerify_CacheLink_ModFilterScopesTheCheck(t *testing.T) {
	svc, game, _ := installSwapMod(t, domain.LinkCopy, "20", "21")
	c := svc.GetGameCache(game)
	plantLinks(t, c.ModPath(cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}))

	result := verifyWithin(t, svc, game, core.VerifyOptions{ModFilter: "other"})
	assert.Empty(t, cacheLinkFindings(result), "%+v", result.Findings)
	result = verifyWithin(t, svc, game, core.VerifyOptions{ModFilter: "mod1"})
	assert.Len(t, cacheLinkFindings(result), 4, "%+v", result.Findings)
}

func TestVerify_CacheLink_FixRemovesThem(t *testing.T) {
	svc, game, src := installSwapMod(t, domain.LinkCopy, "20", "21")
	c := svc.GetGameCache(game)
	k := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	entry := c.ModPath(k)
	fifo := plantLinks(t, entry)
	downloads := src.DownloadCount()

	result := verifyWithin(t, svc, game, core.VerifyOptions{Fix: true})
	links := cacheLinkFindings(result)
	require.Len(t, links, 4, "%+v", result.Findings)
	for path, f := range links {
		assert.Equal(t, core.VerifyStatusFixedCacheLink, f.Status, path)
		assert.Contains(t, f.Note, path)
		assert.False(t, f.Fixable, "a row a --fix run produced is past fixing")
		_, err := os.Lstat(filepath.Join(entry, filepath.FromSlash(path)))
		assert.ErrorIs(t, err, os.ErrNotExist, "%s is removed", path)
	}
	assert.Equal(t, 0, result.Warnings, "%+v", result.Findings)
	assert.Equal(t, 0, result.Issues, "%+v", result.Findings)
	assert.Equal(t, downloads, src.DownloadCount(), "nothing recorded was removed, so nothing is downloaded")

	info, err := os.Lstat(fifo)
	require.NoError(t, err, "the link's target outside the entry is untouched")
	assert.NotZero(t, info.Mode()&os.ModeNamedPipe)
	assert.DirExists(t, filepath.Join(entry, "sub"), "the directory the FIFO was in stays")
	assert.FileExists(t, filepath.Join(entry, "mod1-a.esp"))
	assert.True(t, c.HasFileIDs(k, []string{"20", "21"}))

	again := verifyWithin(t, svc, game, core.VerifyOptions{})
	assert.Empty(t, cacheLinkFindings(again))
	assert.Equal(t, 0, again.Warnings, "%+v", again.Findings)
	assert.Equal(t, 0, again.Issues, "%+v", again.Findings)
}

// A link standing where one of the row's recorded members belongs: --fix
// removes it, which leaves the entry incomplete, and the same run's
// cache_incomplete re-download puts the real file back.
func TestVerify_CacheLink_FixOfRecordedMemberRedownloads(t *testing.T) {
	svc, game, src := installSwapMod(t, domain.LinkCopy, "20", "21")
	c := svc.GetGameCache(game)
	k := cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"}
	entry := c.ModPath(k)
	fifo, _ := outsideTargets(t)
	member := filepath.Join(entry, "mod1-a.esp")
	require.NoError(t, os.Remove(member))
	require.NoError(t, os.Symlink(fifo, member))

	plain := verifyWithin(t, svc, game, core.VerifyOptions{})
	row, ok := cacheLinkFindings(plain)["mod1-a.esp"]
	require.True(t, ok, "%+v", plain.Findings)
	assert.Contains(t, row.Note, "where one of the mod's files belongs")
	assert.Contains(t, row.Note, "downloads the file again")
	assert.True(t, row.Fixable)
	// The link is not the file's content, so the plain run already reads
	// the file as lost - and the deployed copy as kept, never stale.
	assert.Equal(t, core.VerifyStatusCacheIncomplete, modFinding(t, plain, "20").Status, "%+v", plain.Findings)
	assert.Empty(t, findingsWithStatus(plain, "stale_deployment"), "%+v", plain.Findings)

	downloads := src.DownloadCount()
	fixed := verifyWithin(t, svc, game, core.VerifyOptions{Fix: true})
	assert.Equal(t, core.VerifyStatusFixedCacheLink, cacheLinkFindings(fixed)["mod1-a.esp"].Status, "%+v", fixed.Findings)
	refilled := modFinding(t, fixed, "20")
	assert.Equal(t, "ok", refilled.Status, "%+v", fixed.Findings)
	assert.Equal(t, "Re-downloaded OK", refilled.Repair)
	assert.Equal(t, downloads+1, src.DownloadCount())
	assert.Equal(t, 0, fixed.Issues, "%+v", fixed.Findings)
	assert.Equal(t, 0, fixed.Warnings, "%+v", fixed.Findings)

	info, err := os.Lstat(member)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "the member is a real file again")
	b, err := os.ReadFile(member)
	require.NoError(t, err)
	assert.Equal(t, "a", string(b))
	info, err = os.Lstat(fifo)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeNamedPipe, "the outside target is untouched")
}

// A locally imported mod cannot be downloaded again: --fix still removes
// the link, and the file it stood for is reported as needing the archive
// imported again (the cache_incomplete row's own reason).
func TestVerify_CacheLink_FixOfRecordedMemberOfLocalModNeedsManualAction(t *testing.T) {
	svc, game := newDefaultProfileService(t)
	game.LinkMethod, game.LinkMethodExplicit = domain.LinkCopy, true
	ctx := context.Background()
	archive := filepath.Join(t.TempDir(), "LocalMod-1.0.zip")
	createImportTestZip(t, archive, map[string]string{"local-a.esp": "a", "local-b.esp": "b"})
	_, err := svc.ImportArchive(ctx, game, "default", archive, core.ImportArchiveOptions{Force: true}, nil)
	require.NoError(t, err)
	mods, err := svc.GetInstalledMods(ctx, game.ID, "default")
	require.NoError(t, err)
	require.Len(t, mods, 1)
	row := mods[0]
	c := svc.GetGameCache(game)
	k := cacheKeyOf(&row)
	require.NoError(t, cache.MarkFileCompleteWithMembers(c.ModPath(k), "LocalMod-1.0.zip", []string{"local-a.esp", "local-b.esp"}))
	row.FileIDs = []string{"LocalMod-1.0.zip"}
	require.NoError(t, svc.SaveInstalledMod(ctx, &row))
	require.NoError(t, svc.SaveFileChecksum(ctx, row.SourceID, row.ID, game.ID, "default", "LocalMod-1.0.zip", "abc123"))
	member := filepath.Join(c.ModPath(k), "local-a.esp")
	require.NoError(t, os.Remove(member))
	require.NoError(t, os.Symlink("/etc/hostname", member))

	plain := verifyWithin(t, svc, game, core.VerifyOptions{})
	link, ok := cacheLinkFindings(plain)["local-a.esp"]
	require.True(t, ok, "%+v", plain.Findings)
	assert.Contains(t, link.Note, "imported again")

	fixed := verifyWithin(t, svc, game, core.VerifyOptions{Fix: true})
	assert.Equal(t, core.VerifyStatusFixedCacheLink, cacheLinkFindings(fixed)["local-a.esp"].Status, "%+v", fixed.Findings)
	_, err = os.Lstat(member)
	assert.ErrorIs(t, err, os.ErrNotExist)
	lost := findingsWithStatus(fixed, core.VerifyStatusCacheIncomplete)
	require.NotEmpty(t, lost, "%+v", fixed.Findings)
	assert.Contains(t, lost[0].Note, "local-a.esp")
	assert.False(t, lost[0].Fixable)
	assert.Contains(t, lost[0].FixableReason, "imported locally")
}

// #659 deploy: a FIFO left in a cache entry is never deployed. A copy
// deploy opened it and blocked; a symlink or hard-link deploy put it in the
// game directory.
func TestDeploy_NeverDeploysASpecialFileFromTheCache(t *testing.T) {
	for _, method := range []domain.LinkMethod{domain.LinkCopy, domain.LinkSymlink, domain.LinkHardlink} {
		t.Run(method.String(), func(t *testing.T) {
			svc, game, _ := installSwapMod(t, method, "20", "21")
			c := svc.GetGameCache(game)
			entry := c.ModPath(cache.Key{GameID: game.ID, SourceID: "src", ModID: "mod1", Version: "1.0"})
			require.NoError(t, syscall.Mkfifo(filepath.Join(entry, "pipe.esp"), 0o644))
			fifo, _ := outsideTargets(t)
			require.NoError(t, os.Symlink(fifo, filepath.Join(entry, "link.esp")))

			done := make(chan error, 1)
			go func() {
				_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, nil)
				done <- err
			}()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(60 * time.Second):
				t.Fatal("deploy blocked opening a special file in the cache")
			}
			assert.False(t, deployedFile(t, game, "pipe.esp"), "a FIFO is never deployed")
			assert.False(t, deployedFile(t, game, "link.esp"), "a link is never deployed")
			assert.True(t, deployedFile(t, game, "mod1-a.esp"))
		})
	}
}
