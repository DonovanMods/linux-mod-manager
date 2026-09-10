// game_add.go is the game-creation flow `lmm game add` and `POST
// /api/v1/games` share (#307): the catalog query, the spec validation, the
// slug derivation and the single gated write that used to live as seven
// interactive prompts inside cmd/lmm/game_add.go. Nothing here reads a
// terminal - a frontend collects the values however it likes (flags, a
// form, prompts) and hands over one GameSpec - which is what makes the CLI
// and `lmm serve` able to add a game the same way rather than two ways
// that drift.
package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// ErrGameExists is returned by AddGame when games.yaml already holds the
// derived game id. Detected INSIDE the gated write, before anything is
// persisted, so a frontend's 409 never depends on an untyped error's
// wording and never races a concurrent add (the rule ErrProfileExists
// follows for profiles, #332 M6).
var ErrGameExists = errors.New("game already exists")

// ErrNoGameCatalog is returned by SearchGameCatalog for a registered source
// that does not implement source.GameCatalog (NexusMods today, and every
// custom source). It is typed because both frontends branch on it: the CLI
// falls back to its manual-identifier prompt ("<source> has no searchable
// game catalog; enter this game's identifier ... directly"), and `lmm
// serve` answers 400 so the SPA can swap its search box for an identifier
// field.
var ErrNoGameCatalog = errors.New("source has no searchable game catalog")

// GameSpecError names the ONE field of a game-add request that was
// rejected, alongside the reason. The field name is a wire value, not
// prose: an SPA form marks the offending input from it, so it must never
// be recovered by substring-matching an English sentence. Err carries the
// domain sentinel behind the rejection where one exists (domain.
// ErrInvalidGameID for a bad id), so errors.Is still reaches it through
// the wrapper.
//
// Details returns the error itself, so the --json / /api/v1 envelope's
// "details" member is exactly {field, value, reason} (Ruling 3's shared
// `Details() any` extension point).
type GameSpecError struct {
	// Field is the GameSpec member at fault, named as the WIRE key POST
	// /api/v1/games takes: "source_id", "identifier", "name", "game_id",
	// "install_path", "mod_path" - plus "query" for SearchGameCatalog,
	// which shares this type rather than defining a second one-field error.
	Field string `json:"field"`
	// Value is the offending value, omitted when it is empty (the field was
	// simply missing) - never a secret: nothing here carries a credential.
	Value string `json:"value,omitempty"`
	// Reason is the human sentence a frontend shows beside the field.
	Reason string `json:"reason"`
	// Err is the underlying sentinel, if any; it is unwrapped, not encoded.
	Err error `json:"-"`
}

// Error renders "<field>: <reason>".
func (e *GameSpecError) Error() string { return e.Field + ": " + e.Reason }

// Unwrap exposes the underlying sentinel for errors.Is.
func (e *GameSpecError) Unwrap() error { return e.Err }

// Details returns the error itself as the envelope's "details" payload.
func (e *GameSpecError) Details() any { return e }

// newGameSpecError builds a field rejection with no underlying sentinel.
func newGameSpecError(field, value, reason string) *GameSpecError {
	return &GameSpecError{Field: field, Value: value, Reason: reason}
}

// GameCatalogMatch is one game a source's catalog offered for a query.
//
// Identifier is what goes into games.yaml's sources map for that source -
// the entry's own ID when it has one (CurseForge's numeric game id),
// falling back to its slug for a catalog that only populates that.
// GameID is the LOCAL games.yaml key core would derive for this entry
// (DeriveGameID of the slug, falling back to the identifier), computed
// here rather than in a frontend so the CLI's catalog path and the SPA's
// pick both key a CurseForge game "minecraft" and not "432". It is empty
// only for a catalog entry carrying neither a slug nor an identifier -
// a source bug, which AddGame refuses rather than writing a games.yaml
// entry keyed "".
type GameCatalogMatch struct {
	Identifier string `json:"identifier"`
	Name       string `json:"name"`
	Slug       string `json:"slug,omitempty"`
	GameID     string `json:"game_id,omitempty"`
}

// GameCatalogReport is `lmm game add --query`'s document: the matches a
// source's catalog offered, echoed back with the source and query that
// produced them so a stored or replayed response is self-describing.
// Matches is never nil - a query matching nothing is an empty list, not an
// error.
type GameCatalogReport struct {
	SourceID string             `json:"source_id"`
	Query    string             `json:"query"`
	Matches  []GameCatalogMatch `json:"matches"`
}

// DeriveGameID turns a source slug or identifier into a local games.yaml
// key: trimmed, lower-cased, spaces replaced by dashes. This is the
// derivation cmd/lmm's game-add prompts performed inline before #307;
// it is exported because the value is user-visible (it is the id every
// later `--game` flag takes), so a frontend showing a preview of it must
// compute it the same way core will.
func DeriveGameID(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), " ", "-"))
}

// SearchGameCatalog queries sourceID's game catalog and returns the entries
// whose name or slug contains query, case-insensitively - the filter the
// CLI ran inline before #307, now shared with `lmm serve`.
//
// The source's own ListGames error is returned wrapped but intact, so a
// caller can still branch on domain.ErrAuthRequired (the CLI rewrites it
// into its "run lmm auth login <id>" prompt).
func (s *Service) SearchGameCatalog(ctx context.Context, sourceID, query string) (*GameCatalogReport, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		// An empty query would list the source's entire catalog - tens of
		// thousands of rows for CurseForge - which is neither what the CLI
		// prompt nor the SPA's search box asks for.
		return nil, newGameSpecError("query", "", "a search query is required")
	}
	src, err := s.GetSource(sourceID)
	if err != nil {
		return nil, fmt.Errorf("looking up source %s: %w", sourceID, err)
	}
	catalog, ok := src.(source.GameCatalog)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNoGameCatalog, sourceID)
	}

	entries, err := catalog.ListGames(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching games from %s: %w", src.Name(), err)
	}

	needle := strings.ToLower(query)
	report := &GameCatalogReport{SourceID: sourceID, Query: query, Matches: []GameCatalogMatch{}}
	for _, e := range entries {
		if !strings.Contains(strings.ToLower(e.Name), needle) && !strings.Contains(strings.ToLower(e.Slug), needle) {
			continue
		}
		identifier := e.ID
		if identifier == "" {
			identifier = e.Slug
		}
		gameID := DeriveGameID(e.Slug)
		if gameID == "" {
			gameID = DeriveGameID(identifier)
		}
		report.Matches = append(report.Matches, GameCatalogMatch{
			Identifier: identifier, Name: e.Name, Slug: e.Slug, GameID: gameID,
		})
	}
	return report, nil
}

// ExactGameCatalogMatch is the ONE case a catalog search may resolve
// itself: exactly one match whose name equals the searched name, compared
// case-insensitively after trimming. It exists for #206's suggestion path
// - a source was named for a detected game with no identifier, so the game
// is looked up in that source's catalog BY ITS STEAM NAME - where silently
// taking "the only match" would be wrong (a one-match search for "Hades"
// can still return "Hades II") but making the user re-pick "Minecraft"
// out of a list containing "Minecraft" is pointless friction.
//
// nil means "the caller picks": no match, no exact match, or - since a
// catalog may legitimately carry two entries with the same name - more
// than one exact match, which is ambiguous rather than automatic.
func ExactGameCatalogMatch(report *GameCatalogReport, name string) *GameCatalogMatch {
	name = strings.TrimSpace(name)
	if report == nil || name == "" {
		return nil
	}
	var found *GameCatalogMatch
	for i, m := range report.Matches {
		if !strings.EqualFold(strings.TrimSpace(m.Name), name) {
			continue
		}
		if found != nil {
			return nil
		}
		found = &report.Matches[i]
	}
	return found
}

// GameSpec is everything AddGame needs to create a game. It is the
// seven-prompt form of cmd/lmm's old interactive flow reduced to data, so
// the CLI's flags, the CLI's prompts and the SPA's form all produce the
// same value.
//
//   - SourceID/Identifier are the games.yaml sources entry: the registered
//     source and this game's id WITH that source (a NexusMods domain slug,
//     a CurseForge numeric game id, a custom source's own key).
//   - ID is the LOCAL games.yaml key. Optional: empty derives it from
//     Identifier (DeriveGameID). A catalog-driven add passes the GameID
//     SearchGameCatalog suggested, which is derived from the entry's SLUG -
//     that is what keeps a CurseForge add keyed "minecraft" rather than
//     "432".
//   - ModPath is optional: empty defaults to <InstallPath>/mods, exactly
//     the default the CLI's prompt offered. A non-empty value may be
//     absolute OR relative to InstallPath: a relative one is resolved
//     against it, the same rule config.ResolveModPath applies when
//     games.yaml is read (#363), and the ABSOLUTE result is what gets
//     written - so the recorded value never depends on the working
//     directory lmm ran from. Both this and InstallPath are ~-expanded
//     first, so "~/mods" is an absolute path rather than a relative one.
//   - LinkMethod is optional in the strongest sense: domain.LinkSymlink IS
//     its zero value, so an unset field writes exactly the symlink method
//     the CLI has always written, with no defaulting step to drift.
//   - Sources is the FULL source map for a game that needs more than one
//     entry (#206's prefill from a curated known-games entry, e.g. Icarus'
//     {icarus: icarus}). Optional and additive: SourceID/Identifier is
//     layered on top of it, so an explicit source ADDS to a prefilled map
//     rather than replacing it, and a spec carrying only this map is
//     complete on its own.
//   - DeployMode is games.yaml's deploy_mode string, passed through to
//     domain.ParseDeployMode. Optional: "" means the default (extract),
//     exactly what every add wrote before this field existed. #206's
//     prefill carries a curated entry's value here so `game add
//     --from-detected` configures e.g. Icarus' compile mode the same way
//     `game detect` does.
type GameSpec struct {
	SourceID    string
	Identifier  string
	Name        string
	ID          string
	InstallPath string
	ModPath     string
	LinkMethod  domain.LinkMethod
	Sources     map[string]string
	DeployMode  string
}

// AddGame validates spec, then - under the Service's single mutation slot -
// writes the game to games.yaml and (re)creates its "default" profile,
// returning the same core.GameListEntry row `lmm game list --json` emits.
//
// Gated (beginOp) because it is two writes that must not interleave with
// another flow's: `lmm serve` runs Applies in background goroutines, so an
// un-gated add could publish a game between a deploy's two writes. The CLI
// used to take the gate for the games.yaml write only (Service.SaveGame)
// and create the default profile OUTSIDE it; moving both inside is
// strictly stronger and changes no CLI-visible behaviour (a CLI process
// runs one flow at a time), so the CLI now calls this instead of
// SaveGame + CreateOrResetDefaultAfterGameSave.
//
// PATH RULES (a deliberate tightening over the pre-#307 CLI, which
// validated nothing and would happily save a typo'd path that only failed
// at the first deploy): the install path must exist and be a directory;
// the mod path is NOT created - deploy creates it on demand, and a game is
// routinely configured before its mod directory exists - but it must not
// already exist as a non-directory. Both paths are ~-expanded, and a
// RELATIVE mod path is resolved against the install path exactly as
// config.ResolveModPath resolves one at load (#363), with the resolved
// absolute value being what gets written - so "Data" means the same thing
// in a hand-written games.yaml, at this prompt, in the web form and at
// POST /api/v1/games. A relative mod path with no install path to resolve
// against is still refused (review M6), reported against install_path,
// which is the missing value.
//
// A duplicate id is refused with ErrGameExists rather than overwritten.
// That is the one place AddGame does NOT reproduce the old CLI flow, which
// overwrote silently: `lmm game detect`'s repair path is the sanctioned
// way to overwrite an existing game (ApplyGameDetect, whose doc comment
// records those semantics), and an "add" that can destroy an existing
// game's default profile without saying so is a trap in a web form.
func (s *Service) AddGame(ctx context.Context, spec GameSpec) (*GameListEntry, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	return s.addGameLocked(ctx, spec)
}

// addGameLocked is AddGame without the gate, so a flow already holding the
// Service's mutation slot can compose it - ApplyDetectSelection adds an
// UNCURATED detect row through exactly this path, under the one slot that
// covers the whole selection (#368 review Minor 8). Every rule below is
// AddGame's; the only difference is who took the slot.
func (s *Service) addGameLocked(ctx context.Context, spec GameSpec) (*GameListEntry, error) {
	game, err := spec.game(s.sourceIgnoresGameIdentifier)
	if err != nil {
		return nil, err
	}

	// Checked INSIDE the gate, not before it: spec.game() validates every
	// field EXCEPT that SourceID actually resolves, so without this a
	// serve caller could park an unusable game in games.yaml with no error
	// to render (#333 Important #1). It has to share AddGame's own beginOp
	// rather than run as an early pre-check, or it would race
	// UnregisterSourceIfUnused's OWN gated re-check the same way the
	// pre-#333 CLI-only version raced nothing (#333 Minor #1): a check run
	// before the gate could pass, then lose the gate to an unregister that
	// removes the source, and still write a game mapping it. The CLI's own
	// registry pre-check (cmd/lmm/game_add.go) stays - it prints the nicer
	// "registered: ..." hint - but is now redundant rather than the only
	// thing enforcing it.
	//
	// Every id in the resulting map is checked, not only spec.SourceID: a
	// prefilled Sources map (#206) can name a source the running lmm does
	// not have registered - a curated entry for a game whose source is a
	// custom one the user never created - and that must fail the same way,
	// naming the "sources" field so a form marks the offending row.
	if sourceID := strings.TrimSpace(spec.SourceID); sourceID != "" {
		if _, err := s.GetSource(sourceID); err != nil {
			return nil, &GameSpecError{
				Field: "source_id", Value: spec.SourceID,
				Reason: "no source is registered with that id", Err: err,
			}
		}
	}
	for id := range game.SourceIDs {
		if id == strings.TrimSpace(spec.SourceID) {
			continue
		}
		if _, err := s.GetSource(id); err != nil {
			return nil, &GameSpecError{
				Field: "sources", Value: id,
				Reason: "no source is registered with that id", Err: err,
			}
		}
	}
	if _, exists := s.game(game.ID); exists {
		return nil, fmt.Errorf("%w: %s", ErrGameExists, game.ID)
	}
	if err := s.saveGame(ctx, game); err != nil {
		return nil, fmt.Errorf("saving game: %w", err)
	}
	if _, err := s.NewProfileManager().CreateOrResetDefaultAfterGameSave(ctx, game.ID); err != nil {
		return nil, fmt.Errorf("creating default profile: %w", err)
	}

	defaultGame, err := s.DefaultGame(ctx)
	if err != nil {
		return nil, err
	}
	entry := newGameListEntry(game, defaultGame)
	return &entry, nil
}

// sourceIgnoresGameIdentifier reports whether sourceID's registered source
// has no searchable game catalogue - the same test SearchGameCatalog makes
// before answering ErrNoGameCatalog, and the condition under which an empty
// identifier is a legitimate mapping rather than a missing value (#387).
//
// `lmm game edit --source localmods=` has always written one, and the
// README documents it ("directory sources ignore this value"); `game add`
// was the odd one out, demanding a value for a source that has nothing to
// look it up in. An unregistered id answers false and is refused by
// addGameLocked's own registration check a few lines later.
func (s *Service) sourceIgnoresGameIdentifier(sourceID string) bool {
	src, err := s.GetSource(sourceID)
	if err != nil {
		return false
	}
	_, hasCatalog := src.(source.GameCatalog)
	return !hasCatalog
}

// game validates the spec and builds the domain.Game AddGame persists.
// Every rejection is a GameSpecError naming the wire field at fault.
//
// identifierOptional answers "may this source's mapped value be empty" -
// see Service.sourceIgnoresGameIdentifier, the only implementation. A nil
// func means no: an identifier is required for every source, which is what
// a caller with no registry to ask should assume.
func (spec GameSpec) game(identifierOptional func(sourceID string) bool) (*domain.Game, error) {
	// The game's source map: a prefilled Sources map (a curated
	// known-games entry, #206) is the base, and an explicit
	// SourceID/Identifier is layered on top - so naming a source for an
	// already-curated game adds it rather than dropping what detection
	// knew. With neither, there is no mapping at all and the game would be
	// unusable, which is the same refusal a bare `game add` has always
	// made.
	sourceID := strings.TrimSpace(spec.SourceID)
	identifier := strings.TrimSpace(spec.Identifier)
	sources := maps.Clone(spec.Sources)
	if sources == nil {
		sources = map[string]string{}
	}
	switch {
	case sourceID != "":
		if identifier == "" && (identifierOptional == nil || !identifierOptional(sourceID)) {
			return nil, newGameSpecError("identifier", spec.Identifier, "the game's identifier with that source is required")
		}
		sources[sourceID] = identifier
	case len(sources) == 0:
		return nil, newGameSpecError("source_id", "", "a mod source is required")
	}

	name := strings.TrimSpace(spec.Name)
	if name == "" {
		return nil, newGameSpecError("name", spec.Name, "a display name is required")
	}

	deployMode, ok := domain.ParseDeployMode(strings.TrimSpace(spec.DeployMode))
	if !ok {
		return nil, &GameSpecError{
			Field: "deploy_mode", Value: spec.DeployMode,
			Reason: "unrecognised deploy mode (valid: " + domain.ValidDeployModes + ")",
			Err:    domain.ErrInvalidDeployMode,
		}
	}

	// An explicit id is taken as given (only slug-normalised); an absent one
	// is derived from the identifier, which is the manual path's rule.
	gameID := DeriveGameID(spec.ID)
	field := "game_id"
	if spec.ID == "" {
		gameID, field = DeriveGameID(identifier), "identifier"
		if identifier == "" && sourceID == "" {
			// A Sources-only spec has no identifier to derive from, so the
			// missing value is the id itself. With a SourceID named, the
			// identifier IS the value the caller left out - and stays the
			// field the SPA's form marks - even for a source that would
			// have accepted an empty one had a game id come with it (#387).
			field = "game_id"
		}
	}
	if gameID == "" {
		return nil, newGameSpecError(field, spec.ID+identifier, "no usable game id could be derived")
	}
	// The same rule config's profile paths enforce - a game id is a path
	// segment - checked HERE so it fails before games.yaml is written
	// rather than at the default profile's save, which is the first place
	// it used to surface.
	if strings.ContainsAny(gameID, `/\`) || strings.Contains(gameID, "..") {
		return nil, &GameSpecError{
			Field: field, Value: gameID,
			Reason: `a game id must not contain path separators or ".."`,
			Err:    domain.ErrInvalidGameID,
		}
	}

	// Both paths are expanded exactly as the LOADER expands them
	// (config.LoadGames runs ExpandPath over install_path and mod_path
	// before resolving one against the other), so "~/games/skyrim" typed
	// at a prompt - where no shell expanded it - means what it says
	// instead of failing as a directory that does not exist. Expansion
	// before resolution also keeps "~/mods" from being mistaken for a
	// relative value and joined onto the install path (#363).
	installPath := config.ExpandPath(strings.TrimSpace(spec.InstallPath))
	if installPath == "" {
		return nil, newGameSpecError("install_path", "", "the game's install path is required")
	}
	if err := requireDir(installPath); err != nil {
		return nil, &GameSpecError{Field: "install_path", Value: installPath, Reason: err.Error(), Err: err}
	}

	modPath := config.ExpandPath(strings.TrimSpace(spec.ModPath))
	if modPath == "" {
		modPath = filepath.Join(installPath, "mods")
	}
	// #363: the WRITE side applies the LOADER's rule. A relative mod_path
	// has always meant "relative to install_path" in a hand-written
	// games.yaml (config.ResolveModPath); refusing the same string here -
	// which is what #313 settled on, reasoning that "a frontend has no
	// reason to send one" - made one value mean two things, and the
	// reasoning was wrong: a user typing "Data" at the `game add` prompt,
	// in the web form or at POST /api/v1/games is exactly that frontend.
	//
	// The value lmm WRITES is the resolved absolute path, so every later
	// run reads the same directory regardless of the working directory it
	// was started from - the property #313 was protecting, kept.
	//
	// The one refusal that survives is review M6's: a relative value with
	// no install_path to resolve it against. In practice install_path is
	// required and already checked above, so that refusal is reported
	// against install_path (the actual fault) and this branch is
	// unreachable from AddGame - it stays because ResolveModPath owns the
	// rule and this must not silently diverge from it if that ever changes.
	resolved, err := config.ResolveModPath(installPath, modPath)
	if err != nil {
		return nil, &GameSpecError{Field: "mod_path", Value: modPath, Reason: err.Error(), Err: err}
	}
	modPath = resolved
	// Absent is fine (deploy creates it); present-but-not-a-directory is
	// not, and would otherwise fail every later deploy with a confusing
	// link error.
	if info, err := os.Stat(modPath); err == nil && !info.IsDir() {
		return nil, newGameSpecError("mod_path", modPath, "path exists and is not a directory")
	}

	return &domain.Game{
		ID:          gameID,
		Name:        name,
		InstallPath: installPath,
		ModPath:     modPath,
		SourceIDs:   sources,
		LinkMethod:  spec.LinkMethod,
		DeployMode:  deployMode,
	}, nil
}

// requireDir reports why path is not a usable directory, or nil.
func requireDir(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("path does not exist")
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("path exists and is not a directory")
	}
	return nil
}
