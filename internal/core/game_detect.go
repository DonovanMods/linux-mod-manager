package core

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// GameFromDetected converts one domain.DetectedGame (an app.DetectGames
// result) into the domain.Game ApplyGameDetect saves. g.Sources, when the
// known-games entry supplied one (#177: games with a non-NexusMods or
// multi-source setup, e.g. Icarus), wins outright; otherwise this derives
// the single-entry {nexusmods: g.NexusID} map every detected game produced
// before Sources existed, so every pre-#177 known game generates
// byte-for-byte the same games.yaml block it always has. A known-games
// entry setting NEITHER is misconfigured - every legitimate entry sets at
// least one - and used to silently produce {"nexusmods": ""}, a garbage
// source mapping that would propagate into games.yaml unnoticed; that is
// now a fail-loud error naming the game instead (#203 release review).
// g.DeployMode goes through domain.ParseDeployMode, which already treats ""
// as DeployExtract (today's default); an unrecognized non-empty value in
// the known-games schema (steam-games.yaml, built-in or user override) is a
// load-time error rather than a silent fallback (#172). Moved verbatim from
// cmd/lmm's gameFromDetected (v2 Phase 2 Task 21).
func GameFromDetected(g domain.DetectedGame) (*domain.Game, error) {
	deployMode, ok := domain.ParseDeployMode(g.DeployMode)
	if !ok {
		return nil, fmt.Errorf("%w: steam-games.yaml: game %q: deploy_mode %q (valid: %s)",
			domain.ErrInvalidDeployMode, g.Slug, g.DeployMode, domain.ValidDeployModes)
	}
	sources := g.Sources
	if len(sources) == 0 {
		if g.NexusID == "" {
			return nil, fmt.Errorf("game %q: known-games entry has no sources and no nexus_id - set at least one", g.Slug)
		}
		sources = map[string]string{"nexusmods": g.NexusID}
	}
	return &domain.Game{
		ID:          g.Slug,
		Name:        g.Name,
		InstallPath: g.InstallPath,
		ModPath:     g.ModPath,
		SourceIDs:   sources,
		LinkMethod:  domain.LinkSymlink,
		DeployMode:  deployMode,
	}, nil
}

// GameDetectResult is ApplyGameDetect's outcome: which games were written
// to games.yaml and which got a (re)created default profile, in input
// order, so a frontend can report exactly what happened without
// re-deriving it from side effects.
type GameDetectResult struct {
	Saved    []string `json:"saved"`    // game IDs written to games.yaml, in input order
	Profiles []string `json:"profiles"` // "<game>/default" profiles (re)created
	Warnings []string `json:"warnings"`
}

// ApplyGameDetect converts each of games (a caller's detect-prompt
// selection, in order) via GameFromDetected and persists it to games.yaml
// and its (re)created "default" profile, stopping at the first failure -
// conversion or persistence - so a caller can report exactly how far it
// got. Converting one game right before it is saved, rather than
// converting the whole batch up front, matters: it's what makes a later
// game's conversion failure (e.g. an unrecognized deploy_mode) leave every
// earlier game's save and profile creation untouched, reproducing the
// pre-lift cmd loop's per-game interleaving byte-for-byte even though the
// actual "Added:" printing now happens in the caller after this single call
// returns - since nothing else wrote to stdout/stderr during the pre-lift
// loop, deferring those prints to after this call doesn't change their
// order (v2 Phase 2 Task 21 review Important #1, 2026-08-28). Re-running
// against an already-configured game - the CLI's repair path
// (gameDetectSelectionIndices lets an explicit numeric selection name an
// already-configured game) - unconditionally overwrites both the
// games.yaml entry and the default profile's mod list; this mirrors 'lmm
// game add's own overwrite semantics exactly
// (ProfileManager.CreateOrResetDefault), preserved byte-for-byte from the
// pre-lift cmd code (v2 Phase 2 Task 21). One narrow text difference is
// deliberately not reproduced: beginOp takes the lock before the loop, so a
// context cancelled between the prompt read and this call now always
// surfaces as the bare "context canceled" rather than the pre-lift
// interleaved loop's "saving game <slug>: context canceled" (whole-branch
// review Minor #1, 2026-08-29).
func (s *Service) ApplyGameDetect(ctx context.Context, games []domain.DetectedGame) (*GameDetectResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &GameDetectResult{}, err
	}
	defer release()

	result := &GameDetectResult{}
	pm := s.NewProfileManager()
	for _, g := range games {
		game, err := GameFromDetected(g)
		if err != nil {
			return result, fmt.Errorf("converting detected game %s: %w", g.Slug, err)
		}

		if err := s.saveGame(ctx, game); err != nil {
			return result, fmt.Errorf("saving game %s: %w", game.ID, err)
		}
		result.Saved = append(result.Saved, game.ID)

		if _, err := pm.CreateOrResetDefaultAfterGameSave(ctx, game.ID); err != nil {
			return result, fmt.Errorf("creating default profile for %s: %w", game.ID, err)
		}
		result.Profiles = append(result.Profiles, game.ID+"/default")
	}
	return result, nil
}
