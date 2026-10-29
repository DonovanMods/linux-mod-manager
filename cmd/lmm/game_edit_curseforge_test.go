package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setCurseForgeTypeFlags sets --curseforge-version-type for one test.
func setCurseForgeTypeFlags(t *testing.T, values ...string) {
	t.Helper()
	was, wasSet := gameEditCurseForgeTypes, gameEditCurseForgeTypesSet
	t.Cleanup(func() { gameEditCurseForgeTypes, gameEditCurseForgeTypesSet = was, wasSet })
	gameEditCurseForgeTypes, gameEditCurseForgeTypesSet = values, true
}

// #544: --curseforge-version-type sets the CurseForge game version types a
// game accepts; repeated, it names several; `""` clears them.
func TestDoGameEdit_CurseForgeVersionTypes(t *testing.T) {
	svc := setupGameEditTest(t)

	setCurseForgeTypeFlags(t, "67408", "77522")
	out := captureStdout(t, func() error {
		return doGameEdit(context.Background(), svc, "skyrim-se", false)
	})
	assert.Contains(t, out, "67408, 77522")
	game, err := svc.GetGame("skyrim-se")
	require.NoError(t, err)
	require.NotNil(t, game.CurseForge)
	assert.Equal(t, []int{67408, 77522}, game.CurseForge.GameVersionTypes)

	setCurseForgeTypeFlags(t, "")
	out = captureStdout(t, func() error {
		return doGameEdit(context.Background(), svc, "skyrim-se", false)
	})
	assert.Contains(t, out, "cleared")
	game, err = svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Nil(t, game.CurseForge)
}

func TestDoGameEdit_CurseForgeVersionTypeMustBeANumber(t *testing.T) {
	svc := setupGameEditTest(t)
	setCurseForgeTypeFlags(t, "classic")

	err := doGameEdit(context.Background(), svc, "skyrim-se", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"classic"`)
}

func TestDoGameEdit_CurseForgeVersionTypeMustBePositive(t *testing.T) {
	svc := setupGameEditTest(t)
	setCurseForgeTypeFlags(t, "0")

	err := doGameEdit(context.Background(), svc, "skyrim-se", false)
	require.Error(t, err)
	assert.ErrorIs(t, err, domain.ErrInvalidCurseForgeVersionType)
}

// The loader is a separate edit, so the two flags together are refused.
func TestDoGameEditLoader_RefusesCurseForgeVersionTypesToo(t *testing.T) {
	svc := setupGameEditTest(t)
	setCurseForgeTypeFlags(t, "67408")

	err := doGameEditLoader(context.Background(), svc, "skyrim-se", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--curseforge-version-type")
}
