package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	sentinel := errors.New("boom")

	err := withService(cmd, func(ctx context.Context, svc *core.Service) error {
		return sentinel
	})

	require.ErrorIs(t, err, sentinel)
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
	svc, err := initService()
	require.NoError(t, err)
	require.NoError(t, svc.AddGame(&domain.Game{
		ID:      "testgame",
		Name:    "Test Game",
		ModPath: t.TempDir(),
	}))
	require.NoError(t, svc.Close())

	cmd := &cobra.Command{}
	var seen *domain.Game

	err = withGameService(cmd, func(ctx context.Context, svc *core.Service, game *domain.Game) error {
		seen = game
		return nil
	})

	require.NoError(t, err)
	require.NotNil(t, seen)
	assert.Equal(t, "testgame", seen.ID)
}
