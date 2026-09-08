package core

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
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

// GameDetectEntry is one row of a GameDetectListing: a detected game, the
// 1-based Index that names it in a selection (the same number the CLI's
// printed listing shows and `lmm game detect --select` takes), and whether
// games.yaml already holds it.
//
// AlreadyConfigured is the machine-readable form of the CLI listing's
// "[configured]" marker (#205 item 2). It is omitzero: a row that is not
// configured carries no key at all, matching how every other boolean on
// this wire behaves. Selecting a configured row anyway is legal - that is
// the documented repair path - so this is advisory, not a filter.
type GameDetectEntry struct {
	domain.DetectedGame
	Index             int  `json:"index"`
	AlreadyConfigured bool `json:"already_configured,omitzero"`
}

// GameDetectListing is the document a detect scan produces BEFORE anything
// is selected - what the CLI only ever printed to the terminal (under
// --json it emitted nothing until a selection was made, since Ruling 15
// allows only the result document on stdout). `lmm serve` needs the
// listing itself, because its selection happens in a browser between two
// requests, so it is a document here rather than console text.
//
// Games is never nil: a scan that found nothing is an empty list, not an
// error. Warnings are the scan's own (unreadable libraries, malformed
// known-games entries), carried in the document rather than written to
// stderr for the same reason GameDetectResult.Warnings are.
type GameDetectListing struct {
	Games    []GameDetectEntry `json:"games"`
	Warnings []string          `json:"warnings"`
}

// GameDetectListing builds the pre-selection listing for an
// already-detected set of games (app.DetectGames' output - core cannot
// scan Steam itself without importing a concrete source, Ruling 8), marking
// every row that games.yaml already holds.
//
// The configured check reads games.yaml FROM DISK (LoadGamesFromDisk)
// rather than the Service's in-memory set, matching what `lmm game detect`
// has always done: the listing must reflect the file a concurrent `game
// add` may have just written.
func (s *Service) GameDetectListing(ctx context.Context, games []domain.DetectedGame, warnings []string) (*GameDetectListing, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	existing, err := s.LoadGamesFromDisk()
	if err != nil {
		return nil, fmt.Errorf("loading games: %w", err)
	}

	listing := &GameDetectListing{Games: make([]GameDetectEntry, 0, len(games)), Warnings: warnings}
	for i, g := range games {
		_, configured := existing[g.Slug]
		listing.Games = append(listing.Games, GameDetectEntry{
			DetectedGame: g, Index: i + 1, AlreadyConfigured: configured,
		})
	}
	return listing, nil
}

// SelectDetectedGames resolves a caller's selection against a detect
// listing, in the order given, returning the detected games ApplyGameDetect
// should persist. Each selector is either a 1-based index into games (the
// number GameDetectEntry.Index carries and the CLI's `--select` takes) or a
// game's slug, matched case-insensitively - two spellings of the same
// choice, because the CLI's listing is numbered while an SPA holds the row
// itself and has no reason to count.
//
// An empty selection is refused rather than treated as "none": a caller
// that means "add nothing" does not call this at all, so an empty list here
// is a malformed request. A duplicate is refused too - ApplyGameDetect
// overwrites each game it is handed, and applying that twice to one game is
// never what a caller meant.
func SelectDetectedGames(games []domain.DetectedGame, selectors []string) ([]domain.DetectedGame, error) {
	if len(selectors) == 0 {
		return nil, errors.New("no games selected")
	}
	bySlug := make(map[string]int, len(games))
	for i, g := range games {
		bySlug[strings.ToLower(g.Slug)] = i
	}

	seen := make(map[int]bool, len(selectors))
	out := make([]domain.DetectedGame, 0, len(selectors))
	for _, sel := range selectors {
		sel = strings.TrimSpace(sel)
		var idx int
		if n, err := strconv.Atoi(sel); err == nil {
			if n < 1 || n > len(games) {
				return nil, fmt.Errorf("invalid selection %q: use 1-%d or a game slug", sel, len(games))
			}
			idx = n - 1
		} else if i, ok := bySlug[strings.ToLower(sel)]; ok {
			idx = i
		} else {
			return nil, fmt.Errorf("invalid selection %q: no detected game with that index or slug", sel)
		}
		if seen[idx] {
			return nil, fmt.Errorf("duplicate selection %q", sel)
		}
		seen[idx] = true
		out = append(out, games[idx])
	}
	return out, nil
}
