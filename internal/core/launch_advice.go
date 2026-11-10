package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// launchAdvice is the game adapter's adapter.LaunchAdvisor advice for
// profile (#546): what the user must do outside lmm for the deployed mods
// to load - Diablo II: Resurrected's -mod argument, which only Battle.net's
// own settings can pass. It reads the profile's deployed mods in load
// order, with their deployable files. None for a game whose adapter gives
// no advice, which costs nothing more than resolving the adapter; and
// none, rather than an error, when anything cannot be read - advice is
// never what fails a deploy or a launch.
//
// The adapter is handed the arguments lmm last told the user to set for
// the game (launchArgsFile), so a deploy that drops the last mod can say
// to remove them. A deploy - record - stores what this advice says to set
// in their place; a launch only reads.
func (s *Service) launchAdvice(ctx context.Context, game *domain.Game, profile string, record bool) []string {
	a, err := s.AdapterFor(game)
	if err != nil {
		return nil
	}
	if _, ok := a.(adapter.LaunchAdvisor); !ok {
		return nil
	}
	mods, err := s.GetInstalledModsInProfileOrder(ctx, game.ID, profile)
	if err != nil {
		return nil
	}
	gameCache := s.GetGameCache(game)
	var deployed []adapter.DeployedMod
	for _, m := range mods {
		if m.External || !m.Enabled || !m.Deployed {
			continue
		}
		files, err := deployableFiles(gameCache, a, game, m.SourceID, m.ID, m.Version)
		if err != nil {
			continue
		}
		for i, f := range files {
			files[i] = filepath.ToSlash(f)
		}
		deployed = append(deployed, adapter.DeployedMod{Name: m.Name, Files: files})
	}
	advice := adapter.LaunchAdvice(a, game, deployed, s.launchArgs(game.ID))
	if record {
		s.recordLaunchArgs(game.ID, advice.Args)
	}
	return advice.Lines
}

// launchArgsDir holds, per game, the launch arguments lmm last told the
// user to set (#546): <DataDir>/launch-args/<game-id>, one line, absent
// for none.
const launchArgsDir = "launch-args"

// launchArgsFile is game's record, or "" for a game ID that cannot be a
// file name.
func (s *Service) launchArgsFile(gameID string) string {
	if gameID == "" || gameID == "." || gameID == ".." || strings.ContainsAny(gameID, `/\`) {
		return ""
	}
	return filepath.Join(s.dataDir, launchArgsDir, gameID)
}

// launchArgs is the arguments lmm last told the user to set for the game,
// "" when it told them none or the record cannot be read.
func (s *Service) launchArgs(gameID string) string {
	path := s.launchArgsFile(gameID)
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// recordLaunchArgs stores args as the game's record, removing it for "".
// Best effort, like the advice itself: a record that cannot be written
// costs at most one "remove" line later.
func (s *Service) recordLaunchArgs(gameID, args string) {
	path := s.launchArgsFile(gameID)
	if path == "" || s.launchArgs(gameID) == args {
		return
	}
	if args == "" {
		_ = os.Remove(path)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(args+"\n"), 0o644)
}
