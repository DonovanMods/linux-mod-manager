package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
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

// GameSpecFromDetected prefills a GameSpec from one detected candidate,
// with overrides winning field by field - #206's "if the user already has
// the game installed, lmm should fill in most of the add-game fields
// automatically". It is PURE: no service, no scan, no I/O, so the CLI's
// `game add --from-detected` and POST /api/v1/games' from_steam_app_id
// prefill identically and a frontend never re-derives a slug, a mod path
// or a source map of its own.
//
// The rules, in the order a caller will care about them:
//
//   - Name, InstallPath, ID (from the candidate's slug) and DeployMode come
//     from the candidate unless the caller supplied them.
//   - ModPath comes from the candidate when detection knew one (a curated
//     entry's mod_path, already joined onto the install path). An UNKNOWN
//     candidate has none - detection deliberately refuses to guess - so
//     this defaults it to <install>/mods, exactly the default a bare `game
//     add` has always applied. A frontend showing that value should say it
//     is a guess; core cannot tell the user that, but AddGame will not
//     create the directory either way.
//   - Sources: the candidate's own map when it has one (#177's Icarus),
//     else {nexusmods: <nexus_id>} when it has that, else nothing - which
//     is the unknown case, where the caller's SourceID/Identifier is the
//     only mapping there is. An explicit SourceID is layered ON TOP of the
//     curated map by AddGame rather than replacing it, so naming a source
//     for an already-curated game ADDS it instead of silently dropping
//     what detection knew.
//   - LinkMethod is passed through untouched; its zero value already means
//     symlink.
func GameSpecFromDetected(d domain.DetectedGame, overrides GameSpec) GameSpec {
	spec := overrides
	if spec.Name == "" {
		spec.Name = d.Name
	}
	if spec.ID == "" {
		spec.ID = d.Slug
	}
	if spec.InstallPath == "" {
		spec.InstallPath = d.InstallPath
	}
	if spec.ModPath == "" {
		spec.ModPath = d.ModPath
	}
	if spec.ModPath == "" && spec.InstallPath != "" {
		spec.ModPath = filepath.Join(spec.InstallPath, "mods")
	}
	if spec.DeployMode == "" {
		spec.DeployMode = d.DeployMode
	}
	if len(spec.Sources) == 0 {
		switch {
		case len(d.Sources) > 0:
			spec.Sources = maps.Clone(d.Sources)
		case d.NexusID != "":
			spec.Sources = map[string]string{"nexusmods": d.NexusID}
		}
	}
	return spec
}

// FindDetectedGame resolves a Steam app id against a scan - the value
// `lmm game add --from-detected` takes and POST /api/v1/games carries as
// from_steam_app_id. The miss is a GameSpecError naming that wire field,
// so a web form marks the offending input and the CLI prints a sentence
// that names the app id it could not find.
//
// The reason is deliberately frontend-neutral (#206 review Minor 7): both
// callers already scan with IncludeUnknown:true before reaching this
// point, so a miss here means the app id genuinely is not installed, or
// was uninstalled between an earlier scan and this one - not merely that
// the caller scanned too narrowly. A frontend that has something more
// specific to offer (the SPA's stale-scan banner sits beside its own
// Rescan button) says so itself, rather than getting a baked-in CLI-shaped
// suggestion in every response.
func FindDetectedGame(games []domain.DetectedGame, appID string) (domain.DetectedGame, error) {
	appID = strings.TrimSpace(appID)
	for _, g := range games {
		if g.SteamAppID == appID {
			return g, nil
		}
	}
	return domain.DetectedGame{}, newGameSpecError("from_steam_app_id", appID,
		"no installed Steam game has that app id - it may have been uninstalled since the scan")
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
	// Index is the 1-based number a selection names, counted over the
	// KNOWN rows only. An unknown row (#206) carries 0: a detect selection
	// configures a game from its curated entry, and an unknown candidate
	// has none, so there is no number that could name it. 0 is therefore
	// "not selectable here - add it with `lmm game add --from-detected
	// <steam_app_id>`, or POST /api/v1/games with from_steam_app_id".
	//
	// Counting known rows only is what makes an index mean the same row
	// whether or not the caller asked for unknown rows: adding them to the
	// document must never renumber the ones a selection can name.
	Index             int  `json:"index"`
	AlreadyConfigured bool `json:"already_configured,omitzero"`
}

// GameDetectListingOptions tunes the listing document (#206).
type GameDetectListingOptions struct {
	// IncludeUnknown keeps the scan's unknown candidates (known:false) in
	// the document. Off by default, so a caller that scanned wide can
	// still render exactly today's known-only listing, and a caller that
	// never asked for unknown rows cannot accidentally publish them.
	IncludeUnknown bool
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
func (s *Service) GameDetectListing(ctx context.Context, games []domain.DetectedGame, warnings []string, opts GameDetectListingOptions) (*GameDetectListing, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	existing, err := s.LoadGamesFromDisk()
	if err != nil {
		return nil, fmt.Errorf("loading games: %w", err)
	}

	listing := &GameDetectListing{Games: make([]GameDetectEntry, 0, len(games)), Warnings: warnings}
	index := 0
	for _, g := range games {
		if !g.Known && !opts.IncludeUnknown {
			continue
		}
		// Only a known row gets a number - see GameDetectEntry.Index.
		if g.Known {
			index++
		}
		_, configured := existing[g.Slug]
		entry := GameDetectEntry{DetectedGame: g, AlreadyConfigured: configured}
		if g.Known {
			entry.Index = index
		}
		listing.Games = append(listing.Games, entry)
	}
	return listing, nil
}

// ErrUnknownDetectedGame is returned by SelectDetectedGames when a
// selection names a candidate that is installed but NOT in lmm's
// known-games list (#206). Such a row has no curated mod path and no
// sources, so ApplyGameDetect has nothing to write; the sanctioned path is
// the from-detected add flow, which collects exactly those two values.
// Typed because both frontends branch on it: the CLI points at `lmm game
// add --from-detected`, and `lmm serve` answers 400 pointing at POST
// /api/v1/games' from_steam_app_id.
var ErrUnknownDetectedGame = errors.New("detected game is not in the known-games list")

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
	if len(games) == 0 {
		// Without this the range hint below reads "use 1-0", which is not a
		// range and buries the real answer: the scan found nothing at all.
		return nil, errors.New("no games were detected, so there is nothing to select")
	}
	bySlug := make(map[string]int, len(games))
	// byIndex maps a 1-based selection number to its row. It counts the
	// KNOWN rows only, exactly as GameDetectEntry.Index does, so a caller
	// that listed with IncludeUnknown and one that did not name the same
	// game with the same number.
	byIndex := make([]int, 0, len(games))
	for i, g := range games {
		bySlug[strings.ToLower(g.Slug)] = i
		if g.Known {
			byIndex = append(byIndex, i)
		}
	}

	seen := make(map[int]bool, len(selectors))
	out := make([]domain.DetectedGame, 0, len(selectors))
	for _, sel := range selectors {
		sel = strings.TrimSpace(sel)
		var idx int
		if n, err := strconv.Atoi(sel); err == nil {
			if len(byIndex) == 0 {
				// "use 1-0" is not a range; the scan found installed games,
				// just none of them in the known-games list, so a numbered
				// selection has nothing to count. ErrUnknownDetectedGame
				// (not a bare string) so the caller - only `lmm serve`
				// reaches this today - can append its OWN next step
				// (api_games.go already does, for the per-row case below);
				// a CLI command baked into the message here would be wrong
				// wherever a browser is what actually shows it (#206 review
				// Minor 7).
				return nil, fmt.Errorf("%w: nothing in this selection is in the known-games list", ErrUnknownDetectedGame)
			}
			if n < 1 || n > len(byIndex) {
				return nil, fmt.Errorf("invalid selection %q: use 1-%d or a game slug", sel, len(byIndex))
			}
			idx = byIndex[n-1]
		} else if i, ok := bySlug[strings.ToLower(sel)]; ok {
			idx = i
		} else {
			return nil, fmt.Errorf("invalid selection %q: no detected game with that index or slug", sel)
		}
		if !games[idx].Known {
			return nil, fmt.Errorf("%w: %s (Steam app id %s) - it is installed, but nothing tells lmm where it keeps its mods, so it has to be added from the detected game with the source and mod path filled in",
				ErrUnknownDetectedGame, games[idx].Slug, games[idx].SteamAppID)
		}
		if seen[idx] {
			return nil, fmt.Errorf("duplicate selection %q", sel)
		}
		seen[idx] = true
		out = append(out, games[idx])
	}
	return out, nil
}
