package serve

// The profiles fixture: one game with three profiles, arranged so a switch
// and an apply each have real work to do - a mod to undeploy, a mod to
// download and deploy, a mod to disable, and one the source cannot resolve
// at all.
//
// It also reproduces #294's warning case honestly rather than by
// construction. Its files carry NO per-file version (the shape every custom
// source with no version mapping has), so an install records the MOD's
// version; profile "modded" holds a ref locked at an older one, and
// ProfileManager.UpsertMod refuses to record a version a lock disagrees
// with. ApplyProfileSwitch reports that refusal as a Warning - the
// diagnostic #294 made unconditional precisely because it leaves the DB row
// and the profile record disagreeing.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/require"
)

// The profiles fixture's three profiles and the mods each one is about.
const (
	// activeProfile is the game's default profile at the start of every
	// test: p1 and p3 installed, enabled and deployed.
	activeProfile = "default"
	// switchTargetProfile lists p1 and p2 - so a switch to it undeploys p3
	// and installs p2 - and holds p2's ref LOCKED at an older version, which
	// is what makes the install's profile write refuse (#294).
	switchTargetProfile = "modded"
	// applyTargetProfile lists p1 and an unresolvable mod, and has p4
	// installed and enabled without being listed - one install, one
	// plan-time failure and one removal.
	applyTargetProfile = "curated"
	// unresolvableModID is listed by applyTargetProfile and known to no
	// source, so PlanProfileApply records its resolution failure on the
	// plan rather than failing the whole plan.
	unresolvableModID = "ghost"
	// lockedRefVersion is the version switchTargetProfile's p2 ref is
	// locked at; the source offers p2 at lockedMismatchVersion instead.
	lockedRefVersion      = "1.0"
	lockedMismatchVersion = "2.0"
)

// profileModFile is the game-directory path modID deploys.
func profileModFile(modID string) string { return "Mods/" + modID + ".pak" }

// profilesSource serves four mods, each as a single file carrying NO
// version - the custom-source shape core's own comments call out, and the
// one that makes an install record the mod-level version.
type profilesSource struct {
	server   *httptest.Server
	versions map[string]string
}

// newProfilesSource starts the download server.
func newProfilesSource(t *testing.T) *profilesSource {
	t.Helper()

	s := &profilesSource{versions: map[string]string{
		"p1": "1.0",
		"p2": lockedMismatchVersion,
		"p3": "1.0",
		"p4": "1.0",
	}}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modID, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipWith(profileModFile(modID), modID+" payload"))
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (*profilesSource) ID() string      { return fixtureSourceID }
func (*profilesSource) Name() string    { return "Profiles Fixture Source" }
func (*profilesSource) AuthURL() string { return "" }

func (*profilesSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}

func (*profilesSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
}

func (s *profilesSource) GetMod(_ context.Context, _, modID string) (*domain.Mod, error) {
	version, ok := s.versions[modID]
	if !ok {
		return nil, domain.ErrModNotFound
	}
	return &domain.Mod{ID: modID, SourceID: fixtureSourceID, Name: strings.ToUpper(modID), Version: version, GameID: "g1"}, nil
}

func (*profilesSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}

// GetModFiles reports one file with no Version at all - so
// selectFilesForVersion falls through to the primary pick and the install
// records the MOD's version, whatever a locked ref says.
func (s *profilesSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if _, ok := s.versions[mod.ID]; !ok {
		return nil, domain.ErrModNotFound
	}
	return []domain.DownloadableFile{
		{ID: mod.ID + "-f1", Name: "Main", FileName: mod.ID + ".zip", Category: "MAIN", IsPrimary: true, Size: 64},
	}, nil
}

func (s *profilesSource) GetDownloadURL(_ context.Context, mod *domain.Mod, fileID string) (string, error) {
	return s.server.URL + "/" + mod.ID + "/" + fileID, nil
}

func (*profilesSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

var _ source.ModSource = (*profilesSource)(nil)

// newProfilesFixtureServer builds the three-profile world described at the
// top of this file, with the active profile's two mods really deployed.
func newProfilesFixtureServer(t *testing.T) (*Server, *core.Service, *domain.Game) {
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
	svc.RegisterSource(newProfilesSource(t))

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
	pm := svc.NewProfileManager()
	for _, name := range []string{activeProfile, switchTargetProfile, applyTargetProfile} {
		_, err = pm.Create(ctx, game.ID, name)
		require.NoError(t, err)
	}
	require.NoError(t, svc.SetDefaultGame(ctx, game.ID))
	require.NoError(t, pm.SetDefault(ctx, game.ID, activeProfile))

	// The active profile: p1 and p3, installed, enabled and listed.
	for _, modID := range []string{"p1", "p3"} {
		seedProfileMod(t, svc, game, activeProfile, modID, "1.0", true)
	}
	// p4 is installed and enabled under the apply target but NOT listed
	// there, so a profile apply must remove it.
	seedProfileMod(t, svc, game, applyTargetProfile, "p4", "1.0", false)

	// The switch target lists p1 (already installed) and p2 (not installed
	// anywhere), whose ref is LOCKED at a version the source no longer
	// offers - #294's warning case.
	require.NoError(t, pm.AddMod(ctx, game.ID, switchTargetProfile, domain.ModReference{
		SourceID: fixtureSourceID, ModID: "p1", Version: "1.0",
	}))
	require.NoError(t, pm.AddMod(ctx, game.ID, switchTargetProfile, domain.ModReference{
		SourceID: fixtureSourceID, ModID: "p2", Version: lockedRefVersion, Locked: true,
	}))

	// The apply target lists p1 (to install) and a mod no source knows (a
	// plan-time resolution failure the plan carries as data).
	require.NoError(t, pm.AddMod(ctx, game.ID, applyTargetProfile, domain.ModReference{
		SourceID: fixtureSourceID, ModID: "p1", Version: "1.0",
	}))
	require.NoError(t, pm.AddMod(ctx, game.ID, applyTargetProfile, domain.ModReference{
		SourceID: fixtureSourceID, ModID: unresolvableModID, Version: "1.0",
	}))

	s := New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr})
	deployFixtureProfile(t, s, game)
	for _, modID := range []string{"p1", "p3"} {
		require.FileExists(t, deployedPath(game, profileModFile(modID)),
			"the active profile's mods start deployed")
	}
	return s, svc, game
}

// seedProfileMod installs modID under profileName with its file cached,
// optionally adding the profile's own load-order entry.
func seedProfileMod(t *testing.T, svc *core.Service, game *domain.Game, profileName, modID, version string, listed bool) {
	t.Helper()
	ctx := t.Context()
	fileID := modID + "-f1"

	require.NoError(t, svc.GetGameCache(game).Store(game.ID, fixtureSourceID, modID, version,
		profileModFile(modID), []byte(modID+" payload")))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod: domain.Mod{
			ID: modID, SourceID: fixtureSourceID, Name: strings.ToUpper(modID),
			Version: version, GameID: game.ID,
		},
		ProfileName:  profileName,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{fileID},
	}))
	if listed {
		require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, profileName, domain.ModReference{
			SourceID: fixtureSourceID, ModID: modID, Version: version, FileIDs: []string{fileID},
		}))
	}
}
