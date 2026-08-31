package serve

// The updates fixture: a source that reports a real update for each of
// three installed mods and can really download the new version, so Task 9's
// batch tests exercise the whole flow core actually runs - check, re-plan
// per mod (Ruling 5), download, replace - rather than a stub of it. Three
// mods with two selected is what proves "only what you ticked" (#74): the
// third is the control, and its untouched version and untouched deployed
// file are the assertion.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/require"
)

// The three updatable mods, their installed and upstream versions, and the
// game-directory path each one deploys. Every mod deploys "Mods/<id>.pak"
// whose content names the version it came from, so the deployed tree itself
// says which version is live.
var updatableModIDs = []string{"u1", "u2", "u3"}

const (
	updateFromVersion = "1.0"
	updateToVersion   = "2.0"
)

// updatableFileID is the file ID mod modID carries at version.
func updatableFileID(modID, version string) string { return modID + "-" + version }

// updatableModFile is the game-directory path modID deploys.
func updatableModFile(modID string) string { return "Mods/" + modID + ".pak" }

// updatableContent is what that path resolves to once modID's version is
// the deployed one - the tree-level oracle for "was this mod updated".
func updatableContent(modID, version string) string { return modID + " " + version }

// updatesSource is a source.ModSource whose catalog sits at
// updateToVersion while the fixture's rows sit at updateFromVersion, so
// CheckUpdates reports an update for every one of them. Its downloads are
// real, over its own httptest server, because ApplyUpdate genuinely fetches
// and re-deploys the new version.
type updatesSource struct {
	server *httptest.Server
}

// newUpdatesSource starts the download server.
func newUpdatesSource(t *testing.T) *updatesSource {
	t.Helper()

	s := &updatesSource{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modID, fileID, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		version := strings.TrimPrefix(fileID, modID+"-")
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipWith(updatableModFile(modID), updatableContent(modID, version)))
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (*updatesSource) ID() string      { return fixtureSourceID }
func (*updatesSource) Name() string    { return "Updates Fixture Source" }
func (*updatesSource) AuthURL() string { return "" }

func (*updatesSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}

func (*updatesSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}

// GetMod reports the UPSTREAM version, which is what ApplyUpdate fetches
// the new files against.
func (*updatesSource) GetMod(_ context.Context, _, modID string) (*domain.Mod, error) {
	if !knownUpdatableMod(modID) {
		return nil, domain.ErrModNotFound
	}
	return &domain.Mod{ID: modID, SourceID: fixtureSourceID, Name: strings.ToUpper(modID), Version: updateToVersion, GameID: "g1"}, nil
}

func (*updatesSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}

// GetModFiles lists both versions' files, which is the shape
// selectUpdateDeployFiles is written for: the installed row's stored ID is
// still listed under the version being moved away from, and the new
// version's file is the unselected replacement that takes its place.
func (*updatesSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if !knownUpdatableMod(mod.ID) {
		return nil, domain.ErrModNotFound
	}
	return []domain.DownloadableFile{
		{ID: updatableFileID(mod.ID, updateToVersion), Name: "Main " + updateToVersion, FileName: mod.ID + "-2.0.zip", Version: updateToVersion, Category: "MAIN", IsPrimary: true, Size: 128},
		{ID: updatableFileID(mod.ID, updateFromVersion), Name: "Main " + updateFromVersion, FileName: mod.ID + "-1.0.zip", Version: updateFromVersion, Category: "MAIN", IsPrimary: true, Size: 96},
	}, nil
}

func (s *updatesSource) GetDownloadURL(_ context.Context, mod *domain.Mod, fileID string) (string, error) {
	return s.server.URL + "/" + mod.ID + "/" + fileID, nil
}

// CheckUpdates reports one update per installed mod still on the old
// version - the answer #74's checkbox table is built from.
func (*updatesSource) CheckUpdates(_ context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	var updates []domain.Update
	for _, mod := range installed {
		if !knownUpdatableMod(mod.ID) || mod.Version != updateFromVersion {
			continue
		}
		updates = append(updates, domain.Update{InstalledMod: mod, NewVersion: updateToVersion})
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].InstalledMod.ID < updates[j].InstalledMod.ID })
	return updates, nil
}

// knownUpdatableMod reports whether modID is one of the fixture's three.
func knownUpdatableMod(modID string) bool {
	for _, id := range updatableModIDs {
		if id == modID {
			return true
		}
	}
	return false
}

var _ source.ModSource = (*updatesSource)(nil)

// newUpdatesFixtureServer seeds three mods installed at 1.0, cached,
// listed in the profile AND really deployed - so a batch update has a DB
// row, a profile ref and a game-directory file to move for each one, and
// the mod nobody ticked has all three to leave alone.
func newUpdatesFixtureServer(t *testing.T) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	sandboxEnv(t)

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		CacheDir:  t.TempDir(),
		Logger:    slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(newUpdatesSource(t))

	ctx := t.Context()
	game := &domain.Game{
		ID:          "g1",
		Name:        "Fixture Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
		SourceIDs:   map[string]string{fixtureSourceID: ""},
	}
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err = svc.NewProfileManager().Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(ctx, game.ID))

	for _, modID := range updatableModIDs {
		fileID := updatableFileID(modID, updateFromVersion)
		require.NoError(t, svc.GetGameCache(game).Store(game.ID, fixtureSourceID, modID, updateFromVersion,
			updatableModFile(modID), []byte(updatableContent(modID, updateFromVersion))))
		require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
			Mod: domain.Mod{
				ID: modID, SourceID: fixtureSourceID, Name: strings.ToUpper(modID),
				Version: updateFromVersion, GameID: game.ID,
			},
			ProfileName:  "default",
			UpdatePolicy: domain.UpdateNotify,
			Enabled:      true,
			FileIDs:      []string{fileID},
		}))
		require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default", domain.ModReference{
			SourceID: fixtureSourceID, ModID: modID, Version: updateFromVersion, FileIDs: []string{fileID},
		}))
	}

	s := New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr})
	deployFixtureProfile(t, s, game)
	for _, modID := range updatableModIDs {
		require.Equal(t, updatableContent(modID, updateFromVersion), deployedContent(t, game, updatableModFile(modID)),
			"every fixture mod starts deployed at its installed version")
	}
	return s, svc, game
}
