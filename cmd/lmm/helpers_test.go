package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source/curseforge"
	"github.com/DonovanMods/linux-mod-manager/internal/source/nexusmods"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPromptForGameSource_RendersNameViaResolver pins the "Name (id)" format
// (matching auth's promptForSource/doAuthStatus) driven by an injected
// resolver func, per the design brief's "resolver func from callers that
// have one" seam.
func TestPromptForGameSource_RendersNameViaResolver(t *testing.T) {
	names := map[string]string{"acme": "Acme Source", "beta": "Beta Source"}
	resolve := func(id string) string { return names[id] }

	var gotID string
	var promptErr error
	out := captureStdout(t, func() error {
		withStdin(t, "2\n", func() {
			gotID, promptErr = promptForGameSource("My Game", []string{"acme", "beta"}, resolve)
		})
		return nil
	})

	require.NoError(t, promptErr)
	assert.Equal(t, "beta", gotID)
	assert.Contains(t, out, "[1] Acme Source (acme)")
	assert.Contains(t, out, "[2] Beta Source (beta)")
}

// TestPromptForGameSource_NilResolverFallsBackToBareID pins the "unregistered
// source: bare ID fallback" rule for a nil resolver (e.g. a caller with none
// available).
func TestPromptForGameSource_NilResolverFallsBackToBareID(t *testing.T) {
	var gotID string
	var promptErr error
	out := captureStdout(t, func() error {
		withStdin(t, "1\n", func() {
			gotID, promptErr = promptForGameSource("My Game", []string{"acme"}, nil)
		})
		return nil
	})

	require.NoError(t, promptErr)
	assert.Equal(t, "acme", gotID)
	assert.Contains(t, out, "[1] acme\n", "bare ID, no (id) suffix")
	assert.NotContains(t, out, "(acme)")
}

// TestPromptForGameSource_UnknownIDResolverFallsBackToBareID pins the same
// fallback when a non-nil resolver simply doesn't know the ID (e.g. a
// source that was registered when the game was configured but has since
// been removed).
func TestPromptForGameSource_UnknownIDResolverFallsBackToBareID(t *testing.T) {
	resolve := func(id string) string { return "" } // never resolves anything

	var gotID string
	out := captureStdout(t, func() error {
		withStdin(t, "1\n", func() {
			gotID, _ = promptForGameSource("My Game", []string{"ghost-src"}, resolve)
		})
		return nil
	})

	assert.Equal(t, "ghost-src", gotID)
	assert.Contains(t, out, "[1] ghost-src\n", "bare ID, no (id) suffix")
	assert.NotContains(t, out, "(ghost-src)")
}

// TestResolveSource_MultiSource_PromptRendersRegistryNames proves the
// end-to-end wiring: resolveSource takes the *core.Service directly (no
// package-level resolver seam) and its interactive multi-source prompt
// renders registry display names sourced from it.
func TestResolveSource_MultiSource_PromptRendersRegistryNames(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(nexusmods.New(nil, ""))
	svc.RegisterSource(curseforge.New(nil, ""))

	game := &domain.Game{
		ID:        "g1",
		Name:      "Test Game",
		SourceIDs: map[string]string{"nexusmods": "slug", "curseforge": "123"},
	}

	var gotID string
	var resolveErr error
	out := captureStdout(t, func() error {
		withStdin(t, "2\n", func() {
			gotID, resolveErr = resolveSource(svc, game, "", false)
		})
		return nil
	})

	require.NoError(t, resolveErr)
	// getConfiguredSources sorts alphabetically: curseforge, nexusmods ->
	// [1]=curseforge, [2]=nexusmods.
	assert.Equal(t, "nexusmods", gotID)
	assert.Contains(t, out, "[1] CurseForge (curseforge)")
	assert.Contains(t, out, "[2] Nexus Mods (nexusmods)")
}

// TestResolveSource_MultiSource_PromptFallsBackToBareID pins the
// "unregistered source: bare ID fallback" rule end-to-end: a real
// *core.Service with nothing registered under these IDs (e.g. a custom
// source's definition was removed after the game was configured) still
// renders sensibly instead of erroring or showing an empty name.
func TestResolveSource_MultiSource_PromptFallsBackToBareID(t *testing.T) {
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{
		ID:        "g2",
		Name:      "Test Game 2",
		SourceIDs: map[string]string{"unregistered-a": "x", "unregistered-b": "y"},
	}

	var gotID string
	var resolveErr error
	out := captureStdout(t, func() error {
		withStdin(t, "1\n", func() {
			gotID, resolveErr = resolveSource(svc, game, "", false)
		})
		return nil
	})

	require.NoError(t, resolveErr)
	assert.Equal(t, "unregistered-a", gotID)
	assert.Contains(t, out, "[1] unregistered-a\n", "bare ID, no (id) suffix")
	assert.Contains(t, out, "[2] unregistered-b\n", "bare ID, no (id) suffix")
	assert.NotContains(t, out, "(unregistered-a)")
}

// TestResolveSource_MultiSource_NilServiceFallsBackToBareID pins
// resolverFromService's defensive nil-svc branch: resolveSource never
// panics when called without a service (not a real production path - every
// call site routes through withService/withGameService - but a cheap
// guard), falling back to bare IDs.
func TestResolveSource_MultiSource_NilServiceFallsBackToBareID(t *testing.T) {
	game := &domain.Game{
		ID:        "g3",
		Name:      "Test Game 3",
		SourceIDs: map[string]string{"acme": "x", "beta": "y"},
	}

	var gotID string
	var resolveErr error
	out := captureStdout(t, func() error {
		withStdin(t, "1\n", func() {
			gotID, resolveErr = resolveSource(nil, game, "", false)
		})
		return nil
	})

	require.NoError(t, resolveErr)
	assert.Equal(t, "acme", gotID)
	assert.Contains(t, out, "[1] acme\n", "bare ID, no (id) suffix")
	assert.NotContains(t, out, "(acme)")
	assert.Contains(t, out, "[2] beta\n", "Name==ID renders bare, not beta (beta)")
}

func TestReadPromptLineFrom_TrimsAndLowercases(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain y", "y\n", "y"},
		{"yes", "yes\n", "yes"},
		{"uppercase trimmed", "  Y  \n", "y"},
		{"mixed case yes", "Yes\n", "yes"},
		{"empty (just newline)", "\n", ""},
		{"EOF without trailing newline", "yes", "yes"},
		{"EOF empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readPromptLineFrom(strings.NewReader(tc.in))
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestReadPromptLineFrom_NonEOFErrorPropagates(t *testing.T) {
	// iotest.ErrReader returns the supplied error on every Read; ReadString
	// will surface it (as something other than io.EOF) and the helper must
	// wrap it rather than swallow it.
	boom := errors.New("disk on fire")
	_, err := readPromptLineFrom(iotest.ErrReader(boom))
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
}

func TestAuthPromptError_FormatsMessage(t *testing.T) {
	err := authPromptError("nexusmods")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authentication required")
	assert.Contains(t, err.Error(), "lmm auth login nexusmods")
}

func TestWithService_RunsFnAndCloses(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()

	type ctxKey struct{}
	parent := context.WithValue(context.Background(), ctxKey{}, "marker")
	cmd := &cobra.Command{}
	cmd.SetContext(parent)
	called := false

	err := withService(cmd, func(ctx context.Context, svc *core.Service) error {
		called = true
		assert.Equal(t, "marker", ctx.Value(ctxKey{}), "ctx from cmd.Context() should be forwarded")
		require.NotNil(t, svc)
		return nil
	})

	require.NoError(t, err)
	assert.True(t, called, "fn should have been invoked")
}

func TestWithService_PropagatesFnError(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	sentinel := errors.New("boom")

	err := withService(cmd, func(ctx context.Context, svc *core.Service) error {
		return sentinel
	})

	require.ErrorIs(t, err, sentinel)
}

func TestWithGameService_UnknownGameWrapsErrGameNotFound(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = "no-such-game"

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	err := withGameService(cmd, func(ctx context.Context, svc *core.Service, game *domain.Game) error {
		t.Fatal("fn should not run when game is missing")
		return nil
	})
	require.Error(t, err)
	require.ErrorIs(t, err, domain.ErrGameNotFound, "must preserve sentinel for errors.Is")
	assert.Contains(t, err.Error(), "no-such-game")
}

func TestWithGameService_RequiresGame(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = ""

	cmd := &cobra.Command{}
	called := false

	err := withGameService(cmd, func(ctx context.Context, svc *core.Service, game *domain.Game) error {
		called = true
		return nil
	})

	require.Error(t, err)
	assert.False(t, called, "fn should not be invoked when no game is set")
	assert.Contains(t, err.Error(), "no game specified")
}

func TestWithGameService_ResolvesGame(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameID = "testgame"

	// Seed the game via a one-off service so withGameService can resolve it.
	svc, err := initService(t.Context())
	require.NoError(t, err)
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID:      "testgame",
		Name:    "Test Game",
		ModPath: t.TempDir(),
	}))
	require.NoError(t, svc.Close())

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var seen *domain.Game

	err = withGameService(cmd, func(ctx context.Context, svc *core.Service, game *domain.Game) error {
		seen = game
		return nil
	})

	require.NoError(t, err)
	require.NotNil(t, seen)
	assert.Equal(t, "testgame", seen.ID)
}
