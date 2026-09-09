package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/curseforge"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/icarus"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/nexusmods"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steamworkshop"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// icarusFirestoreProjectID is the Firestore project backing the Icarus source.
const icarusFirestoreProjectID = "projectdaedalus-fb09f"

// builtinSourceFactories constructs the first-party sources keyless; API keys
// are attached after construction by registerSource so built-ins and custom
// sources share one key pipeline.
//
// Each factory is handed the resolved Paths: steamworkshop (#269) needs the
// cache root for its metadata cache (<CacheDir>/_steamworkshop/meta/), the
// only built-in that needs anything from the environment beyond a key.
var builtinSourceFactories = []func(Paths) source.ModSource{
	func(Paths) source.ModSource { return nexusmods.New(nil, "") },
	func(Paths) source.ModSource { return curseforge.New(nil, "") },
	func(Paths) source.ModSource { return icarus.New(nil, icarusFirestoreProjectID) },
	func(p Paths) source.ModSource {
		return steamworkshop.New(steamworkshop.Options{CacheDir: p.CacheDir})
	},
}

// registerSources registers the built-in sources followed by every custom
// source definition under <ConfigDir>/sources. Built-ins register first, so a
// custom definition reusing a built-in ID loses the collision (and warns).
func registerSources(ctx context.Context, svc *core.Service, p Paths, warn io.Writer) {
	for _, factory := range builtinSourceFactories {
		registerSource(ctx, svc, factory(p), warn)
	}
	registerCustomSources(ctx, svc, p.ConfigDir, warn)
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
		key, err := ResolveAPIKey(ctx, svc, src)
		if err != nil {
			// #79: the credential is there but unreadable (the key file was
			// deleted or replaced). Registering keyless is still the right
			// outcome - the source works for anything anonymous - but
			// silently doing so is how a user ends up debugging a 401 with
			// no idea their key stopped being used.
			_, _ = fmt.Fprintf(warn, "warning: source %q: %v\n", id, err) //nolint:errcheck // best-effort warning write
		}
		if key != "" {
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

// credentialVia names which credential lmm will actually send for a
// source, from whether each kind is available: "env" when the environment
// variable named by EnvKeyFor is set - it wins outright, so a one-off
// `NEXUSMODS_API_KEY=... lmm ...` overrides a stored key without touching
// it - "stored" when only a usable `lmm auth login` token exists, and ""
// when neither does. shadowed reports the case #356 was filed about: a
// stored token that exists and would work, with the environment variable
// in front of it.
//
// This is the ONE place that precedence is written down. ResolveAPIKey
// (what the source clients are handed) and AuthStatus (what `lmm auth
// status`, GET /api/v1/auth and the web setup card report) both derive
// their answer from it, so they cannot disagree about which key is in use
// the way they did before #356 - status said "stored" whenever a token
// existed, while the clients were already sending the environment key.
//
// It takes two booleans rather than the credentials themselves, and that
// is what lets both callers stay cheap and safe. ResolveAPIKey settles its
// answer before reading the token store at all, because envSet decides it
// on its own (this runs once per source at every app.Open). AuthStatus
// answers the stored half from db.ListTokens' listing, which describes a
// row without handing back its key (#79) - so no status surface decrypts a
// credential it is only going to describe.
func credentialVia(envSet, storedUsable bool) (via string, shadowed bool) {
	switch {
	case envSet:
		return "env", storedUsable
	case storedUsable:
		return "stored", false
	default:
		return "", false
	}
}

// ResolveAPIKey returns the API key for src: the environment variable named
// by EnvKeyFor(src) wins, then the token stored by `lmm auth login`; "" if
// neither is set. The precedence itself is credentialVia's, shared with
// AuthStatus so the key lmm sends and the key it reports are one answer.
//
// The error is non-fatal by design and always accompanies an empty key: it
// reports a STORED credential that exists but could not be decrypted (#79 -
// a missing or replaced <DataDir>/key), which every caller treats as "no
// key" while telling the user why, rather than refusing to run. An
// environment key short-circuits before the stored one is even looked at,
// so it is never reported when a working key is in hand.
func ResolveAPIKey(ctx context.Context, svc *core.Service, src source.ModSource) (string, error) {
	envKey := os.Getenv(EnvKeyFor(src))
	// storedUsable is false here because it is not known yet, and asking
	// would cost a decrypt per source at every app.Open: envSet settles the
	// answer on its own when it is true, and when it is false the store has
	// to be read to find out either way.
	if via, _ := credentialVia(envKey != "", false); via == "env" {
		return envKey, nil
	}
	token, err := svc.GetSourceToken(ctx, src.ID())
	if err != nil {
		return "", err
	}
	if token == nil {
		return "", nil
	}
	return token.APIKey, nil
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
		key, err := ResolveAPIKey(ctx, svc, src)
		if err != nil {
			return "", err
		}
		if key != "" {
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

// AuthState is a source's authentication status: whether it has no auth
// capability at all, has one but hasn't authenticated, or has. Wire-typed
// (final review, Important #1 / #301) rather than the pre-#301 display
// string ("yes"/"no"/"n/a") that source list --json carried directly onto
// the wire - a JSON consumer classifies a source's auth state without
// parsing English, and a text/JSON-view renderer that still wants those
// exact words derives them from this enum instead (cmd/lmm/source.go).
//
// AuthUnknown is the zero value, deliberately never assigned by authState:
// a source that registered always gets one of the other three. It exists
// so SourceInfo.Auth's omitzero tag has a true "never determined" state to
// fall back to for a definition that never registered at all (an error
// row) - final review, Important #2 / #302: before this, an error row's
// unset Auth field was indistinguishable from AuthNone ("no auth capability
// at all"), a claim never actually evaluated for it.
type AuthState int

const (
	// AuthUnknown means this row's auth state was never determined - the
	// zero value, seen only on an error row (a source definition that never
	// registered, so authState was never called for it).
	AuthUnknown AuthState = iota
	// AuthNone means the source has no auth capability at all - the old
	// display string's "n/a".
	AuthNone
	// AuthRequired means the source can authenticate but hasn't - "no".
	AuthRequired
	// AuthAuthenticated means the source has authenticated - "yes".
	AuthAuthenticated
)

// authStateNames maps each AuthState to its wire name. Keep in declaration order.
var authStateNames = [...]string{
	AuthUnknown:       "unknown",
	AuthNone:          "none",
	AuthRequired:      "required",
	AuthAuthenticated: "authenticated",
}

// String returns the state's wire name.
func (a AuthState) String() string {
	if a >= 0 && int(a) < len(authStateNames) && authStateNames[a] != "" {
		return authStateNames[a]
	}
	return fmt.Sprintf("auth_state(%d)", int(a))
}

// MarshalText implements encoding.TextMarshaler.
func (a AuthState) MarshalText() ([]byte, error) { return []byte(a.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (a *AuthState) UnmarshalText(b []byte) error {
	for i, n := range authStateNames {
		if n == string(b) {
			*a = AuthState(i)
			return nil
		}
	}
	return fmt.Errorf("unknown auth state %q", b)
}

// SourceInfo is one row of `lmm source list`: a registered source, or a
// definition that never became one.
//
//   - Type is the source's own TypeLabel ("built-in", "directory",
//     "manifest", "api") or "error" for a definition that failed to load,
//     collided with a registered ID, or failed to construct.
//   - Auth is omitzero (final review, Important #2 / #302): an error row's
//     auth state was never determined (it never registered, so authState
//     was never called for it), so it carries no "auth" key at all rather
//     than falsely asserting AuthNone's "no auth capability" - which
//     authState never actually evaluated for it. A registered source always
//     gets one of AuthState's other three values, so this never drops a key
//     for a real row.
//   - Capabilities is the source's enabled capability names, in a fixed
//     order (search, deps, updates, auth, versions) - not the pre-#301
//     comma-joined display string; a text renderer that wants the old
//     column joins it back with strings.Join.
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
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Type         string    `json:"type"`
	Auth         AuthState `json:"auth,omitzero"`
	Capabilities []string  `json:"capabilities"`
	InUse        bool      `json:"in_use,omitzero"`
	Err          error     `json:"-"`
	ErrorMessage string    `json:"error,omitempty"`
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

// authState reports a source's authentication status.
func authState(src source.ModSource) AuthState {
	if !source.CapabilitiesOf(src).Auth {
		return AuthNone
	}
	if a, ok := src.(interface{ IsAuthenticated() bool }); ok {
		if a.IsAuthenticated() {
			return AuthAuthenticated
		}
		return AuthRequired
	}
	return AuthAuthenticated
}

// capabilitySummary returns c's enabled capability names, in a fixed order
// (search, deps, updates, auth, versions) - the same order the pre-#301
// comma-joined display string used.
func capabilitySummary(c source.Capabilities) []string {
	var out []string
	add := func(enabled bool, name string) {
		if enabled {
			out = append(out, name)
		}
	}
	add(c.Search, "search")
	add(c.Dependencies, "deps")
	add(c.Updates, "updates")
	add(c.Auth, "auth")
	add(c.Versions, "versions")
	return out
}
