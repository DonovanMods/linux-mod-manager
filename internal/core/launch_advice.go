package core

import (
	"context"
	"path/filepath"

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
func (s *Service) launchAdvice(ctx context.Context, game *domain.Game, profile string) []string {
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
	return adapter.LaunchAdvice(a, game, deployed)
}
