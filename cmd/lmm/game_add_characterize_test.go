package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/nexusmods"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDoGameAdd_OverwritesExistingDefaultProfileMods characterizes the
// pre-lift 'lmm game add' behaviour (v2 Phase 2 Task 21): saveGameConfig
// always writes a fresh default profile via config.SaveProfile,
// unconditionally discarding any mods an existing profile of the same name
// already had. Mirrors 'lmm game detect's repair overwrite
// (TestDoGameDetect_RepairWipesExistingDefaultProfileMods) so both call
// sites are pinned before they re-point to the same core helper.
func TestDoGameAdd_OverwritesExistingDefaultProfileMods(t *testing.T) {
	svc := setupGameAddTest(t)
	svc.RegisterSource(nexusmods.New(nil, ""))

	require.NoError(t, config.SaveProfile(configDir, &domain.Profile{
		Name:      "default",
		GameID:    "skyrimspecialedition",
		IsDefault: true,
		Mods: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "42", Version: "1.0"},
		},
	}))

	input := strings.Join([]string{
		"1", // select nexusmods (only registered source)
		"Skyrim Special Edition",
		"skyrimspecialedition",
		"/opt/games/skyrim",
		"",
	}, "\n") + "\n"

	cmd, buf := newGameAddCmd()
	reader := bufio.NewReader(strings.NewReader(input))

	err := doGameAdd(context.Background(), cmd, reader, svc)
	require.NoError(t, err, "output so far:\n%s", buf.String())

	profile, err := config.LoadProfile(configDir, "skyrimspecialedition", "default")
	require.NoError(t, err)
	assert.Empty(t, profile.Mods, "'lmm game add' must wipe an existing default profile's mod list, matching 'lmm game detect's repair overwrite")
}

// TestDoGameAdd_ProfileWriteFailureNotDoubleWrapped pins the whole-branch
// review's Important #1 fix (2026-08-29): a profile write failure on 'lmm
// game add' must print exactly the pre-lift text - "creating default
// profile: " (the label this call site has always applied, see 'git show
// 9cfaf37:cmd/lmm/game_add.go') directly wrapping config.SaveProfile's own
// error, not an extra "saving default profile: " segment from
// ProfileManager.CreateOrResetDefault in between. Reproduced the same way
// the review did live on the twin binaries: a read-only <configDir>/games
// directory forces SaveProfile's MkdirAll to fail.
func TestDoGameAdd_ProfileWriteFailureNotDoubleWrapped(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permission-based test is meaningless as root")
	}

	svc := setupGameAddTest(t)
	svc.RegisterSource(nexusmods.New(nil, ""))

	gamesDir := filepath.Join(configDir, "games")
	require.NoError(t, os.MkdirAll(gamesDir, 0755))
	require.NoError(t, os.Chmod(gamesDir, 0555))
	t.Cleanup(func() { _ = os.Chmod(gamesDir, 0755) })

	input := strings.Join([]string{
		"1", // select nexusmods (only registered source)
		"Skyrim Special Edition",
		"skyrimspecialedition",
		"/opt/games/skyrim",
		"",
	}, "\n") + "\n"

	cmd, buf := newGameAddCmd()
	reader := bufio.NewReader(strings.NewReader(input))

	err := doGameAdd(context.Background(), cmd, reader, svc)
	require.Error(t, err, "output so far:\n%s", buf.String())
	want := fmt.Sprintf("creating default profile: creating profiles dir: mkdir %s: permission denied", filepath.Join(gamesDir, "skyrimspecialedition"))
	assert.EqualError(t, err, want)
}
