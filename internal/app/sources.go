package app

import (
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
func registerSources(svc *core.Service, cfgDir string, warn io.Writer) {
	for _, factory := range builtinSourceFactories {
		registerSource(svc, factory(), warn)
	}
	registerCustomSources(svc, cfgDir, warn)
}

// registerSource attaches src's API key (when it declares Auth) and registers
// it, unless the ID is already taken.
func registerSource(svc *core.Service, src source.ModSource, warn io.Writer) {
	id := src.ID()
	if _, err := svc.GetSource(id); err == nil {
		fmt.Fprintf(warn, "warning: skipping source %q: id already in use\n", id)
		return
	}
	// Gate on Capabilities().Auth: a key set on an auth-less source would be
	// stored but never attached to a request.
	if setter, ok := src.(interface{ SetAPIKey(string) }); ok && source.CapabilitiesOf(src).Auth {
		if key := ResolveAPIKey(svc, src); key != "" {
			setter.SetAPIKey(key)
		}
	}
	svc.RegisterSource(src)
}

// registerCustomSources loads <cfgDir>/sources/*.yaml and registers each
// definition, warning and skipping on load errors, construction failures, and
// ID collisions.
func registerCustomSources(svc *core.Service, cfgDir string, warn io.Writer) {
	defs, loadErrs, err := config.LoadSourceDefinitions(cfgDir)
	if err != nil {
		fmt.Fprintf(warn, "warning: loading custom sources: %v\n", err)
		return
	}
	for _, le := range loadErrs {
		fmt.Fprintf(warn, "warning: skipping source definition %v\n", le)
	}
	for _, def := range defs {
		src, err := custom.New(def)
		if err != nil {
			fmt.Fprintf(warn, "warning: skipping source %q: %v\n", def.ID, err)
			continue
		}
		registerSource(svc, src, warn)
	}
}

// ResolveAPIKey returns the API key for src: the environment variable named
// by EnvKeyFor(src) wins, then the token stored by `lmm auth login`; "" if
// neither is set.
func ResolveAPIKey(svc *core.Service, src source.ModSource) string {
	if key := os.Getenv(EnvKeyFor(src)); key != "" {
		return key
	}
	token, err := svc.GetSourceToken(src.ID())
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
