package core_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fetchTestSource is a source that cannot serve download URLs at all and
// retrieves files itself instead - the shape source.Fetcher exists for
// (#269 Tier 3's steamcmd shell-out), with the subprocess replaced by a
// function so the seam itself can be tested without one.
type fetchTestSource struct {
	*mockSource

	// urlErr is what GetDownloadURL returns. The default (set by
	// newFetchTestSource) is ErrNotSupported, the ONLY error core is
	// allowed to fall back to Fetch on.
	urlErr error

	// fetch does the work. It is handed exactly what core promises a
	// Fetcher: a destDir core created and owns, and a mod whose GameID is
	// the SOURCE's game id.
	fetch func(destDir string) (string, error)

	// seen records the arguments of the last Fetch call.
	seenGameID  string
	seenFileID  string
	seenDestDir string
	seenPhases  []string
}

func newFetchTestSource(id string) *fetchTestSource {
	return &fetchTestSource{mockSource: newMockSource(id), urlErr: source.ErrNotSupported}
}

func (s *fetchTestSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{{ID: "item", Name: "Item", FileName: "item", IsPrimary: true}}, nil
}

func (s *fetchTestSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", fmt.Errorf("source %q: downloads: %w", s.ID(), s.urlErr)
}

func (s *fetchTestSource) Fetch(_ context.Context, mod *domain.Mod, fileID, destDir string, progress source.FetchProgressFunc) (string, error) {
	s.seenGameID, s.seenFileID, s.seenDestDir = mod.GameID, fileID, destDir
	progress(source.FetchPhaseStarted, "starting", 0)
	path, err := s.fetch(destDir)
	if err != nil {
		return "", err
	}
	progress(source.FetchPhaseProgress, "halfway", 512)
	progress(source.FetchPhaseDone, "done", 1024)
	return path, nil
}

var _ source.Fetcher = (*fetchTestSource)(nil)

// fetchTestGame returns a service, a game mapped to src, and src registered.
func fetchTestGame(t *testing.T, src source.ModSource, sourceGameID string) (*core.Service, *domain.Game) {
	t.Helper()
	svc := newFlowsTestService(t)
	svc.RegisterSource(src)
	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{src.ID(): sourceGameID},
	}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return svc, game
}

// TestDownloadMod_FallsBackToFetchWhenTheSourceServesNoURL is the seam's
// happy path: GetDownloadURL says ErrNotSupported, core creates a staging
// directory of its own, hands it to Fetch, and ingests the directory tree
// that comes back through the ordinary local-ingest path.
func TestDownloadMod_FallsBackToFetchWhenTheSourceServesNoURL(t *testing.T) {
	src := newFetchTestSource("fetch-src")
	src.fetch = func(destDir string) (string, error) {
		content := filepath.Join(destDir, "steamapps", "workshop", "content", "1133870", "42")
		if err := os.MkdirAll(content, 0755); err != nil {
			return "", err
		}
		return content, os.WriteFile(filepath.Join(content, "mod.txt"), []byte("workshop bytes"), 0644)
	}
	svc, game := fetchTestGame(t, src, "1133870")

	mod := &domain.Mod{ID: "42", SourceID: src.ID(), Name: "Item", Version: "7", GameID: game.ID}
	file := &domain.DownloadableFile{ID: "item", Name: "Item", FileName: "item", IsPrimary: true}

	var phases []core.DeployPhase
	result, err := svc.DownloadModForTest(context.Background(), src.ID(), game, mod, file, func(e core.Event) {
		if step, ok := e.(core.StepEvent); ok {
			phases = append(phases, step.Phase)
		}
	})
	require.NoError(t, err)
	assert.Equal(t, 1, result.FilesExtracted)

	// The bytes are in the cache, as an ORDINARY mod: nothing about the
	// fetch path leaves a Workshop-shaped trace downstream.
	cached, err := svc.GetGameCache(game).ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	require.NoError(t, err)
	assert.Equal(t, []string{"mod.txt"}, cached)

	// The Fetcher contract: core's own staging dir, and the SOURCE's game
	// id rather than lmm's.
	assert.Equal(t, "1133870", src.seenGameID, "Fetch must receive the source's game id, not lmm's")
	assert.Equal(t, "item", src.seenFileID)
	assert.NotEmpty(t, src.seenDestDir)

	// The generic fetch phases reach the flow's event stream as the three
	// workshop-fetch DeployPhases.
	assert.Equal(t, []core.DeployPhase{core.WorkshopFetchStarted, core.WorkshopFetchProgress, core.WorkshopFetchDone}, phases)

	// The staging directory core created is gone once the fetch is ingested.
	assert.NoDirExists(t, src.seenDestDir)
}

// TestDownloadMod_RefusesAFetchedPathOutsideTheStagingDirectory is the
// seam's security rule, the Fetcher counterpart of #300's file:// guard: a
// source may write into the directory core gave it and nowhere else, so a
// path outside it is refused and nothing is ingested.
func TestDownloadMod_RefusesAFetchedPathOutsideTheStagingDirectory(t *testing.T) {
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("not yours"), 0644))

	src := newFetchTestSource("fetch-src")
	src.fetch = func(string) (string, error) { return outside, nil }
	svc, game := fetchTestGame(t, src, "1133870")

	mod := &domain.Mod{ID: "42", SourceID: src.ID(), Name: "Item", Version: "7", GameID: game.ID}
	file := &domain.DownloadableFile{ID: "item", FileName: "item", IsPrimary: true}

	_, err := svc.DownloadModForTest(context.Background(), src.ID(), game, mod, file, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside the staging directory")

	_, cacheErr := svc.GetGameCache(game).ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	assert.Error(t, cacheErr, "nothing may be ingested from outside the staging directory")
}

// TestDownloadMod_RefusesAFetchedPathReachedByASymlinkOutOfTheStagingDir
// closes the same rule's obvious hole: a symlink core would otherwise
// happily follow out of the directory it owns.
func TestDownloadMod_RefusesAFetchedPathReachedByASymlinkOutOfTheStagingDir(t *testing.T) {
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("not yours"), 0644))

	src := newFetchTestSource("fetch-src")
	src.fetch = func(destDir string) (string, error) {
		link := filepath.Join(destDir, "content")
		return link, os.Symlink(outside, link)
	}
	svc, game := fetchTestGame(t, src, "1133870")

	mod := &domain.Mod{ID: "42", SourceID: src.ID(), Version: "7", GameID: game.ID}
	file := &domain.DownloadableFile{ID: "item", FileName: "item", IsPrimary: true}

	_, err := svc.DownloadModForTest(context.Background(), src.ID(), game, mod, file, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside the staging directory")
}

// TestDownloadMod_DoesNotFallBackToFetchOnARealDownloadURLFailure pins the
// gate: only ErrNotSupported means "I cannot serve URLs". Any other failure
// from GetDownloadURL is a failure, even for a source that could fetch.
func TestDownloadMod_DoesNotFallBackToFetchOnARealDownloadURLFailure(t *testing.T) {
	src := newFetchTestSource("fetch-src")
	src.urlErr = errors.New("upstream is on fire")
	fetched := false
	src.fetch = func(string) (string, error) { fetched = true; return "", nil }
	svc, game := fetchTestGame(t, src, "1133870")

	mod := &domain.Mod{ID: "42", SourceID: src.ID(), Version: "7", GameID: game.ID}
	file := &domain.DownloadableFile{ID: "item", FileName: "item", IsPrimary: true}

	_, err := svc.DownloadModForTest(context.Background(), src.ID(), game, mod, file, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "upstream is on fire")
	assert.False(t, fetched, "a non-ErrNotSupported failure must not be rerouted through Fetch")
}

// TestDownloadMod_FetchFailureBecomesTheTypedWorkshopFetchError proves the
// error taxonomy survives the seam: the sentinel a source wraps stays
// findable with errors.Is, and the structured detail reaches the frontend
// as core.WorkshopFetchError rather than as a subprocess's stderr.
func TestDownloadMod_FetchFailureBecomesTheTypedWorkshopFetchError(t *testing.T) {
	src := newFetchTestSource("fetch-src")
	src.fetch = func(string) (string, error) {
		return "", &domain.WorkshopFetchFailure{
			AppID: "1133870", PublishedFileID: "42", Tool: "steamcmd",
			Reason: "publisher says no", Err: domain.ErrWorkshopAnonymousRefused,
		}
	}
	svc, game := fetchTestGame(t, src, "1133870")

	mod := &domain.Mod{ID: "42", SourceID: src.ID(), Version: "7", GameID: game.ID}
	file := &domain.DownloadableFile{ID: "item", FileName: "item", IsPrimary: true}

	_, err := svc.DownloadModForTest(context.Background(), src.ID(), game, mod, file, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, domain.ErrWorkshopAnonymousRefused))

	var typed *core.WorkshopFetchError
	require.True(t, errors.As(err, &typed))
	assert.Equal(t, "1133870", typed.AppID)
	assert.Equal(t, "42", typed.PublishedFileID)
	assert.Equal(t, "steamcmd", typed.Tool)
	assert.Equal(t, "publisher says no", typed.Reason)
	assert.True(t, strings.Contains(typed.Error(), "publisher says no"))
}

// exactSizeSource serves an ordinary download URL and declares its sizes to
// the byte (source.ExactFileSizer) - the Workshop's legacy file_url shape,
// where the CDN publishes no checksum and the API's file_size is the only
// integrity check there is.
type exactSizeSource struct {
	*mockSourceWithDownloads
	exact bool
	size  int64
}

func (s *exactSizeSource) ExactFileSizes() bool { return s.exact }

func (s *exactSizeSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	return []domain.DownloadableFile{{ID: "1", Name: "Main", FileName: mod.ID + ".txt", Size: s.size, IsPrimary: true}}, nil
}

// TestDownloadMod_ExactFileSizerMismatchIsAHardFailure pins Tier 3's Path A
// integrity rule: a body that does not match the declared byte count fails
// the download outright, and leaves no partial behind.
func TestDownloadMod_ExactFileSizerMismatchIsAHardFailure(t *testing.T) {
	base := newMockSourceWithDownloads("exact-src")
	base.payloads["1"] = []byte("twelve bytes")
	src := &exactSizeSource{mockSourceWithDownloads: base, exact: true, size: 999}
	svc, game := fetchTestGame(t, src, "g1")

	mod := &domain.Mod{ID: "m1", SourceID: src.ID(), Version: "1.0", GameID: game.ID}
	files, err := svc.GetModFiles(context.Background(), src.ID(), mod)
	require.NoError(t, err)

	_, err = svc.DownloadModForTest(context.Background(), src.ID(), game, mod, &files[0], nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "size mismatch")

	_, cacheErr := svc.GetGameCache(game).ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	assert.Error(t, cacheErr, "a size-mismatched download must not reach the cache")
}

// TestDownloadMod_DeclaredSizeIsAdvisoryForOrdinarySources is the other
// half of the same rule, and the reason it is opt-in: every existing
// source's declared size stays advisory, so a rounded or stale one keeps
// installing exactly as it always has.
func TestDownloadMod_DeclaredSizeIsAdvisoryForOrdinarySources(t *testing.T) {
	base := newMockSourceWithDownloads("loose-src")
	base.payloads["1"] = []byte("twelve bytes")
	src := &exactSizeSource{mockSourceWithDownloads: base, exact: false, size: 999}
	svc, game := fetchTestGame(t, src, "g1")

	mod := &domain.Mod{ID: "m1", SourceID: src.ID(), Version: "1.0", GameID: game.ID}
	files, err := svc.GetModFiles(context.Background(), src.ID(), mod)
	require.NoError(t, err)

	_, err = svc.DownloadModForTest(context.Background(), src.ID(), game, mod, &files[0], nil)
	require.NoError(t, err)
}
