package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// coldIndexSource is a source that answers Search from a local index it
// builds on the first query - Thunderstore's shape, without importing it.
type coldIndexSource struct {
	id       string
	present  bool
	packages int
}

func (s *coldIndexSource) ID() string      { return s.id }
func (s *coldIndexSource) Name() string    { return "Thunderstore" }
func (s *coldIndexSource) AuthURL() string { return "" }
func (s *coldIndexSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}

func (s *coldIndexSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	// The build happens inside Search, which is the whole reason the notice
	// has to be printed before it.
	s.present, s.packages = true, 50707
	return source.SearchResult{
		Mods:       []domain.Mod{{ID: "RugbugRedfern-Skinwalkers", SourceID: s.id, Name: "Skinwalkers"}},
		TotalCount: 1,
	}, nil
}

func (s *coldIndexSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, source.ErrNotSupported
}

func (s *coldIndexSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, source.ErrNotSupported
}

func (s *coldIndexSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, source.ErrNotSupported
}

func (s *coldIndexSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", source.ErrNotSupported
}

func (s *coldIndexSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

func (s *coldIndexSource) IndexStatus(_ context.Context, sourceGameID string) (source.IndexStatus, error) {
	return source.IndexStatus{
		GameID: sourceGameID, Present: s.present, Packages: s.packages,
	}, nil
}

func (s *coldIndexSource) RefreshIndex(_ context.Context, sourceGameID string, _ bool, _ source.IndexProgressFunc) (source.IndexStatus, error) {
	s.present, s.packages = true, 50707
	return source.IndexStatus{GameID: sourceGameID, Present: true, Packages: 50707}, nil
}

// newColdIndexService wires a real Service and game around src.
func newColdIndexService(t *testing.T, src source.ModSource) (*core.Service, *domain.Game) {
	t.Helper()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(src)

	game := &domain.Game{
		ID: "lethal", Name: "Lethal Company", ModPath: t.TempDir(),
		SourceIDs: map[string]string{src.ID(): "lethal-company"},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	return svc, game
}

// captureStderr runs fn with os.Stderr redirected and returns what it wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	os.Stderr = orig
	require.NoError(t, w.Close())
	var buf bytes.Buffer
	_, err = buf.ReadFrom(r)
	require.NoError(t, err)
	return buf.String()
}

// TestSearchAnnouncesAColdIndexBuildOnStderr is #360 §2.7: the first search
// against a local-index source waits several seconds while it downloads a
// community index, and a user staring at a blank terminal has nothing to
// tell them why. The notice names the source and the index being built -
// and goes to stderr, so `--json` still writes exactly one document.
func TestSearchAnnouncesAColdIndexBuildOnStderr(t *testing.T) {
	src := &coldIndexSource{id: "thunderstore"}
	svc, game := newColdIndexService(t, src)
	withSearchFlags(t, "", 10)

	stderr := captureStderr(t, func() {
		require.NoError(t, doSearch(t.Context(), svc, game, []string{"skinwalkers"}))
	})

	assert.Contains(t, stderr, "Building the Thunderstore index for lethal-company (one-time)...")
	assert.Contains(t, stderr, "Indexed 50707 packages in ")
}

// TestSearchSaysNothingAboutAWarmIndex keeps the notice from becoming
// noise on every query: an index that is already built explains nothing.
func TestSearchSaysNothingAboutAWarmIndex(t *testing.T) {
	src := &coldIndexSource{id: "thunderstore", present: true, packages: 50707}
	svc, game := newColdIndexService(t, src)
	withSearchFlags(t, "", 10)

	stderr := captureStderr(t, func() {
		require.NoError(t, doSearch(t.Context(), svc, game, []string{"skinwalkers"}))
	})
	assert.NotContains(t, stderr, "Building")
	assert.NotContains(t, stderr, "Indexed")
}

// TestSearchSaysNothingForASourceWithNoIndex: every other source keeps no
// local index, and SourceIndexStatus answering nil is what hides the notice
// entirely.
func TestSearchSaysNothingForASourceWithNoIndex(t *testing.T) {
	spy := &pageSizeSpySource{id: "spy-noindex"}
	svc, game := newPageSizeSpyService(t, spy)
	withSearchFlags(t, "", 10)

	stderr := captureStderr(t, func() {
		require.NoError(t, doSearch(t.Context(), svc, game, []string{"query"}))
	})
	assert.Empty(t, strings.TrimSpace(stderr))
}

// TestSearchJSONKeepsOneDocumentOnStdout is the invariant the stderr choice
// exists for: a caller piping `lmm search --json` into jq must not have a
// progress line land in the middle of its document.
func TestSearchJSONKeepsOneDocumentOnStdout(t *testing.T) {
	src := &coldIndexSource{id: "thunderstore"}
	svc, game := newColdIndexService(t, src)
	withSearchFlags(t, "", 10)

	origJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = origJSON })

	stdout, stderr, err := captureStdoutAndStderr(t, func() error {
		return doSearch(t.Context(), svc, game, []string{"skinwalkers"})
	})
	require.NoError(t, err)

	assert.Contains(t, stderr, "Building the Thunderstore index")
	assert.NotContains(t, stdout, "Building the Thunderstore index")
	assert.True(t, strings.HasPrefix(strings.TrimSpace(stdout), "{"), "stdout is one JSON document: %q", stdout)
}

// TestSearchSaysHowToFixAnEmptyCommunityMapping is T1 review #3 at the
// surface the user actually meets. `sources: {thunderstore: ""}` used to
// make lmm index whichever real community shared the lmm game's id; it is
// refused now, and the refusal has to carry the one command that fixes it,
// because the community slug is not derivable from anything lmm knows.
//
// The CLI adds nothing to this: core's error is the sentence, and both
// frontends print it verbatim.
func TestSearchSaysHowToFixAnEmptyCommunityMapping(t *testing.T) {
	src := &coldIndexSource{id: "thunderstore"}
	svc, game := newColdIndexService(t, src)

	unmapped := *game
	unmapped.SourceIDs = map[string]string{"thunderstore": ""}
	require.NoError(t, svc.SaveGame(t.Context(), &unmapped))
	withSearchFlags(t, "thunderstore", 10)

	err := doSearch(t.Context(), svc, &unmapped, []string{"skinwalkers"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lmm game edit lethal --source thunderstore=<identifier>")
	assert.False(t, src.present, "nothing may be indexed for a community the user never named")
}
