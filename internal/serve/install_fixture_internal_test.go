package serve

// The install fixture: a source that really downloads, over a real HTTP
// server, so Task 8's install tests exercise the whole flow core actually
// runs - fetch, extract to cache, conflict check, deploy - rather than a
// stub of it. The download counter is what proves the conflict-overwrite
// re-run is download-free (docs/plans/2026-08-30-serve-impl.md Task 8).

import (
	"archive/zip"
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/require"
)

// The two catalog mods the install fixture offers, beyond the already-
// installed "m1": one with two versioned files (so #225's version select
// and file picker both have something to offer), and one single-file mod
// whose archive contains the very path "m1" already deployed (so its
// install hits the conflict gate).
const (
	installModID   = "m2"
	conflictModID  = "m3"
	installModFile = "Mods/boots.pak"
)

// installSourceMod is one catalog entry: the mod, its downloadable files,
// and the archive member each file's zip carries.
type installSourceMod struct {
	mod     domain.Mod
	files   []domain.DownloadableFile
	members map[string]string // file ID -> the single path inside its zip
}

// installSource is a source.ModSource with a working download: every file
// resolves to a URL on its own httptest server, which serves a real zip.
// urlRequests counts GetDownloadURL calls - the first thing
// downloadModToCache does for a file it is actually going to fetch - so a
// re-run that reads the cache warm leaves it untouched.
type installSource struct {
	server      *httptest.Server
	mods        map[string]*installSourceMod
	urlRequests atomic.Int64
}

// newInstallSource builds the catalog and starts its download server.
func newInstallSource(t *testing.T) *installSource {
	t.Helper()

	s := &installSource{mods: map[string]*installSourceMod{}}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modID, fileID, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		entry, ok := s.mods[modID]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		member, ok := entry.members[fileID]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipWith(member, "payload for "+modID+"/"+fileID))
	}))
	t.Cleanup(s.server.Close)

	// The already-installed mod, so ModDetail/PlanInstall can resolve it.
	s.mods["m1"] = &installSourceMod{
		mod:     domain.Mod{ID: "m1", SourceID: fixtureSourceID, Name: "Mod One", Version: "1.0", GameID: "g1"},
		files:   []domain.DownloadableFile{{ID: "m1f1", Name: "Main", FileName: "one.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 64}},
		members: map[string]string{"m1f1": deployFixtureFile},
	}
	// Two versioned files: #225's version select AND its file picker.
	s.mods[installModID] = &installSourceMod{
		mod: domain.Mod{ID: installModID, SourceID: fixtureSourceID, Name: "Better Boots", Version: "2.0", GameID: "g1"},
		files: []domain.DownloadableFile{
			{ID: "f2", Name: "Main 2.0", FileName: "boots-2.0.zip", Version: "2.0", Category: "MAIN", IsPrimary: true, Size: 128},
			{ID: "f1", Name: "Main 1.0", FileName: "boots-1.0.zip", Version: "1.0", Category: "MAIN", Size: 96},
		},
		members: map[string]string{"f1": installModFile, "f2": installModFile},
	}
	// One file, whose archive member is the path "m1" already deployed.
	s.mods[conflictModID] = &installSourceMod{
		mod:     domain.Mod{ID: conflictModID, SourceID: fixtureSourceID, Name: "Clashing Mod", Version: "1.0", GameID: "g1"},
		files:   []domain.DownloadableFile{{ID: "c1", Name: "Main", FileName: "clash.zip", Version: "1.0", Category: "MAIN", IsPrimary: true, Size: 32}},
		members: map[string]string{"c1": deployFixtureFile},
	}
	return s
}

// zipWith returns a zip archive holding exactly one member at the given
// path - the smallest real archive the extractor will accept.
func zipWith(member, content string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(path.Clean(member))
	if err != nil {
		panic(err)
	}
	if _, err := w.Write([]byte(content)); err != nil {
		panic(err)
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func (*installSource) ID() string      { return fixtureSourceID }
func (*installSource) Name() string    { return "Install Fixture Source" }
func (*installSource) AuthURL() string { return "" }

func (*installSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}

func (s *installSource) Search(_ context.Context, q source.SearchQuery) (source.SearchResult, error) {
	var mods []domain.Mod
	needle := strings.ToLower(q.Query)
	for _, entry := range s.mods {
		if needle == "" || strings.Contains(strings.ToLower(entry.mod.Name), needle) {
			mods = append(mods, entry.mod)
		}
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].ID < mods[j].ID })
	return source.SearchResult{Mods: mods, TotalCount: len(mods)}, nil
}

func (s *installSource) GetMod(_ context.Context, _, modID string) (*domain.Mod, error) {
	entry, ok := s.mods[modID]
	if !ok {
		return nil, domain.ErrModNotFound
	}
	mod := entry.mod
	return &mod, nil
}

func (*installSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}

func (s *installSource) GetModFiles(_ context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	entry, ok := s.mods[mod.ID]
	if !ok {
		return nil, domain.ErrModNotFound
	}
	return append([]domain.DownloadableFile(nil), entry.files...), nil
}

// GetDownloadURL is the counted call: downloadModToCache asks for a URL
// only when it has decided to actually fetch the file, so this counter is
// the cache-warm oracle the conflict-overwrite test asserts on.
func (s *installSource) GetDownloadURL(_ context.Context, mod *domain.Mod, fileID string) (string, error) {
	s.urlRequests.Add(1)
	return s.server.URL + "/" + mod.ID + "/" + fileID, nil
}

func (*installSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// downloadCount reports how many downloads this source has been asked for.
func (s *installSource) downloadCount() int { return int(s.urlRequests.Load()) }

var _ source.ModSource = (*installSource)(nil)

// newInstallFixtureServer is the mutation fixture with the downloading
// source in place of the download-free one: game "g1" (the default), the
// "default" profile, and "m1" installed with its file in the cache.
func newInstallFixtureServer(t *testing.T) (*Server, *core.Service, *domain.Game, *installSource) {
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

	src := newInstallSource(t)
	svc.RegisterSource(src)

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

	mod := domain.Mod{ID: "m1", SourceID: fixtureSourceID, Name: "Mod One", Version: "1.0", GameID: game.ID}
	require.NoError(t, svc.GetGameCache(game).Store(
		game.ID, mod.SourceID, mod.ID, mod.Version, deployFixtureFile, []byte("pak bytes")))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:          mod,
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))
	require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default",
		domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version}))

	return New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr}), svc, game, src
}
