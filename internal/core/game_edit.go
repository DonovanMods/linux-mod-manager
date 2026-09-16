// game_edit.go is the game-EDIT flow `lmm game edit` and `PUT
// /api/v1/games/{id}` share (#326): rewriting which sources a configured
// game maps, the one part of games.yaml neither frontend could reach
// before - a custom source created in the Setup editor sat at "In use: -"
// forever unless the user stopped the server and hand-edited the file
// (epic live review, C-4).
//
// It is a settings-class single-step write, not a Plan/Apply pair: there
// is nothing to preview beyond the map itself and nothing that can go
// stale between a preview and a commit, exactly the shape mod_settings.go
// documents for lock/policy/convert.
package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// UpdateGameSources replaces gameID's source map with sources and returns
// the game's own `lmm game list --json` row (GameListEntry), re-read after
// the write, so a caller never needs a follow-up request to see what it
// changed.
//
// REPLACE, not merge: the map handed over is the map the game ends up
// with. A frontend editing one entry sends the whole map back, which is
// what makes "remove this source" expressible at all - `lmm game edit`'s
// --source/--remove-source flags are a delta the CLI resolves against the
// current map before calling this.
//
// Gated (beginOp) for AddGame's reason: it is a games.yaml write that must
// not interleave with a deploy job running on `lmm serve`'s own goroutine.
// Every check happens INSIDE the gate - the game must still exist, and
// every source id must still be registered - so a source unregistered
// between the check and the write cannot be mapped anyway
// (UnregisterSourceIfUnused takes the same gate).
//
// Rejections are GameSpecError with Field "sources", the wire key PUT
// /api/v1/games/{id} takes, so an SPA marks the offending input rather
// than parsing an English sentence; Value names the offending source id.
// An unknown game is domain.ErrGameNotFound, the 404 every other
// game-scoped route already answers with.
//
// An EMPTY map is refused: a game that maps no source can neither search
// nor install, and the only way to reach that state is to remove the last
// entry - a footgun, not a use case. An empty IDENTIFIER is refused for
// any source that says it needs one, and accepted for the sources that say
// they do not (a directory source keyed by nothing else is exactly how the
// fixtures and several custom sources are configured); either way it is
// trimmed, like the id.
func (s *Service) UpdateGameSources(ctx context.Context, gameID string, sources map[string]string) (*GameListEntry, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	game, ok := s.game(gameID)
	if !ok {
		return nil, domain.ErrGameNotFound
	}

	cleaned, err := s.validatedSourceMap(sources)
	if err != nil {
		return nil, err
	}

	if err := s.refuseRemovingReferencedSources(ctx, game, cleaned); err != nil {
		return nil, err
	}

	// A COPY: s.game returns the pointer the in-memory set holds, which
	// concurrent readers (GetGame, ListGames, SourcesForGame) are walking
	// right now. Mutating its map in place would be a data race no lock
	// here could close; saveGame publishes the replacement atomically.
	updated := *game
	updated.SourceIDs = cleaned
	if err := s.saveGame(ctx, &updated); err != nil {
		return nil, err
	}

	defaultGame, err := s.DefaultGame(ctx)
	if err != nil {
		return nil, err
	}
	entry, err := s.newGameListEntry(ctx, &updated, defaultGame)
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

// SetGameAdapter rewrites gameID's `adapter:` key and returns the game's
// own `lmm game list --json` row, re-read after the write (#353).
//
// It is UpdateGameSources' sibling in every respect - a settings-class
// single-step write, gated for the same reason, with every check inside
// the gate - and it is the ONLY way a frontend changes the key, so the
// rules below cannot be bypassed by one of them.
//
// An EMPTY name clears the key, which hands the game back to the adapter
// Service.AdapterName derives for it - icarus for `deploy_mode: compile`,
// bepinex for a game-root game with BepInEx, generic-files otherwise - and
// the returned row's EffectiveAdapter names which. Any other name must be
// registered: an unregistered one is a GameSpecError on field "adapter", so
// an SPA marks the input rather than parsing a sentence, and the message
// names what IS registered. The composition rules AdapterFor enforces are
// refused the same way, for the same reason: a `deploy_mode: compile` game
// may only be given an adapter that can compile (design §2), and bepinex
// only a game whose mod_path is its install path (#413 re-review P-b).
func (s *Service) SetGameAdapter(ctx context.Context, gameID, name string) (*GameListEntry, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	game, ok := s.game(gameID)
	if !ok {
		return nil, domain.ErrGameNotFound
	}

	// A COPY, for the reason UpdateGameSources documents: s.game returns
	// the pointer concurrent readers are walking right now.
	updated := *game
	updated.Adapter = strings.TrimSpace(name)

	if _, err := s.AdapterFor(&updated); err != nil {
		return nil, &GameSpecError{
			Field: "adapter", Value: updated.Adapter,
			Reason: err.Error(), Err: err,
		}
	}

	if err := s.saveGame(ctx, &updated); err != nil {
		return nil, err
	}

	defaultGame, err := s.DefaultGame(ctx)
	if err != nil {
		return nil, err
	}
	entry, err := s.newGameListEntry(ctx, &updated, defaultGame)
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

// SetGameModPath rewrites gameID's `mod_path:` and returns the game's own
// `lmm game list --json` row, re-read after the write (#427, #456) - the
// repair for a mod_path that no longer exists, and the command form of the
// "set its mod_path" step the BepInEx remedies used to leave to a hand edit
// of games.yaml.
//
// UpdateGameSources' sibling: a settings-class single-step write, gated for
// the same reason, every check inside the gate. The value is resolved by
// the rules `lmm game add` and the games.yaml loader apply
// (resolveModPathValue), and refused as a GameSpecError on field
// "mod_path" when it is empty or names something that is not a directory.
// An absent directory is accepted, as AddGame accepts one: a deploy
// creates it.
//
// Two refusals protect what is already on disk:
//
//   - GameModPathInUseError while any profile of the game has files
//     deployed. lmm records a deployed file relative to the mod_path it was
//     deployed under, so a move under a live deployment strands every
//     file: still live, no longer recorded, and looked for in the wrong
//     place by the next purge. The purge comes first.
//   - A GameSpecError when the move would turn a game its adapter accepts
//     into one AdapterFor refuses (`adapter: bepinex` off the game root). A
//     game that is ALREADY refused may be moved, because that is how such a
//     game is repaired.
//
// Setting the value the game already has writes nothing.
func (s *Service) SetGameModPath(ctx context.Context, gameID, modPath string) (*GameListEntry, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	game, ok := s.game(gameID)
	if !ok {
		return nil, domain.ErrGameNotFound
	}

	raw := config.ExpandPath(strings.TrimSpace(modPath))
	if raw == "" {
		return nil, newGameSpecError("mod_path", "", "a mod path is required")
	}
	resolved, err := resolveModPathValue(game.InstallPath, raw)
	if err != nil {
		return nil, err
	}

	defaultGame, err := s.DefaultGame(ctx)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(resolved) == filepath.Clean(game.ModPath) {
		entry, err := s.newGameListEntry(ctx, game, defaultGame)
		if err != nil {
			return nil, err
		}
		return &entry, nil
	}

	counts, err := s.db.DeployedFileCounts(ctx, game.ID)
	if err != nil {
		return nil, err
	}
	deployed := 0
	for _, n := range counts {
		deployed += n
	}
	if deployed > 0 {
		return nil, &GameModPathInUseError{GameID: game.ID, ModPath: game.ModPath, DeployedFiles: deployed}
	}

	// A COPY, for the reason UpdateGameSources documents.
	updated := *game
	updated.ModPath = resolved
	if _, err := s.AdapterFor(game); err == nil {
		if _, err := s.AdapterFor(&updated); err != nil {
			return nil, &GameSpecError{Field: "mod_path", Value: resolved, Reason: err.Error(), Err: err}
		}
	}

	if err := s.saveGame(ctx, &updated); err != nil {
		return nil, err
	}
	entry, err := s.newGameListEntry(ctx, &updated, defaultGame)
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

// resolveModPathValue applies the loader's mod_path rule to an
// already-expanded, non-empty value on the WRITE side (#363): a relative
// value is relative to installPath, and what is written is the resolved
// absolute path. A path that exists and is not a directory is refused -
// every later deploy would fail on it with a confusing link error - while
// an absent one is fine, because a deploy creates it. Shared by GameSpec
// (`lmm game add`) and SetGameModPath, so the two cannot accept different
// values.
func resolveModPathValue(installPath, modPath string) (string, error) {
	resolved, err := config.ResolveModPath(installPath, modPath)
	if err != nil {
		return "", &GameSpecError{Field: "mod_path", Value: modPath, Reason: err.Error(), Err: err}
	}
	if info, err := os.Stat(resolved); err == nil && !info.IsDir() {
		return "", newGameSpecError("mod_path", resolved, "path exists and is not a directory")
	}
	return resolved, nil
}

// modPathReasonAbsent is ModPathMissingError.Reason for a mod_path that is
// not there at all - the one reason that is not always a problem.
const modPathReasonAbsent = "does not exist"

// ModPathProblem reports whether game's mod_path needs the user's attention
// - a *ModPathMissingError - or nil when it does not. It is what every game
// document's mod_path_error says (GameListEntry, GameSummary, GameStatus,
// GameDetectEntry - `lmm game list/show`, `lmm status`, `lmm game detect`,
// the web Games rows) and what `lmm verify`'s mod_path_missing row reports
// (#427).
//
// A mod_path that is not a directory, or cannot be read, is always a
// problem. An ABSENT one is the ordinary state of a game nobody has deployed
// to yet - `lmm game add` and detection both leave it to the first deploy,
// which creates it - so it is flagged only when that is not what is going
// on (#427 review F3):
//
//   - lmm recorded files deployed under it, for any profile: its own files
//     are gone (the owner's #427 case), or
//   - a deploy could not create it: the install path is gone too, or the
//     nearest part of the mod_path that exists is outside the install path.
//
// A game with no mod_path configured is a different complaint, made
// elsewhere, and answers nil.
func (s *Service) ModPathProblem(ctx context.Context, game *domain.Game) (*ModPathMissingError, error) {
	problem := modPathStatProblem(game)
	if problem == nil || problem.Reason != modPathReasonAbsent {
		return problem, nil
	}

	counts, err := s.db.DeployedFileCounts(ctx, game.ID)
	if err != nil {
		return nil, err
	}
	for _, n := range counts {
		problem.DeployedFiles += n
	}
	problem.InstallPathMissing, problem.OutsideInstallPath = modPathUncreatable(game)
	if problem.InstallPathMissing || problem.OutsideInstallPath {
		problem.InstallPath = game.InstallPath
	}
	if problem.DeployedFiles == 0 && problem.InstallPath == "" {
		return nil, nil
	}
	return problem, nil
}

// modPathStatProblem is the stat half of ModPathProblem, with no judgement
// about an absent directory: `lmm import`'s scan has nothing to scan either
// way, so it refuses on this alone. One stat.
func modPathStatProblem(game *domain.Game) *ModPathMissingError {
	if game == nil || game.ModPath == "" {
		return nil
	}
	modPath := config.ExpandPath(game.ModPath)
	info, err := os.Stat(modPath)
	var reason string
	switch {
	case errors.Is(err, fs.ErrNotExist):
		reason = modPathReasonAbsent
	case err != nil:
		reason = "cannot be read: " + err.Error()
	case !info.IsDir():
		reason = "is not a directory"
	default:
		return nil
	}
	e := &ModPathMissingError{GameID: game.ID, ModPath: game.ModPath, Reason: reason}
	// A BepInEx layout is relative to the game root, so for a game that has
	// BepInEx the right mod_path is not a guess.
	if hasBepInEx(game) && filepath.Clean(game.ModPath) != filepath.Clean(game.InstallPath) {
		e.SuggestedModPath = game.InstallPath
	}
	return e
}

// modPathUncreatable reports why a deploy could not create game's absent
// mod_path where the user expects it: the install path is gone
// (installMissing), or the nearest existing ancestor of the mod_path lies
// outside the install path (outside) - so the mod_path is not somewhere in
// the game at all. A game with no install path has nothing to compare
// against, and answers neither.
func modPathUncreatable(game *domain.Game) (installMissing, outside bool) {
	if game.InstallPath == "" {
		return false, false
	}
	install := config.ExpandPath(game.InstallPath)
	if _, err := os.Stat(install); errors.Is(err, fs.ErrNotExist) {
		return true, false
	}
	ancestor := filepath.Clean(config.ExpandPath(game.ModPath))
	for {
		if _, err := os.Stat(ancestor); err == nil {
			break
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
		ancestor = parent
	}
	return false, !pathWithin(resolvedPath(ancestor), resolvedPath(install))
}

// pathWithin reports whether path is root or lies beneath it; both are
// cleaned, absolute paths.
func pathWithin(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ModPathMissingError is ModPathProblem's answer: GameID's mod_path is not a
// directory lmm can deploy into as things stand, and Error names the
// command that repairs it.
type ModPathMissingError struct {
	GameID  string `json:"game_id"`
	ModPath string `json:"mod_path"`
	// Reason is what is wrong with it: "does not exist", "is not a
	// directory", or "cannot be read: <cause>".
	Reason string `json:"reason"`
	// DeployedFiles is how many deployed files lmm has recorded under the
	// absent mod_path, across every profile - the reason an absent
	// directory is a problem rather than a game nobody has deployed to.
	DeployedFiles int `json:"deployed_files,omitzero"`
	// InstallPath is the game's install path, present when it is part of
	// the problem: InstallPathMissing (it is gone as well) or
	// OutsideInstallPath (the absent mod_path is not inside it).
	InstallPath        string `json:"install_path,omitempty"`
	InstallPathMissing bool   `json:"install_path_missing,omitzero"`
	OutsideInstallPath bool   `json:"outside_install_path,omitzero"`
	// SuggestedModPath is the value lmm can recommend with confidence - the
	// install path, for a game that has BepInEx - and empty otherwise.
	SuggestedModPath string `json:"suggested_mod_path,omitempty"`
}

// Error implements error.
func (e *ModPathMissingError) Error() string {
	head := fmt.Sprintf("mod_path %s %s", e.ModPath, e.Reason)
	switch {
	case e.DeployedFiles > 0:
		head += fmt.Sprintf(", but lmm recorded %d deployed file(s) under it", e.DeployedFiles)
		if e.InstallPathMissing {
			head += fmt.Sprintf(", and the install path %s does not exist either", e.InstallPath)
		}
	case e.InstallPathMissing:
		head += ", and neither does the install path " + e.InstallPath
	case e.OutsideInstallPath:
		head += ", and is outside the install path " + e.InstallPath
	case e.Reason == modPathReasonAbsent:
		head += " yet (a deploy creates it)"
	}

	switch {
	case e.DeployedFiles > 0 && e.SuggestedModPath != "":
		return fmt.Sprintf("%s; a BepInEx game deploys into its root: purge them, then run `lmm game edit %s --mod-path %s`, which names the purge each profile needs",
			head, e.GameID, e.SuggestedModPath)
	case e.DeployedFiles > 0:
		return fmt.Sprintf("%s; if the game still loads mods from there, run `lmm deploy --game %s` to put the active profile's back, or, if it loads them from somewhere else, purge them and run `lmm game edit %s --mod-path <path>`, which names the purge each profile needs",
			head, e.GameID, e.GameID)
	case e.SuggestedModPath != "":
		return fmt.Sprintf("%s; a BepInEx game deploys into its root: run `lmm game edit %s --mod-path %s`",
			head, e.GameID, e.SuggestedModPath)
	default:
		return fmt.Sprintf("%s; if the game loads mods from somewhere else, run `lmm game edit %s --mod-path <path>`", head, e.GameID)
	}
}

// Details returns the error itself for the --json error envelope's
// "details" field (Ruling 3).
func (e *ModPathMissingError) Details() any { return e }

// GameModPathInUseError refuses SetGameModPath while GameID has files
// deployed under ModPath - see SetGameModPath for why the purge comes
// first.
type GameModPathInUseError struct {
	GameID        string `json:"game_id"`
	ModPath       string `json:"mod_path"`
	DeployedFiles int    `json:"deployed_files"`
}

// Error implements error.
func (e *GameModPathInUseError) Error() string {
	return fmt.Sprintf("%d file(s) are deployed under %s, and lmm records each one relative to the mod_path; run `lmm purge --game %s` first, then change the mod_path, then run `lmm deploy --game %s`",
		e.DeployedFiles, e.ModPath, e.GameID, e.GameID)
}

// Details returns the error itself for the --json error envelope's
// "details" field (Ruling 3).
func (e *GameModPathInUseError) Details() any { return e }

// validatedSourceMap trims and checks every entry of a proposed source
// map, returning the map to persist. Each id must be non-empty and must
// name a source registered with this Service - the same check AddGame
// runs inside its own gate, for the same reason: a game mapping a source
// that does not exist is unusable, and nothing later would say why.
func (s *Service) validatedSourceMap(sources map[string]string) (map[string]string, error) {
	if len(sources) == 0 {
		return nil, newGameSpecError("sources", "", "a game must map at least one source")
	}

	cleaned := make(map[string]string, len(sources))
	// Sorted so a map with two bad ids always reports the same one - an
	// error message that changes between identical requests is untestable
	// and unexplainable.
	for _, id := range slices.Sorted(maps.Keys(sources)) {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			return nil, newGameSpecError("sources", id, "a source id is required")
		}
		if _, err := s.GetSource(trimmed); err != nil {
			return nil, &GameSpecError{
				Field: "sources", Value: trimmed,
				Reason: "no source is registered with that id", Err: err,
			}
		}
		// M6 (epic review M-6): two distinct keys that trim to the same id
		// - {" nexusmods":"a","nexusmods":"b"}, say - are legal JSON but not
		// a legal source map: silently keeping whichever sorts last hides
		// which value the caller actually gets, and does not tell them
		// their input had a collision at all.
		if _, dup := cleaned[trimmed]; dup {
			return nil, newGameSpecError("sources", trimmed, "duplicate source id after trimming whitespace")
		}
		identifier := strings.TrimSpace(sources[id])
		// T1 review #3: the same question AddGame asks, on the only other
		// way into games.yaml. A source that does not declare its mapped
		// value meaningless REQUIRES one, and writing an empty mapping for
		// it produces a game that fails at first use - or worse, for a
		// source that keeps a local index, one that quietly indexes a
		// community the user never named.
		if identifier == "" && !s.sourceIgnoresGameIdentifier(trimmed) {
			return nil, emptyIdentifierRefusal(trimmed)
		}
		cleaned[trimmed] = identifier
	}
	return cleaned, nil
}

// refuseRemovingReferencedSources refuses to drop a source id from game's
// map while any of game's profiles still has an installed mod that came
// from it (#326 fix wave, epic review M-4). Silently dropping the mapping
// left those mods degrading quietly: update checks skip them, a re-link to
// the source is refused ("source %q is not configured for %s",
// mod_edit.go), and archive imports warn the source isn't configured for
// the game. `lmm source remove` already refuses the mirror case
// (SourceInUseError, a GAME still mapping a source about to be
// unregistered entirely); this is the same rule one level down, for
// installed MODS a game's own edit would otherwise orphan.
//
// Only ids present in game.SourceIDs but absent from cleaned are checked -
// an id that stays mapped, or one newly added, references nothing to
// orphan. Sorted so a removal dropping two still-referenced sources always
// names the same one first, the same determinism validatedSourceMap's own
// field selection gives the caller.
func (s *Service) refuseRemovingReferencedSources(ctx context.Context, game *domain.Game, cleaned map[string]string) error {
	for _, sourceID := range slices.Sorted(maps.Keys(game.SourceIDs)) {
		if _, kept := cleaned[sourceID]; kept {
			continue
		}
		mods, err := s.modsReferencingSource(ctx, game.ID, sourceID)
		if err != nil {
			return err
		}
		if len(mods) > 0 {
			return &GameSourceInUseError{SourceID: sourceID, GameID: game.ID, Count: len(mods), Mods: mods}
		}
	}
	return nil
}

// modsReferencingSource returns every distinct "source:mod" key
// (domain.ModKey) across every profile of gameID whose installed row
// names sourceID, sorted - the population refuseRemovingReferencedSources
// refuses to orphan. A mod installed in more than one profile counts once.
func (s *Service) modsReferencingSource(ctx context.Context, gameID, sourceID string) ([]string, error) {
	profileNames, err := s.NewProfileManager().ListNames(ctx, gameID)
	if err != nil {
		return nil, fmt.Errorf("listing profiles: %w", err)
	}

	seen := map[string]bool{}
	for _, profileName := range profileNames {
		installed, err := s.GetInstalledMods(ctx, gameID, profileName)
		if err != nil {
			return nil, fmt.Errorf("getting installed mods: %w", err)
		}
		for _, im := range installed {
			if im.SourceID == sourceID {
				seen[domain.ModKey(im.SourceID, im.ID)] = true
			}
		}
	}

	keys := slices.Sorted(maps.Keys(seen))
	return keys, nil
}

// GameSourceInUseError refuses UpdateGameSources' removal of a source id
// because at least one profile of GameID still has an installed mod that
// came from it. Mods names them (ModKey form, "source:mod-id"), so a
// frontend can say WHICH mods to uninstall rather than "some mod,
// somewhere".
type GameSourceInUseError struct {
	SourceID string   `json:"source_id"`
	GameID   string   `json:"game_id"`
	Count    int      `json:"count"`
	Mods     []string `json:"mods"`
}

// Error implements error.
func (e *GameSourceInUseError) Error() string {
	return fmt.Sprintf("%d installed mod(s) still come from %q; uninstall them first", e.Count, e.SourceID)
}

// Details returns the error itself for the --json error envelope's
// "details" field (Ruling 3), the same shape SourceInUseError uses.
func (e *GameSourceInUseError) Details() any { return e }
