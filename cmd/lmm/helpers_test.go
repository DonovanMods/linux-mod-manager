package main

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
