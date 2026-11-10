package domain_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Nothing set is no block at all, the way an empty curseforge block is.
func TestNewGameLaunch_NothingSetIsNil(t *testing.T) {
	launch, err := domain.NewGameLaunch("", "", nil)
	require.NoError(t, err)
	assert.Nil(t, launch)
}

func TestNewGameLaunch_KeepsWhatIsSet(t *testing.T) {
	launch, err := domain.NewGameLaunch("command", "489830", []string{"~/bin/run.sh", "--profile", "{profile}", "{install_path}/x"})
	require.NoError(t, err)
	require.NotNil(t, launch)
	assert.Equal(t, "command", launch.Via)
	assert.Equal(t, "489830", launch.SteamAppID)
	assert.Equal(t, []string{"~/bin/run.sh", "--profile", "{profile}", "{install_path}/x"}, launch.Command)
}

// Every refusal wraps ErrInvalidLaunch and names the key at fault, so the
// games.yaml loader's message points at the line to fix.
func TestNewGameLaunch_Refusals(t *testing.T) {
	cases := []struct {
		name    string
		via     string
		appID   string
		command []string
		field   string
	}{
		{"unknown placeholder", "", "", []string{"run", "{game}"}, "launch.command[1]"},
		{"unknown placeholder in argv0", "", "", []string{"{nope}/run"}, "launch.command[0]"},
		{"empty argv0", "", "", []string{"  ", "x"}, "launch.command[0]"},
		{"relative argv0 path", "", "", []string{"bin/run.sh"}, "launch.command[0]"},
		{"via command without a command", "command", "", nil, "launch.command"},
		{"non-numeric app id", "", "48a", nil, "launch.steam_app_id"},
		{"negative-looking app id", "", "-1", nil, "launch.steam_app_id"},
		{"malformed via", "Steam!", "", nil, "launch.via"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.NewGameLaunch(tc.via, tc.appID, tc.command)
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrInvalidLaunch)
			assert.Contains(t, err.Error(), tc.field)
		})
	}
}

// A program is absolute, ~/-relative or a bare name looked up on PATH; a
// relative path would depend on whichever directory lmm runs in.
func TestNewGameLaunch_ProgramForms(t *testing.T) {
	for _, prog := range []string{"/opt/run", "~/run.sh", "lutris", "{install_path}/run.sh"} {
		_, err := domain.NewGameLaunch("", "", []string{prog})
		assert.NoError(t, err, prog)
	}
}

// A brace that is not a placeholder - a JSON argument, say - is the
// command's own business.
func TestNewGameLaunch_BracesThatAreNotPlaceholdersPass(t *testing.T) {
	_, err := domain.NewGameLaunch("", "", []string{"run", `{"a": 1}`, "{}", "{Profile}"})
	require.NoError(t, err)
}

// via names a method this layer does not know - whether it EXISTS is core's
// question, as with adapter: (a future method must load).
func TestNewGameLaunch_ViaIsSyntaxOnly(t *testing.T) {
	launch, err := domain.NewGameLaunch("heroic", "", nil)
	require.NoError(t, err)
	assert.Equal(t, "heroic", launch.Via)
}

func TestExpandLaunchPlaceholders(t *testing.T) {
	got := domain.ExpandLaunchPlaceholders("{install_path}/bin --p={profile} {steam_app_id}{game_id} {mod_path} {other}", map[string]string{
		"install_path": "/g", "profile": "default", "steam_app_id": "1", "game_id": "g", "mod_path": "/g/m",
	})
	assert.Equal(t, "/g/bin --p=default 1g /g/m {other}", got)
}

func TestLaunchPlaceholdersUsed(t *testing.T) {
	assert.Equal(t, []string{"steam_app_id", "profile"}, domain.LaunchPlaceholdersUsed([]string{"x{steam_app_id}", "{profile}", "{steam_app_id}"}))
	assert.Empty(t, domain.LaunchPlaceholdersUsed([]string{"x"}))
}

// #545: a Battle.net game is started through its prefix's Battle.net
// client. The block names the prefix (required), the launch code
// (optional: none opens the client) and the runner.
func TestNewGameLaunchWithBattleNet(t *testing.T) {
	launch, err := domain.NewGameLaunchWithBattleNet("battlenet", "", nil, &domain.BattleNetLaunch{
		Prefix: "~/Games/battlenet", Product: "WoW", Runner: "umu", Proton: "GE-Proton",
	})
	require.NoError(t, err)
	require.NotNil(t, launch.BattleNet)
	assert.Equal(t, "battlenet", launch.Via)
	assert.Equal(t, domain.BattleNetLaunch{Prefix: "~/Games/battlenet", Product: "WoW", Runner: "umu", Proton: "GE-Proton"}, *launch.BattleNet)

	// The block alone is a launch, as command alone is.
	launch, err = domain.NewGameLaunchWithBattleNet("", "", nil, &domain.BattleNetLaunch{Prefix: "/games/bnet"})
	require.NoError(t, err)
	require.NotNil(t, launch)
	assert.Equal(t, "/games/bnet", launch.BattleNet.Prefix)

	// No block at all is NewGameLaunch exactly.
	launch, err = domain.NewGameLaunchWithBattleNet("", "", nil, nil)
	require.NoError(t, err)
	assert.Nil(t, launch)
}

func TestNewGameLaunchWithBattleNet_Refusals(t *testing.T) {
	cases := map[string]struct {
		via string
		bn  *domain.BattleNetLaunch
		key string
	}{
		"via battlenet without a block": {"battlenet", nil, "launch.battlenet.prefix"},
		"no prefix":                     {"battlenet", &domain.BattleNetLaunch{Product: "WoW"}, "launch.battlenet.prefix"},
		"relative prefix":               {"", &domain.BattleNetLaunch{Prefix: "Games/battlenet"}, "launch.battlenet.prefix"},
		"unknown runner":                {"", &domain.BattleNetLaunch{Prefix: "/p", Runner: "lutris"}, "launch.battlenet.runner"},
		"proton with wine":              {"", &domain.BattleNetLaunch{Prefix: "/p", Runner: "wine", Proton: "GE-Proton"}, "launch.battlenet.proton"},
		"launch code with a space":      {"", &domain.BattleNetLaunch{Prefix: "/p", Product: "WoW --x"}, "launch.battlenet.product"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := domain.NewGameLaunchWithBattleNet(tc.via, "", nil, tc.bn)
			require.ErrorIs(t, err, domain.ErrInvalidLaunch)
			assert.Contains(t, err.Error(), tc.key)
		})
	}
}
