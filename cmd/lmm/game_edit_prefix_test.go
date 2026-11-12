package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setPrefixFlags sets --prefix and/or --prefix-user for one test; a nil
// value leaves that flag unpassed.
func setPrefixFlags(t *testing.T, prefix, user *string) {
	t.Helper()
	wasP, wasPSet, wasU, wasUSet := gameEditPrefix, gameEditPrefixSet, gameEditPrefixUser, gameEditPrefixUserSet
	t.Cleanup(func() {
		gameEditPrefix, gameEditPrefixSet, gameEditPrefixUser, gameEditPrefixUserSet = wasP, wasPSet, wasU, wasUSet
	})
	gameEditPrefix, gameEditPrefixSet, gameEditPrefixUser, gameEditPrefixUserSet = "", false, "", false
	if prefix != nil {
		gameEditPrefix, gameEditPrefixSet = *prefix, true
	}
	if user != nil {
		gameEditPrefixUser, gameEditPrefixUserSet = *user, true
	}
}

func strPtr(s string) *string { return &s }

// #605: --prefix sets the game's Wine/Proton prefix, --prefix-user the
// Windows user inside it; `""` clears either.
func TestDoGameEdit_Prefix(t *testing.T) {
	svc := setupGameEditTest(t)
	pfx := filepath.Join(t.TempDir(), "pfx")

	setPrefixFlags(t, &pfx, strPtr("dyoung"))
	out := captureStdout(t, func() error {
		return doGameEdit(context.Background(), svc, "skyrim-se", false)
	})
	assert.Contains(t, out, "prefix set to "+pfx)
	assert.Contains(t, out, "prefix user set to dyoung")
	game, err := svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Equal(t, pfx, game.Prefix)
	assert.Equal(t, "dyoung", game.PrefixUser)

	setPrefixFlags(t, strPtr(""), strPtr(""))
	out = captureStdout(t, func() error {
		return doGameEdit(context.Background(), svc, "skyrim-se", false)
	})
	assert.Contains(t, out, "prefix cleared")
	game, err = svc.GetGame("skyrim-se")
	require.NoError(t, err)
	assert.Empty(t, game.Prefix)
	assert.Empty(t, game.PrefixUser)
}

func TestDoGameEdit_PrefixMustBeAbsolute(t *testing.T) {
	svc := setupGameEditTest(t)
	setPrefixFlags(t, strPtr("relative/pfx"), nil)

	err := doGameEdit(context.Background(), svc, "skyrim-se", false)
	var spec *core.GameSpecError
	require.ErrorAs(t, err, &spec)
	assert.Equal(t, "prefix", spec.Field)
}

// The loader is a separate edit, so --prefix beside it is refused.
func TestDoGameEditLoader_RefusesPrefixToo(t *testing.T) {
	svc := setupGameEditTest(t)
	setPrefixFlags(t, strPtr("/pfx"), nil)

	err := doGameEditLoader(context.Background(), svc, "skyrim-se", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--prefix")
}

// `lmm game show` prints the resolved prefix and where it came from.
func TestDoGameShow_PrintsThePrefix(t *testing.T) {
	svc := setupGameEditTest(t)
	pfx := filepath.Join(t.TempDir(), "pfx")
	require.NoError(t, os.MkdirAll(filepath.Join(pfx, "drive_c", "users", "dyoung"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pfx, "system.reg"), nil, 0o644))
	setPrefixFlags(t, &pfx, nil)
	captureStdout(t, func() error { return doGameEdit(context.Background(), svc, "skyrim-se", false) })

	out := captureStdout(t, func() error { return doGameShow(context.Background(), svc, "skyrim-se") })
	assert.Contains(t, out, "Prefix:")
	assert.Contains(t, out, pfx)
	assert.Contains(t, out, "set in games.yaml")
	assert.Contains(t, out, "dyoung")
}

// A prefix that is set but unusable says why, under the path.
func TestDoGameShow_PrintsAnUnresolvedPrefixsProblem(t *testing.T) {
	svc := setupGameEditTest(t)
	missing := filepath.Join(t.TempDir(), "missing")
	setPrefixFlags(t, &missing, nil)
	captureStdout(t, func() error { return doGameEdit(context.Background(), svc, "skyrim-se", false) })

	out := captureStdout(t, func() error { return doGameShow(context.Background(), svc, "skyrim-se") })
	assert.Contains(t, out, "does not exist")
}
