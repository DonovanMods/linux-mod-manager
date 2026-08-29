package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
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
