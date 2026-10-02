package core_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// downloadFailureFixture is a source whose GetDownloadURL fails with err and
// a mod whose page is pageURL: the smallest setup in which a download fails
// the way a real one does, at the source, before any bytes move.
func downloadFailureFixture(t *testing.T, urlErr error, pageURL string) (*core.Service, *domain.Game, *domain.Mod, *domain.DownloadableFile) {
	t.Helper()
	src := newFetchTestSource("dl-src")
	src.urlErr = urlErr
	svc, game := fetchTestGame(t, src, "g")
	mod := &domain.Mod{ID: "42", SourceID: src.ID(), Name: "Item", Version: "7", GameID: game.ID, SourceURL: pageURL}
	file := &domain.DownloadableFile{ID: "item", FileName: "item", IsPrimary: true}
	return svc, game, mod, file
}

// TestDownloadError_CarriesTheModPage pins #513's contract: a failed download
// is a *core.DownloadError that names the mod and carries its page URL as
// data, for a generic failure and for a refused manual download alike, and
// its text is the underlying failure's own.
func TestDownloadError_CarriesTheModPage(t *testing.T) {
	const page = "https://example.test/mods/42"

	t.Run("a generic failure", func(t *testing.T) {
		boom := errors.New("upstream is on fire")
		svc, game, mod, file := downloadFailureFixture(t, boom, page)

		_, err := svc.DownloadModForTest(context.Background(), "dl-src", game, mod, file, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "upstream is on fire", "the cause's own text is kept")
		assert.True(t, errors.Is(err, boom), "the cause is still reachable")

		var dl *core.DownloadError
		require.True(t, errors.As(err, &dl))
		assert.Equal(t, "dl-src", dl.SourceID)
		assert.Equal(t, "42", dl.ModID)
		assert.Equal(t, "Item", dl.ModName)
		assert.Equal(t, page, dl.ModURL)
		assert.False(t, dl.ManualDownload)
	})

	t.Run("a source that refuses automated downloads", func(t *testing.T) {
		refusal := &source.ManualDownloadError{Reason: "the author has turned API downloads off"}
		svc, game, mod, file := downloadFailureFixture(t, refusal, page)

		_, err := svc.DownloadModForTest(context.Background(), "dl-src", game, mod, file, nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "the author has turned API downloads off")
		assert.True(t, errors.Is(err, source.ErrManualDownload))

		var dl *core.DownloadError
		require.True(t, errors.As(err, &dl))
		assert.True(t, dl.ManualDownload)
		assert.Equal(t, page, dl.ModURL)
	})

	t.Run("an HTTP failure fetching the file", func(t *testing.T) {
		svc := newFlowsTestService(t)
		mock := &perModFileSource{mockSourceWithDownloads: newMockSourceWithDownloads("src")}
		defer mock.Close()
		svc.RegisterSource(mock)
		game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
		mod := &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: "g1", SourceURL: page}
		mock.AddMod("g1", mod)
		// No AddDownload: the file's URL answers 404.
		file := &domain.DownloadableFile{ID: "1", FileName: "mod1.zip", IsPrimary: true}

		_, err := svc.DownloadModForTest(context.Background(), "src", game, mod, file, nil)
		require.Error(t, err)

		var dl *core.DownloadError
		require.True(t, errors.As(err, &dl))
		assert.Equal(t, page, dl.ModURL)
		assert.False(t, dl.ManualDownload)
	})

	t.Run("an unsafe page URL is not carried", func(t *testing.T) {
		for _, bad := range []string{"javascript:alert(1)", "file:///etc/passwd", "data:text/html,x", "/relative", ""} {
			svc, game, mod, file := downloadFailureFixture(t, errors.New("nope"), bad)
			_, err := svc.DownloadModForTest(context.Background(), "dl-src", game, mod, file, nil)
			var dl *core.DownloadError
			require.True(t, errors.As(err, &dl), bad)
			assert.Empty(t, dl.ModURL, "%q must never become a link", bad)
		}
	})

	t.Run("a cancelled download is not a download failure to link", func(t *testing.T) {
		svc, game, mod, file := downloadFailureFixture(t, context.Canceled, page)
		_, err := svc.DownloadModForTest(context.Background(), "dl-src", game, mod, file, nil)
		require.Error(t, err)
		var dl *core.DownloadError
		assert.False(t, errors.As(err, &dl))
		assert.True(t, errors.Is(err, context.Canceled))
	})
}

// TestDownloadError_WorkshopFailureKeepsItsTypedDetails: wrapping must not
// hide the Steam Workshop explainer behind the new type.
func TestDownloadError_WorkshopFailureKeepsItsTypedDetails(t *testing.T) {
	src := newFetchTestSource("fetch-src")
	src.fetch = func(string) (string, error) {
		return "", &domain.WorkshopFetchFailure{
			AppID: "1133870", PublishedFileID: "42", Tool: "steamcmd",
			Reason: "publisher says no", Err: domain.ErrWorkshopAnonymousRefused,
		}
	}
	svc, game := fetchTestGame(t, src, "1133870")
	mod := &domain.Mod{ID: "42", SourceID: src.ID(), Name: "Item", Version: "7", GameID: game.ID, SourceURL: "https://steamcommunity.test/42"}
	file := &domain.DownloadableFile{ID: "item", FileName: "item", IsPrimary: true}

	_, err := svc.DownloadModForTest(context.Background(), src.ID(), game, mod, file, nil)
	require.Error(t, err)

	var workshop *core.WorkshopFetchError
	require.True(t, errors.As(err, &workshop))
	var dl *core.DownloadError
	require.True(t, errors.As(err, &dl))
	assert.Equal(t, "https://steamcommunity.test/42", dl.ModURL)

	// One flat envelope: the Workshop keys the SPA already reads, plus the page.
	var buf bytes.Buffer
	require.NoError(t, core.EncodeJSON(&buf, dl.Details()))
	assert.Contains(t, buf.String(), `"published_file_id": "42"`)
	assert.Contains(t, buf.String(), `"reason": "publisher says no"`)
	assert.Contains(t, buf.String(), `"mod_url": "https://steamcommunity.test/42"`)
}

// TestApplyInstall_DownloadFailureCarriesTheModPage: the install flow
// returns the typed error through its own "download failed" wrapping, for a
// plain failure and - without matching any text - for a refused manual
// download.
func TestApplyInstall_DownloadFailureCarriesTheModPage(t *testing.T) {
	const page = "https://example.test/mods/mod1"
	install := func(t *testing.T, urlErr error) error {
		t.Helper()
		src := newFetchTestSource("src")
		src.urlErr = urlErr
		svc, game := fetchTestGame(t, src, "g")
		src.AddMod("g", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "1.0", GameID: game.ID, SourceURL: page})
		plan, err := svc.PlanInstall(context.Background(), game, "default", "src", "mod1", false)
		require.NoError(t, err)
		_, err = svc.ApplyInstall(context.Background(), game, plan, core.InstallOptions{}, nil)
		require.Error(t, err)
		return err
	}

	t.Run("a plain failure", func(t *testing.T) {
		err := install(t, errors.New("upstream is on fire"))
		assert.Contains(t, err.Error(), "download failed")
		assert.Contains(t, err.Error(), "upstream is on fire")
		var dl *core.DownloadError
		require.True(t, errors.As(err, &dl))
		assert.Equal(t, page, dl.ModURL)
		assert.False(t, dl.ManualDownload)
	})

	t.Run("a manual download", func(t *testing.T) {
		err := install(t, &source.ManualDownloadError{Reason: "the author turned API downloads off"})
		assert.Contains(t, err.Error(), "download unavailable via API")
		assert.Contains(t, err.Error(), "the author turned API downloads off", "the reason is no longer dropped")
		var dl *core.DownloadError
		require.True(t, errors.As(err, &dl))
		assert.Equal(t, page, dl.ModURL)
		assert.True(t, dl.ManualDownload)
	})
}

// TestApplyUpdate_DownloadFailureCarriesTheModPage: the update flow's
// "downloading update" failure carries the page of the mod being updated.
func TestApplyUpdate_DownloadFailureCarriesTheModPage(t *testing.T) {
	const page = "https://example.test/mods/mod1"
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-new.esp", IsPrimary: true}},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1", SourceURL: page})
	// No AddDownload("new-1", ...): the download 404s.

	plan, err := svc.NewUpdatePlanForApplyTest(context.Background(), game.ID, "default", domain.Update{InstalledMod: *old, NewVersion: "2.0"})
	require.NoError(t, err)
	_, err = svc.ApplyUpdate(context.Background(), game, plan, core.UpdateOptions{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "downloading update:")

	var dl *core.DownloadError
	require.True(t, errors.As(err, &dl))
	assert.Equal(t, "src", dl.SourceID)
	assert.Equal(t, "mod1", dl.ModID)
	assert.Equal(t, page, dl.ModURL)
}

// TestApplyUpdateBatch_DownloadFailureCarriesTheModPage: a batch item that
// fails to download keeps the typed error for an in-process caller and puts
// the page on the wire, which is all a job result document has.
func TestApplyUpdateBatch_DownloadFailureCarriesTheModPage(t *testing.T) {
	const page = "https://example.test/mods/mod1"
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"}, map[string][]byte{"mod1-old.esp": []byte("old")})

	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-new.esp", IsPrimary: true}},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1", SourceURL: page})

	plan, err := svc.PlanUpdateBatchFrom(context.Background(), game, "default", []domain.Update{{InstalledMod: *old, NewVersion: "2.0"}}, nil)
	require.NoError(t, err)
	result, err := svc.ApplyUpdateBatch(context.Background(), game, plan, core.UpdateBatchOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, result.Failed, 1)

	assert.Equal(t, page, result.Failed[0].ModURL)
	var dl *core.DownloadError
	require.True(t, errors.As(result.Failed[0].Cause(), &dl))
	assert.Equal(t, page, dl.ModURL)
}

// detailsOf is what the --json / /api/v1 error envelope would attach for err.
func detailsOf(err error) any {
	var withDetails interface{ Details() any }
	if errors.As(err, &withDetails) {
		return withDetails.Details()
	}
	return nil
}

// TestDownloadError_DoesNotShadowAnotherTypedRefusal: a refusal that comes out
// of the download path with typed details of its own (a game's loader
// precondition, say) keeps them - the envelope must not be replaced by the
// download's, or the web UI loses the setup steps it renders from them.
func TestDownloadError_DoesNotShadowAnotherTypedRefusal(t *testing.T) {
	refusal := &core.LoaderRequiredError{}
	svc, game, mod, file := downloadFailureFixture(t, refusal, "https://example.test/mods/42")

	_, err := svc.DownloadModForTest(context.Background(), "dl-src", game, mod, file, nil)
	require.Error(t, err)

	var dl *core.DownloadError
	assert.False(t, errors.As(err, &dl), "a typed refusal is not re-labelled as a download failure")
	assert.Equal(t, refusal.Details(), detailsOf(err))
}
