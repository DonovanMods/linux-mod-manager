// import_gates.go is what a profile import from someone else's file holds
// back until the user says otherwise (#621; owner rulings 2026-11-12):
//
//   - hooks, which are scripts lmm runs: listed in the plan, applied only
//     with ProfileImportOptions.WithHooks (`--with-hooks`);
//   - overrides that do not target a text config file: lmm writes an
//     override's content into the game directory, and a .dll or an .exe
//     there is code the game runs. Listed in the plan, skipped unless
//     ProfileImportOptions.AllowAnyOverrides (`--allow-any-overrides`).
//
// The gates apply to every import of a profile document - version 0 and the
// version-1 collection alike - so the older format is not a way around
// them. They do not apply to a profile the user already has, or to
// overrides set locally: only the import path consults them.
//
// Before either gate, a document whose hooks or override paths could make
// the plan show something other than what it holds - a control, escape or
// bidi/format character - or whose override path is absolute or escapes
// the game directory is refused outright (checkImportedProfile).
package core

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ErrUnsafeImport refuses a profile document whose hooks or override paths
// could not be shown as they are, or whose override path leaves the game
// directory. It is the caller's input, not a failure.
var ErrUnsafeImport = errors.New("the profile document is refused")

// IsUnsafeImport reports whether err refused a profile document for a hook
// or override path no option may let through (ErrUnsafeImport).
func IsUnsafeImport(err error) bool { return errors.Is(err, ErrUnsafeImport) }

// textConfigOverrideExts are the file types an imported override may
// target by default: text configuration a game reads, never code it runs.
// Besides the generic formats: .properties (Java key=value), .config
// (.NET/Unity XML), .ltx (S.T.A.L.K.E.R.), .lsx (Larian XML), .cvars
// (cvar lists) and .ron (Rusty Object Notation).
var textConfigOverrideExts = map[string]bool{
	".ini": true, ".cfg": true, ".conf": true, ".txt": true, ".json": true,
	".xml": true, ".toml": true, ".yaml": true, ".yml": true,
	".properties": true, ".config": true, ".ltx": true, ".lsx": true,
	".cvars": true, ".ron": true,
}

// TextConfigOverrideExtensions lists, in order, the file extensions an
// imported override may target without ProfileImportOptions.AllowAnyOverrides.
func TextConfigOverrideExtensions() []string {
	out := make([]string, 0, len(textConfigOverrideExts))
	for ext := range textConfigOverrideExts {
		out = append(out, ext)
	}
	sort.Strings(out)
	return out
}

// RestrictedOverride is one override of an imported document that does
// not target a text config file: skipped unless the import allows any
// override.
type RestrictedOverride struct {
	// Path is the override's path, relative to the game directory.
	Path string `json:"path"`
	// Reason says why it is held back.
	Reason string `json:"reason"`
}

// ImportHook is one hook an imported profile document carries: the
// profile hook it sets ("install.after_all") and the command, which is
// empty for a hook the document explicitly disables.
type ImportHook struct {
	Hook    string `json:"hook"`
	Command string `json:"command"`
}

// checkImportedProfile refuses what no option may let through: a hook
// command or override path holding a character that could make it display
// as something else, and an override path that is absolute or escapes the
// game directory (the deploy's own rule, applied before anything is saved).
func checkImportedProfile(profile *domain.Profile) error {
	for _, h := range listHooks(profile.Hooks, profile.HooksExplicit) {
		if hasUnsafeRunes(h.Command) {
			return fmt.Errorf("%w: the %s hook's command contains control or formatting characters, so the plan could not show it as it would run", ErrUnsafeImport, h.Hook)
		}
	}
	for path := range profile.Overrides {
		if hasUnsafeRunes(path) {
			return fmt.Errorf("%w: an override path (%q) contains control or formatting characters", ErrUnsafeImport, path)
		}
		if err := checkOverridePath(path); err != nil {
			return fmt.Errorf("%w: %v", ErrUnsafeImport, err)
		}
	}
	return nil
}

// loaderConfigFiles are text config files, by file name, that tell a game
// or a mod loader which DLL or assembly to load (owner ruling, 2026-11-12):
// an override of one can make the game run code, so it is held back like
// a file that is not text config at all. Matched case-insensitively on the
// file name, wherever it sits. Each entry names the key that loads a
// library:
//
//   - doorstop_config.ini: Unity Doorstop, BepInEx's entry point -
//     target_assembly names the assembly Doorstop loads into the game.
//   - enblocal.ini: ENB - [PROXY] ProxyLibrary names a DLL ENB loads
//     (with EnableProxyLibrary).
var loaderConfigFiles = map[string]bool{
	"doorstop_config.ini": true,
	"enblocal.ini":        true,
}

// LoaderConfigOverrideFiles lists, in order, the loader configuration file
// names an imported override may target only with AllowAnyOverrides.
func LoaderConfigOverrideFiles() []string {
	out := make([]string, 0, len(loaderConfigFiles))
	for name := range loaderConfigFiles {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// isLoaderConfig reports whether name is one of loaderConfigFiles, in any
// case (strings.EqualFold, so no case mapping can make two names collide).
func isLoaderConfig(name string) bool {
	for known := range loaderConfigFiles {
		if strings.EqualFold(name, known) {
			return true
		}
	}
	return false
}

// restrictedOverrideReason is why an imported override at path is held
// back, or "" when it is a text config file it may write.
func restrictedOverrideReason(path string) string {
	if isLoaderConfig(filepath.Base(filepath.FromSlash(path))) {
		return "loader configuration: can make the game load a DLL; imported only with --allow-any-overrides"
	}
	if !textConfigOverrideExts[strings.ToLower(filepath.Ext(path))] {
		return "not a text config file (" + strings.Join(TextConfigOverrideExtensions(), ", ") + "); imported only with --allow-any-overrides"
	}
	return ""
}

// restrictedOverrides lists profile's overrides that are held back - not a
// text config file, or a loader configuration - in path order.
func restrictedOverrides(profile *domain.Profile) []RestrictedOverride {
	var out []RestrictedOverride
	for path := range profile.Overrides {
		if reason := restrictedOverrideReason(path); reason != "" {
			out = append(out, RestrictedOverride{Path: path, Reason: reason})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// overridePaths is every override path profile carries, in order.
func overridePaths(profile *domain.Profile) []string {
	if len(profile.Overrides) == 0 {
		return nil
	}
	out := make([]string, 0, len(profile.Overrides))
	for path := range profile.Overrides {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

// importAdjust is what an import changes in the profile its document
// describes before saving it: the collection state a collection import
// checked itself, and what the gates hold back.
type importAdjust struct {
	collection    *collectionState
	withholdHooks bool
	dropOverrides []RestrictedOverride
}

// applyTo adjusts profile, the document's profile, before it is saved.
func (a importAdjust) applyTo(profile *domain.Profile) {
	a.collection.applyTo(profile)
	if a.withholdHooks {
		profile.Hooks, profile.HooksExplicit = domain.GameHooks{}, domain.GameHooksExplicit{}
	}
	for _, o := range a.dropOverrides {
		delete(profile.Overrides, o.Path)
	}
	if len(profile.Overrides) == 0 {
		profile.Overrides = nil
	}
	a.collection.finish(profile)
}
