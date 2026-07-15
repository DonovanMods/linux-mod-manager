package core_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// downloadWithSHA256 runs one DownloadMod through a mock source serving
// content, with expectedSHA declared on the file. Returns the error.
func downloadWithSHA256(t *testing.T, content []byte, expectedSHA string) (error, *domain.Game, *domain.Mod, func() bool) {
	t.Helper()

	cfg := core.ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()}
	svc, err := core.NewService(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	mock := newMockSourceWithDownloads("test")
	defer mock.Close()
	svc.RegisterSource(mock)

	game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: filepath.Join(t.TempDir(), "mods"), DeployMode: domain.DeployCopy}
	require.NoError(t, svc.AddGame(game))

	mod := &domain.Mod{ID: "m1", SourceID: "test", Name: "Mod", Version: "1.0.0", GameID: "testgame"}
	file := &domain.DownloadableFile{ID: "file1", Name: "File", FileName: "m1.zip", SHA256: expectedSHA}

	mock.AddDownload(file.ID, content)

	_, err = svc.DownloadMod(context.Background(), "test", game, mod, file, nil)
	gameCache := svc.GetGameCache(game)
	cached := func() bool { return gameCache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) }
	return err, game, mod, cached
}

func TestDownloadModVerifiesDeclaredSHA256(t *testing.T) {
	content := []byte("mod archive bytes")
	sum := sha256.Sum256(content)
	good := hex.EncodeToString(sum[:])

	t.Run("matching hash passes", func(t *testing.T) {
		err, _, _, cached := downloadWithSHA256(t, content, good)
		require.NoError(t, err)
		assert.True(t, cached())
	})

	t.Run("uppercase hash passes (case-insensitive)", func(t *testing.T) {
		err, _, _, cached := downloadWithSHA256(t, content, strings.ToUpper(good))
		require.NoError(t, err)
		assert.True(t, cached())
	})

	t.Run("mismatched hash fails and nothing is cached", func(t *testing.T) {
		err, _, _, cached := downloadWithSHA256(t, content, strings.Repeat("ab", 32))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sha256 mismatch")
		assert.False(t, cached())
	})

	t.Run("empty hash skips verification", func(t *testing.T) {
		err, _, _, cached := downloadWithSHA256(t, content, "")
		require.NoError(t, err)
		assert.True(t, cached())
	})
}
