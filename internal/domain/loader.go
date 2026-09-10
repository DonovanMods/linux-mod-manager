// Package domain: this file holds the per-game MOD-LOADER declaration
// (#359) - the `loader:` block in games.yaml.
//
// A mod loader (BepInEx today) is not a mod, and modelling it as one fails
// every test: it installs into the game ROOT rather than the mod path, its
// presence is a property of the game INSTALLATION (you do not want it torn
// out because you switched to a vanilla-ish profile), a plugin is unusable
// without it, and the correct BUILD depends on facts about the game - Unity
// Mono versus IL2CPP, a native Linux build versus Proton - rather than about
// the mod. So it is a per-game prerequisite, declared here and checked at
// plan time (docs/plans/2026-09-09-bepinex-spike.md §2).
package domain

import (
	"fmt"
	"strings"
)

// LoaderKindBepInEx is the only loader kind lmm knows about today.
//
// GameLoader.Kind is a free string rather than an enum on purpose: BepInEx
// is one of several Unity loaders (MelonLoader is the obvious second), and a
// string leaves the door open at zero cost, which is cheaper than inventing
// a general "framework" abstraction before there is a second framework to
// generalise over.
const LoaderKindBepInEx = "bepinex"

// LoaderRuntime is the Unity scripting backend a game was built with, which
// decides which BepInEx build is the correct one: Unity Mono has stable
// BepInEx 5 releases, while IL2CPP needs a BepInEx 6 bleeding-edge build
// that is not a GitHub release at all (spike §1.1). lmm does not choose or
// download that build - it records which one the game needs and verifies
// what is installed against the declaration.
type LoaderRuntime int

// LoaderRuntimeUnknown, LoaderRuntimeMono and LoaderRuntimeIL2CPP are
// LoaderRuntime's values. Unknown is the zero value AND a legitimate state:
// a game may declare a loader before anything has answered the runtime
// question, and `lmm game show` answers it from disk when it can.
const (
	LoaderRuntimeUnknown LoaderRuntime = iota
	LoaderRuntimeMono
	LoaderRuntimeIL2CPP
)

// loaderRuntimeNames maps each LoaderRuntime to its wire name. Keep in
// declaration order. Unknown's name is empty, so it is omitted from YAML and
// JSON rather than written as a value nobody chose.
var loaderRuntimeNames = [...]string{
	LoaderRuntimeUnknown: "",
	LoaderRuntimeMono:    "mono",
	LoaderRuntimeIL2CPP:  "il2cpp",
}

// ValidLoaderRuntimes lists ParseLoaderRuntime's recognised non-empty
// values, in constant order, for "unrecognised value" error messages - the
// single source of truth, the way ValidLinkMethods and ValidDeployModes are.
const ValidLoaderRuntimes = "mono, il2cpp"

// String returns the runtime's wire name ("mono", "il2cpp", or "" for
// unknown).
func (r LoaderRuntime) String() string {
	if r >= 0 && int(r) < len(loaderRuntimeNames) {
		return loaderRuntimeNames[r]
	}
	return ""
}

// MarshalText implements encoding.TextMarshaler.
func (r LoaderRuntime) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (r *LoaderRuntime) UnmarshalText(b []byte) error {
	runtime, ok := ParseLoaderRuntime(string(b))
	if !ok {
		return fmt.Errorf("unknown loader runtime %q (valid: %s)", b, ValidLoaderRuntimes)
	}
	*r = runtime
	return nil
}

// ParseLoaderRuntime converts a string to LoaderRuntime, following
// ParseDeployMode's fail-loud contract: empty is "not answered yet" and
// returns Unknown with ok=true, any other unrecognised string returns
// ok=false so the caller can fail naming the field, the value and the game.
func ParseLoaderRuntime(s string) (runtime LoaderRuntime, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return LoaderRuntimeUnknown, true
	case "mono":
		return LoaderRuntimeMono, true
	case "il2cpp":
		return LoaderRuntimeIL2CPP, true
	default:
		return LoaderRuntimeUnknown, false
	}
}

// LoaderBootstrap is HOW the loader is injected on Linux, and the two values
// are genuinely different mechanisms rather than a preference (spike §1.2):
// a native Linux build uses run_bepinex.sh, which sets up LD_PRELOAD for
// libdoorstop.so, while a Proton/Wine game uses the Windows winhttp.dll
// proxy plus doorstop_config.ini. They need different BepInEx archives and
// different Steam launch options, and getting it wrong leaves a game that
// silently loads nothing.
type LoaderBootstrap int

// LoaderBootstrapUnknown, LoaderBootstrapNative and LoaderBootstrapProton
// are LoaderBootstrap's values, Unknown being the zero value and a
// legitimate "not answered yet".
const (
	LoaderBootstrapUnknown LoaderBootstrap = iota
	LoaderBootstrapNative
	LoaderBootstrapProton
)

// loaderBootstrapNames maps each LoaderBootstrap to its wire name. Keep in
// declaration order.
var loaderBootstrapNames = [...]string{
	LoaderBootstrapUnknown: "",
	LoaderBootstrapNative:  "native",
	LoaderBootstrapProton:  "proton",
}

// ValidLoaderBootstraps is ValidLoaderRuntimes' counterpart for
// ParseLoaderBootstrap.
const ValidLoaderBootstraps = "native, proton"

// String returns the bootstrap's wire name ("native", "proton", or "" for
// unknown).
func (b LoaderBootstrap) String() string {
	if b >= 0 && int(b) < len(loaderBootstrapNames) {
		return loaderBootstrapNames[b]
	}
	return ""
}

// MarshalText implements encoding.TextMarshaler.
func (b LoaderBootstrap) MarshalText() ([]byte, error) { return []byte(b.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (b *LoaderBootstrap) UnmarshalText(text []byte) error {
	bootstrap, ok := ParseLoaderBootstrap(string(text))
	if !ok {
		return fmt.Errorf("unknown loader bootstrap %q (valid: %s)", text, ValidLoaderBootstraps)
	}
	*b = bootstrap
	return nil
}

// ParseLoaderBootstrap converts a string to LoaderBootstrap on
// ParseLoaderRuntime's terms.
func ParseLoaderBootstrap(s string) (bootstrap LoaderBootstrap, ok bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return LoaderBootstrapUnknown, true
	case "native":
		return LoaderBootstrapNative, true
	case "proton":
		return LoaderBootstrapProton, true
	default:
		return LoaderBootstrapUnknown, false
	}
}

// GameLoader is a game's mod-loader declaration: which loader is installed
// (or required) in the game directory, at what version, for which Unity
// runtime, injected which way.
//
// Every field but Kind is optional, because each is a fact a user may not
// know when they configure the game and lmm can often answer from disk
// afterwards. Version is what the user says is installed: `lmm verify`
// compares it against what it finds and reports DRIFT rather than repairing
// it, since lmm does not install the loader (spike §4 - choosing the right
// build is the hard part, and getting it wrong leaves a game that silently
// loads nothing).
type GameLoader struct {
	// Kind is the loader's name, lower-case by convention; today only
	// "bepinex" means anything to lmm. See LoaderKindBepInEx.
	Kind string `json:"kind"`
	// Version is the loader version installed in the game directory, e.g.
	// "5.4.23.5". Optional: empty means "do not check the version".
	Version string `json:"version,omitempty"`
	// Runtime is the game's Unity scripting backend. Optional.
	Runtime LoaderRuntime `json:"runtime,omitempty"`
	// Bootstrap is how the loader is injected on Linux. Optional.
	Bootstrap LoaderBootstrap `json:"bootstrap,omitempty"`
}

// IsBepInEx reports whether this declaration names BepInEx, comparing
// case-insensitively because Kind is a value a user types into games.yaml
// (and "BepInEx" is how the project spells its own name). A nil receiver
// declares nothing, so every caller can ask without a guard.
func (l *GameLoader) IsBepInEx() bool {
	return l != nil && strings.EqualFold(strings.TrimSpace(l.Kind), LoaderKindBepInEx)
}

// DeclaresBepInEx is the question every BepInEx rule asks of a game: does
// its config say the loader is set up here? It gates the archive-root
// normaliser's two ambiguous shapes (#358), the plan-time precondition and
// the verify tier, so it lives on Game rather than being re-derived at each.
func (g *Game) DeclaresBepInEx() bool { return g != nil && g.Loader.IsBepInEx() }
