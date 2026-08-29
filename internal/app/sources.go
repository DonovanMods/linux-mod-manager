package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/curseforge"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/internal/source/icarus"
	"github.com/DonovanMods/linux-mod-manager/internal/source/nexusmods"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
)

// icarusFirestoreProjectID is the Firestore project backing the Icarus source.
const icarusFirestoreProjectID = "projectdaedalus-fb09f"

// builtinSourceFactories constructs the first-party sources keyless; API keys
// are attached after construction by registerSource so built-ins and custom
// sources share one key pipeline.
var builtinSourceFactories = []func() source.ModSource{
	func() source.ModSource { return nexusmods.New(nil, "") },
	func() source.ModSource { return curseforge.New(nil, "") },
	func() source.ModSource { return icarus.New(nil, icarusFirestoreProjectID) },
}

// registerSources registers the built-in sources followed by every custom
// source definition under <cfgDir>/sources. Built-ins register first, so a
// custom definition reusing a built-in ID loses the collision (and warns).
func registerSources(ctx context.Context, svc *core.Service, cfgDir string, warn io.Writer) {
	for _, factory := range builtinSourceFactories {
		registerSource(ctx, svc, factory(), warn)
	}
	registerCustomSources(ctx, svc, cfgDir, warn)
}

// registerSource attaches src's API key (when it declares Auth) and registers
// it, unless the ID is already taken.
func registerSource(ctx context.Context, svc *core.Service, src source.ModSource, warn io.Writer) {
	id := src.ID()
	if _, err := svc.GetSource(id); err == nil {
		_, _ = fmt.Fprintf(warn, "warning: skipping source %q: id already in use\n", id) //nolint:errcheck // best-effort warning write
		return
	}
	// Gate on Capabilities().Auth: a key set on an auth-less source would be
	// stored but never attached to a request.
	if setter, ok := src.(interface{ SetAPIKey(string) }); ok && source.CapabilitiesOf(src).Auth {
		if key := ResolveAPIKey(ctx, svc, src); key != "" {
			setter.SetAPIKey(key)
		}
	}
	svc.RegisterSource(src)
}

// registerCustomSources loads <cfgDir>/sources/*.yaml and registers each
// definition, warning and skipping on load errors, construction failures, and
// ID collisions.
func registerCustomSources(ctx context.Context, svc *core.Service, cfgDir string, warn io.Writer) {
	defs, loadErrs, err := config.LoadSourceDefinitions(cfgDir)
	if err != nil {
		_, _ = fmt.Fprintf(warn, "warning: loading custom sources: %v\n", err) //nolint:errcheck // best-effort warning write
		return
	}
	for _, le := range loadErrs {
		_, _ = fmt.Fprintf(warn, "warning: skipping source definition %v\n", le) //nolint:errcheck // best-effort warning write
	}
	for _, def := range defs {
		src, err := custom.New(def)
		if err != nil {
			_, _ = fmt.Fprintf(warn, "warning: skipping source %q: %v\n", def.ID, err) //nolint:errcheck // best-effort warning write
			continue
		}
		registerSource(ctx, svc, src, warn)
	}
}

// ResolveAPIKey returns the API key for src: the environment variable named
// by EnvKeyFor(src) wins, then the token stored by `lmm auth login`; "" if
// neither is set.
func ResolveAPIKey(ctx context.Context, svc *core.Service, src source.ModSource) string {
	if key := os.Getenv(EnvKeyFor(src)); key != "" {
		return key
	}
	token, err := svc.GetSourceToken(ctx, src.ID())
	if err != nil || token == nil {
		return ""
	}
	return token.APIKey
}

// EnvKeyFor returns the environment variable that can supply src's API key:
// the source's own EnvKeyProvider name (built-ins keep their legacy names such
// as NEXUSMODS_API_KEY), else the derived LMM_<ID>_API_KEY.
func EnvKeyFor(src source.ModSource) string {
	if p, ok := src.(source.EnvKeyProvider); ok {
		return p.EnvKey()
	}
	return EnvKeyForSourceID(src.ID())
}

// EnvKeyForSourceID derives LMM_<ID>_API_KEY with the ID uppercased and dashes
// replaced by underscores.
func EnvKeyForSourceID(sourceID string) string {
	return "LMM_" + strings.ReplaceAll(strings.ToUpper(sourceID), "-", "_") + "_API_KEY"
}

// LoadSourceDefinitions loads every custom source definition under
// <configDir>/sources - the same definitions registerCustomSources
// registers at startup - for a frontend that needs to inspect them directly
// (e.g. 'lmm source list' cross-referencing what actually got registered).
func LoadSourceDefinitions(configDir string) ([]source.SourceDefinition, []config.SourceLoadError, error) {
	return config.LoadSourceDefinitions(configDir)
}

// LoadSourceDefinitionFile parses and validates a single source definition
// file, for 'lmm source validate'.
func LoadSourceDefinitionFile(path string) (source.SourceDefinition, error) {
	return config.LoadSourceDefinitionFile(path)
}

// ConstructSource builds def's ModSource without performing any live I/O
// (a directory scan, a manifest fetch, an API call) - the same construction
// step registerCustomSources performs at startup, exposed for a frontend
// that needs to recover a definition's construction error without
// registering it (e.g. 'lmm source list' reclassifying a definition that
// never made it into the registry).
func ConstructSource(def source.SourceDefinition) (source.ModSource, error) {
	return custom.New(def)
}

// ProbeSource constructs def's source, attaches its API key the same way
// registration does, and performs one live operation against it - a
// directory scan, a manifest fetch+parse, or an API call - returning a
// human-readable summary of what it found. probeID supplies the mod id for
// an api definition with no search endpoint. This is 'lmm source validate
// --probe's underlying smoke test.
func ProbeSource(ctx context.Context, svc *core.Service, def source.SourceDefinition, probeID string) (string, error) {
	src, err := custom.New(def)
	if err != nil {
		return "", fmt.Errorf("constructing source: %w", err)
	}
	if a, ok := src.(interface{ SetAPIKey(string) }); ok {
		// Same resolution as registration (env var named by EnvKeyFor, then
		// the stored token), so a probe sees exactly the key a real run would.
		if key := ResolveAPIKey(ctx, svc, src); key != "" {
			a.SetAPIKey(key)
		}
	}

	switch def.Type {
	case source.TypeDirectory, source.TypeManifest:
		res, err := src.Search(ctx, source.SearchQuery{PageSize: 1})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("ok — %d mod(s) visible", res.TotalCount), nil
	case source.TypeAPI:
		if def.API.Endpoints.Search != nil {
			res, err := src.Search(ctx, source.SearchQuery{PageSize: 1})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("ok — search responded (%d total reported)", res.TotalCount), nil
		}
		if probeID == "" {
			return "", errors.New("this definition has no search endpoint; provide a known mod id with --id to probe get_mod")
		}
		mod, err := src.GetMod(ctx, "", probeID)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("ok — get_mod %s returned %q", probeID, mod.Name), nil
	}
	return "", fmt.Errorf("unsupported source type %q", def.Type)
}

// SourceInfo is one row of `lmm source list`: a registered source, or a
// definition that never became one.
//
//   - Type is the source's own TypeLabel ("built-in", "directory",
//     "manifest", "api") or "error" for a definition that failed to load,
//     collided with a registered ID, or failed to construct.
//   - Auth is "yes" (authenticated), "no" (auth-capable but not
//     authenticated) or "n/a" (the source needs no auth at all).
//   - Capabilities is the compact summary a row shows, e.g. "search,updates".
//   - InUse marks one of the active game's configured sources - only ever
//     set in the full-registry-with-a-game view (SourceInfos' all=true),
//     which is the one case that marks a subset rather than restricting to
//     it. omitzero (not omitempty: under encoding/json/v2 only omitzero
//     drops a false bool), so a scoped or gameless response carries no
//     "in_use" key at all - the shape today's callers already depend on.
//   - Err/ErrorMessage are the failure behind an "error" row, paired the way
//     core.SourceWarning pairs them: the structured error for a caller that
//     wants to classify it, its message for the wire.
type SourceInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Auth         string `json:"auth"`
	Capabilities string `json:"capabilities"`
	InUse        bool   `json:"in_use,omitzero"`
	Err          error  `json:"-"`
	ErrorMessage string `json:"error,omitempty"`
}

// newSourceInfoError builds an "error" row with Err and ErrorMessage paired
// from a single error, so a construction site cannot emit one without the
// other (the same rule core.newSourceWarning follows).
func newSourceInfoError(id string, err error) SourceInfo {
	return SourceInfo{ID: id, Type: "error", Err: err, ErrorMessage: err.Error()}
}

// SourceInfos assembles the `lmm source list` rows.
//
// This lives in app, not core, because two of its three inputs do: the
// custom source DEFINITIONS on disk (LoadSourceDefinitions) and the ability
// to re-run a failed one's construction to recover its error
// (ConstructSource). Core's registry only holds sources that registered
// successfully - it cannot see a definition that collided or failed to
// build, which is exactly what the error rows report - and constructing one
// means naming concrete source packages, which core must not import
// (Ruling 12, #300). Everything else here (ListSources, SourcesForGame,
// GetSource) is core's.
//
// game nil means no game context is resolvable: the full registry is
// returned either way, and all has no effect. With a game, all=false scopes
// the list to that game's configured+registered sources, while all=true
// returns the full registry with those sources marked InUse.
//
// Definitions that failed stay visible in EVERY view: they never registered,
// so they have no game association to scope by, and hiding them would bury
// exactly the diagnostics a user debugging their YAML needs.
func SourceInfos(ctx context.Context, svc *core.Service, game *domain.Game, all bool) ([]SourceInfo, error) {
	// Reads definition files and re-runs failed constructions (which may
	// touch the filesystem); an already-cancelled ctx aborts before any of
	// it, matching Open's own contract.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	defs, loadErrs, err := LoadSourceDefinitions(svc.ConfigDir())
	if err != nil {
		return nil, fmt.Errorf("loading source definitions: %w", err)
	}

	// Reclassify each definition against what actually ended up registered
	// (registration may have skipped it on ID collision or construction
	// failure) so the list reflects reality rather than just "a definition
	// with this ID exists".
	var errRows []SourceInfo
	for _, d := range defs {
		registered, err := svc.GetSource(d.ID)
		switch {
		case err == nil && isCustomSource(registered):
			// Registered successfully as this definition's own custom
			// source; its row (built from ListSources below) carries the
			// correct TypeLabel() already - nothing to record here.
		case err == nil:
			// Something else (a built-in, or another def) already held this ID.
			errRows = append(errRows, newSourceInfoError(d.ID, errors.New("id already in use")))
		default:
			// Nothing registered under this ID: construction must have
			// failed. Re-run it to recover the actual error for display.
			if _, cerr := ConstructSource(d); cerr != nil {
				errRows = append(errRows, newSourceInfoError(d.ID, cerr))
			}
		}
	}

	// ListSources is registry-map order (nondeterministic, a pre-existing
	// quirk of this command); sort so the full-registry views are stable and
	// consistent with SourcesForGame's already-sorted scoped view.
	srcs := svc.ListSources()
	sort.Slice(srcs, func(i, j int) bool { return srcs[i].ID() < srcs[j].ID() })
	var inUseIDs map[string]bool
	switch {
	case game != nil && !all:
		srcs, err = svc.SourcesForGame(game.ID)
		if err != nil {
			return nil, err
		}
	case game != nil:
		scoped, err := svc.SourcesForGame(game.ID)
		if err != nil {
			return nil, err
		}
		inUseIDs = make(map[string]bool, len(scoped))
		for _, s := range scoped {
			inUseIDs[s.ID()] = true
		}
	}

	rows := make([]SourceInfo, 0, len(srcs)+len(errRows)+len(loadErrs))
	for _, src := range srcs {
		rows = append(rows, SourceInfo{
			ID:           src.ID(),
			Name:         src.Name(),
			Type:         source.TypeLabelOf(src),
			Auth:         authState(src),
			Capabilities: capabilitySummary(source.CapabilitiesOf(src)),
			InUse:        inUseIDs[src.ID()],
		})
	}
	rows = append(rows, errRows...)
	for _, le := range loadErrs {
		rows = append(rows, newSourceInfoError(le.File, le.Err))
	}
	return rows, nil
}

// isCustomSource reports whether src is a user-defined source (as opposed to
// a built-in like NexusMods/CurseForge): a self-reported type of exactly
// "directory", "manifest", or "api". "built-in" and the "unknown" fallback
// both answer false - conservative on the unknown side so the definitions
// reclassify loop (the only call site) reports a collision/error row rather
// than assuming an unlabeled source is the definition's own. Unreachable in
// practice: LoadSourceDefinitions guarantees ID uniqueness within a load, so
// a registered source matching a definition's ID is either a built-in or
// that definition's own constructed source - never an unrelated third party.
func isCustomSource(src source.ModSource) bool {
	switch source.TypeLabelOf(src) {
	case "directory", "manifest", "api":
		return true
	}
	return false
}

// authState reports a source's authentication status for display.
func authState(src source.ModSource) string {
	if !source.CapabilitiesOf(src).Auth {
		return "n/a"
	}
	if a, ok := src.(interface{ IsAuthenticated() bool }); ok {
		if a.IsAuthenticated() {
			return "yes"
		}
		return "no"
	}
	return "yes"
}

// capabilitySummary renders capabilities as a compact list, e.g. "search,updates".
func capabilitySummary(c source.Capabilities) string {
	out := ""
	add := func(enabled bool, name string) {
		if !enabled {
			return
		}
		if out != "" {
			out += ","
		}
		out += name
	}
	add(c.Search, "search")
	add(c.Dependencies, "deps")
	add(c.Updates, "updates")
	add(c.Auth, "auth")
	add(c.Versions, "versions")
	return out
}
