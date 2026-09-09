package core_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- #246: a source whose mod document carries no full description (
// CurseForge, since #235) supplies it through source.DescriptionFetcher,
// which ONLY Service.ModDetail may consume - never search, never an update
// check, each of which would pay a round trip per row.

// describingMockSource embeds mockSource and adds source.DescriptionFetcher,
// counting calls so a test can prove which flows reach it. Mirrors
// changelogMockSource's embedding pattern.
type describingMockSource struct {
	*mockSource
	description string
	err         error
	calls       atomic.Int64
}

func newDescribingMockSource(id string) *describingMockSource {
	return &describingMockSource{mockSource: newMockSource(id)}
}

func (d *describingMockSource) Description(_ context.Context, _, _ string) (string, error) {
	d.calls.Add(1)
	if d.err != nil {
		return "", d.err
	}
	return d.description, nil
}

func TestModDetail_Description(t *testing.T) {
	t.Run("the fetcher fills an empty Description, exactly once", func(t *testing.T) {
		svc, game, _ := newModDetailTestService(t)
		src := newDescribingMockSource("src")
		src.description = "<p>The <b>real</b> description.</p>"
		svc.RegisterSource(src)
		src.AddMod(game.ID, &domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"})

		detail, err := svc.ModDetail(context.Background(), game, "default", "src", "a")
		require.NoError(t, err)
		assert.Equal(t, "<p>The <b>real</b> description.</p>", detail.Mod.Description,
			"raw source markup survives to the wire, as Mod.Description always has (#86)")
		assert.Equal(t, int64(1), src.calls.Load(), "one detail view is one description fetch")
		assert.Empty(t, detail.Notes)
	})

	t.Run("a source that already has a description is not asked again", func(t *testing.T) {
		svc, game, _ := newModDetailTestService(t)
		src := newDescribingMockSource("src")
		src.description = "should never be used"
		svc.RegisterSource(src)
		src.AddMod(game.ID, &domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A",
			Version: "1.5", Description: "already complete"})

		detail, err := svc.ModDetail(context.Background(), game, "default", "src", "a")
		require.NoError(t, err)
		assert.Equal(t, "already complete", detail.Mod.Description)
		assert.Zero(t, src.calls.Load(), "the second round trip is only worth paying when there is nothing to show")
	})

	t.Run("a non-fetcher source is unchanged", func(t *testing.T) {
		svc, game, src := newModDetailTestService(t)
		src.AddMod(game.ID, &domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"})

		detail, err := svc.ModDetail(context.Background(), game, "default", "src", "a")
		require.NoError(t, err)
		assert.Empty(t, detail.Mod.Description)
		assert.Empty(t, detail.Notes)
	})

	t.Run("a failing fetch logs at Warn, so the degradation is diagnosable", func(t *testing.T) {
		// Track C review, finding 12: the spec calls for a Warn and NOT a
		// Note, and "no Note" was asserted while "a Warn" was not -
		// fillModDescription could have returned silently and stayed green.
		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
		svc, err := core.NewService(core.ServiceConfig{
			ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(), Logger: logger,
		})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, svc.Close()) })

		game := &domain.Game{ID: "testgame", Name: "Test Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
		src := newDescribingMockSource("src")
		src.err = errors.New("upstream timeout")
		svc.RegisterSource(src)
		src.AddMod(game.ID, &domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"})

		detail, err := svc.ModDetail(context.Background(), game, "default", "src", "a")
		require.NoError(t, err)
		assert.Empty(t, detail.Mod.Description)
		assert.Empty(t, detail.Notes, "the degradation is a log line, never a note on the wire")

		logged := buf.String()
		assert.Contains(t, logged, "level=WARN", "a description that silently went missing must be diagnosable")
		assert.Contains(t, logged, "fetching mod description failed")
		assert.Contains(t, logged, "upstream timeout", "the upstream reason is what makes the line useful")
		assert.Contains(t, logged, "mod_id=a")
	})

	t.Run("a failing fetch degrades to empty, never a failure", func(t *testing.T) {
		svc, game, _ := newModDetailTestService(t)
		src := newDescribingMockSource("src")
		src.err = errors.New("upstream timeout")
		svc.RegisterSource(src)
		src.AddMod(game.ID, &domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"})

		detail, err := svc.ModDetail(context.Background(), game, "default", "src", "a")
		require.NoError(t, err, "a description fetch failure must not fail ModDetail")
		require.NotNil(t, detail.Mod)
		assert.Empty(t, detail.Mod.Description)
		assert.Equal(t, "Mod A", detail.Mod.Name, "the rest of the detail is intact")
	})
}

// TestDescriptionFetcherIsDetailOnly is #246's own boundary: the two flows
// that see MANY mods at once must never reach the fetcher, because each row
// would cost its own round trip.
func TestDescriptionFetcherIsDetailOnly(t *testing.T) {
	setup := func(t *testing.T) (*core.Service, *domain.Game, *describingMockSource) {
		t.Helper()
		svc, game, _ := newModDetailTestService(t)
		src := newDescribingMockSource("src")
		src.description = "<p>full</p>"
		svc.RegisterSource(src)
		game.SourceIDs = map[string]string{"src": game.ID}
		require.NoError(t, svc.SaveGame(context.Background(), game))
		for _, id := range []string{"a", "b", "c"} {
			src.AddMod(game.ID, &domain.Mod{ID: id, SourceID: "src", GameID: game.ID, Name: "Mod " + id, Version: "1.0"})
		}
		return svc, game, src
	}

	t.Run("search never fetches descriptions", func(t *testing.T) {
		svc, game, src := setup(t)
		report, err := svc.Search(context.Background(), game, "default", "Mod", core.SearchOptions{})
		require.NoError(t, err)
		require.NotEmpty(t, report.Mods)
		assert.Zero(t, src.calls.Load(), "a search row must not cost a description round trip")
	})

	t.Run("an update check never fetches descriptions", func(t *testing.T) {
		svc, game, src := setup(t)
		require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
			Mod:          domain.Mod{ID: "a", SourceID: "src", Name: "Mod a", Version: "0.9", GameID: game.ID},
			ProfileName:  "default",
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
		}))
		installed, err := svc.GetInstalledMods(context.Background(), game.ID, "default")
		require.NoError(t, err)
		require.NotEmpty(t, installed)
		_, err = svc.NewUpdater().CheckUpdates(context.Background(), game, installed, nil)
		require.NoError(t, err)
		assert.Zero(t, src.calls.Load(), "an update check must not cost a description round trip per mod")
	})
}
