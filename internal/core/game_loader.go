// Package core: this file holds the game LOADER declaration's frontend seams
// (#359) - the unparsed spec both frontends hand over, and the settings-class
// edit that writes it.
//
// It sits beside game_edit.go and follows its shape exactly: a games.yaml
// edit with nothing to preview and nothing that can go stale between a
// preview and a commit is a single gated write, not a Plan/Apply pair
// (mod_settings.go documents that class).
package core

import (
	"context"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// LoaderSpec is a mod-loader declaration as a frontend collects it: four
// strings, unparsed.
//
// It exists rather than taking a domain.GameLoader directly for the reason
// GameSpec exists at all - so a rejection can name the WIRE FIELD at fault
// ("loader.runtime") with the value the caller sent. Handing over a parsed
// domain type would push the parse into each frontend, which is where two
// copies of the same rule start to drift, and would lose the offending
// string before core ever saw it.
type LoaderSpec struct {
	// Kind is the loader's name; only "bepinex" means anything to lmm today
	// (domain.LoaderKindBepInEx). Required: a declaration with no kind
	// declares nothing.
	Kind string `json:"kind"`
	// Version is the loader version installed in the game directory.
	// Optional: empty means "do not check the version" at verify time.
	Version string `json:"version,omitempty"`
	// Runtime is the game's Unity scripting backend
	// (domain.ValidLoaderRuntimes). Optional: empty is "not answered yet",
	// which `lmm game show` answers from disk where it can.
	Runtime string `json:"runtime,omitempty"`
	// Bootstrap is how the loader is injected on Linux
	// (domain.ValidLoaderBootstraps). Optional, on Runtime's terms.
	Bootstrap string `json:"bootstrap,omitempty"`
}

// loader validates the spec and returns the domain declaration to persist,
// or nil for a nil spec (a game that declares no loader).
//
// Every rejection is a GameSpecError naming the wire field, so the CLI, the
// web form and POST /api/v1/games all report the same thing about the same
// input.
func (spec *LoaderSpec) loader() (*domain.GameLoader, error) {
	if spec == nil {
		return nil, nil
	}
	kind := strings.ToLower(strings.TrimSpace(spec.Kind))
	if kind == "" {
		return nil, newGameSpecError("loader.kind", spec.Kind,
			"a loader kind is required (today lmm knows "+domain.LoaderKindBepInEx+")")
	}
	runtime, ok := domain.ParseLoaderRuntime(spec.Runtime)
	if !ok {
		return nil, &GameSpecError{
			Field: "loader.runtime", Value: spec.Runtime,
			Reason: "unrecognised loader runtime (valid: " + domain.ValidLoaderRuntimes + ")",
			Err:    domain.ErrInvalidLoaderRuntime,
		}
	}
	bootstrap, ok := domain.ParseLoaderBootstrap(spec.Bootstrap)
	if !ok {
		return nil, &GameSpecError{
			Field: "loader.bootstrap", Value: spec.Bootstrap,
			Reason: "unrecognised loader bootstrap (valid: " + domain.ValidLoaderBootstraps + ")",
			Err:    domain.ErrInvalidLoaderBootstrap,
		}
	}
	return &domain.GameLoader{
		Kind: kind, Version: strings.TrimSpace(spec.Version),
		Runtime: runtime, Bootstrap: bootstrap,
	}, nil
}

// UpdateGameLoader replaces gameID's mod-loader declaration with loader (nil
// CLEARS it) and returns the game's own `lmm game list --json` row
// (GameListEntry), re-read after the write - so a caller never needs a
// follow-up request to see what it changed.
//
// REPLACE, not merge, for UpdateGameSources' reason: the declaration handed
// over is the declaration the game ends up with, which is what makes
// "this game has no loader after all" expressible at all. `lmm game edit
// --loader ""` is the CLI spelling of the nil.
//
// Gated (beginOp) because it is a games.yaml write that must not interleave
// with a deploy job running on `lmm serve`'s own goroutine, and every check
// happens INSIDE the gate. An unknown game is domain.ErrGameNotFound, the
// 404 every other game-scoped route already answers with.
//
// It changes NO deployed state, deliberately. The declaration is a statement
// about the game installation, which lmm does not install (spike §4): what
// it changes is which rules apply - the archive-root normaliser's two
// ambiguous shapes (#358), the plan-time precondition, and the verify tier -
// from the next command onwards.
func (s *Service) UpdateGameLoader(ctx context.Context, gameID string, loader *LoaderSpec) (*GameListEntry, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	game, ok := s.game(gameID)
	if !ok {
		return nil, domain.ErrGameNotFound
	}

	declared, err := loader.loader()
	if err != nil {
		return nil, err
	}

	// A COPY: s.game returns the pointer the in-memory set holds, which
	// concurrent readers are walking right now (UpdateGameSources' own
	// reasoning). saveGame publishes the replacement atomically.
	updated := *game
	updated.Loader = declared
	if err := s.saveGame(ctx, &updated); err != nil {
		return nil, err
	}

	defaultGame, err := s.DefaultGame(ctx)
	if err != nil {
		return nil, err
	}
	entry := newGameListEntry(&updated, defaultGame)
	return &entry, nil
}
