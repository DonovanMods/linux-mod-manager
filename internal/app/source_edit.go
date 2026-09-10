package app

// source_edit.go is the WRITE half of the custom-source surface (#333):
// locating, reading, saving and deleting the YAML definitions under
// <configDir>/sources, and swapping the resulting source into a RUNNING
// registry so an edit takes effect without a restart.
//
// It lives in app for the same reason SourceInfos does: definitions on
// disk and the concrete source constructors are app's to see (core must
// not import internal/source/custom - Ruling 12, #300), and both frontends
// need identical answers. `lmm source add`/`lmm source remove` and the web
// UI's editor are two callers of the functions below, not two
// implementations of them.
//
// Before #333 the only way to add or remove a custom source was to write
// or delete the file by hand, which nothing validated and nothing checked
// against the games that map it. Every rule those two commands now enforce
// - the definition must construct, its id must match, a built-in id is not
// available, a source configured for a game is not removable - is here, so
// neither frontend can enforce a different set.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// sourcesDirName is the <configDir> subdirectory holding user-defined
// source definitions - the same one config.LoadSourceDefinitions reads.
const sourcesDirName = "sources"

// Errors a source-definition edit can fail with, as sentinels a frontend
// maps to its own vocabulary (an exit code, an HTTP status) instead of
// matching on message text.
var (
	// ErrSourceDefinitionNotFound means no file under <configDir>/sources
	// defines the requested id.
	ErrSourceDefinitionNotFound = errors.New("no source definition with that id")
	// ErrBuiltinSourceID means the id names a first-party source, which has
	// no definition file and cannot be replaced by one: registerSources
	// registers the built-ins first, so a definition reusing the id would
	// lose the collision and never take effect.
	ErrBuiltinSourceID = errors.New("that id belongs to a built-in source")
	// ErrSourceIDMismatch means the caller named one id and the submitted
	// definition declares another. Saving it anyway would silently create
	// or edit a DIFFERENT source than the one asked for.
	ErrSourceIDMismatch = errors.New("the definition's id does not match the id being saved")
)

// SourcesDir returns <configDir>/sources, where user-defined source
// definitions live.
func SourcesDir(configDir string) string {
	return filepath.Join(configDir, sourcesDirName)
}

// IsBuiltinSourceID reports whether id names one of the first-party
// sources. It asks the factories rather than a hand-kept list, so a
// built-in added later is covered without anyone remembering this
// function exists.
func IsBuiltinSourceID(id string) bool {
	// A zero Paths is fine here: no factory consults it to answer ID().
	for _, factory := range builtinSourceFactories {
		if factory(Paths{}).ID() == id {
			return true
		}
	}
	return false
}

// SourceDefinitionFile returns the path of the definition file under
// <configDir>/sources whose parsed id is sourceID, or
// ErrSourceDefinitionNotFound.
//
// It searches by the definition's OWN id rather than assuming
// "<id>.yaml": nothing has ever required the two to agree, so a definition
// a user wrote as my-mods.yml with `id: donovan-mods` has to be findable.
// Files that fail to parse are skipped - they define no id to match.
func SourceDefinitionFile(configDir, sourceID string) (string, error) {
	dir := SourcesDir(configDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: %s", ErrSourceDefinitionNotFound, sourceID)
	}
	if err != nil {
		return "", fmt.Errorf("reading sources directory %s: %w", dir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || (filepath.Ext(name) != ".yaml" && filepath.Ext(name) != ".yml") {
			continue
		}
		path := filepath.Join(dir, name)
		def, err := LoadSourceDefinitionFile(path)
		if err != nil {
			continue
		}
		if def.ID == sourceID {
			return path, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrSourceDefinitionNotFound, sourceID)
}

// ReadSourceDefinition returns the raw YAML bytes of sourceID's definition
// file - the text an editor loads, comments and formatting intact, rather
// than a re-serialisation of the parsed struct (which would silently
// discard both).
func ReadSourceDefinition(configDir, sourceID string) ([]byte, error) {
	path, err := SourceDefinitionFile(configDir, sourceID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading definition: %w", err)
	}
	return data, nil
}

// SaveSourceDefinition validates data, writes it under <configDir>/sources
// and swaps the resulting source into svc's running registry, returning the
// validation report either way (a caller renders it on failure as well as
// success - that is the whole point of the editor's report).
//
// expectID, when non-empty, must equal the definition's own id, or the save
// is refused with ErrSourceIDMismatch. It is how a route that names the
// source in its path refuses to edit a different one; a caller that takes
// the id FROM the document (`lmm source add <file>`) passes "".
//
// Order is deliberate. Everything that can refuse - a parse failure, an id
// mismatch, a built-in id, a construction failure - runs before anything is
// written, so a rejected definition leaves no file behind. The file is
// written before the registry swap because the file is the durable truth: a
// swap refused by a cancelled ctx leaves the saved definition to be picked
// up at the next start, which is the pre-#333 behaviour, while the reverse
// order would leave a live source no file defines.
//
// The write is atomic (temp file + rename in the same directory), so a
// crash mid-save cannot leave a half-written definition that fails to parse
// at the next start.
func SaveSourceDefinition(ctx context.Context, svc *core.Service, expectID string, data []byte) (*SourceValidationReport, error) {
	report, def, err := ValidateSourceContent(data)
	if err != nil {
		return report, err
	}
	if expectID != "" && def.ID != expectID {
		return report, fmt.Errorf("%w: %q declares id %q", ErrSourceIDMismatch, expectID, def.ID)
	}
	if IsBuiltinSourceID(def.ID) {
		return report, fmt.Errorf("%w: %s", ErrBuiltinSourceID, def.ID)
	}
	src, err := ConstructSource(def)
	if err != nil {
		// A definition that validates but cannot construct would register
		// nowhere and show up as an error row; refuse it here instead, and
		// report it the same way a validation failure is reported.
		report.Valid = false
		report.Errors = append(report.Errors, err.Error())
		return report, err
	}

	path, err := SourceDefinitionFile(svc.ConfigDir(), def.ID)
	if errors.Is(err, ErrSourceDefinitionNotFound) {
		// A new definition takes the canonical name. def.Validate has
		// already pinned the id to ^[a-z0-9-]+$, so it cannot escape the
		// directory or name a hidden file.
		path, err = filepath.Join(SourcesDir(svc.ConfigDir()), def.ID+".yaml"), nil
	}
	if err != nil {
		return report, err
	}
	if err := writeFileAtomic(path, data); err != nil {
		return report, err
	}

	// Attach the key exactly as registration does (env var first, then the
	// stored token), so an edited source is as usable as a restarted one.
	attachAPIKey(ctx, svc, src)
	if _, err := svc.ReplaceSource(ctx, src); err != nil {
		return report, fmt.Errorf("saved %s, but the running server kept the previous source: %w", filepath.Base(path), err)
	}
	return report, nil
}

// DeleteSourceDefinition removes sourceID's definition file and unregisters
// its source. It refuses a built-in id (ErrBuiltinSourceID), an id no
// definition file defines (ErrSourceDefinitionNotFound), and a source that
// configured games still map (*core.SourceInUseError - remove it from those
// games first).
//
// The registry is emptied BEFORE the file, and the removed source is put
// back if the file removal then fails, so the two halves never disagree:
// nothing on disk changes until the (cancellable) registry step has
// succeeded, and a failure afterwards is compensated rather than left
// half-applied. The re-registration runs under context.WithoutCancel for
// the same reason core's completion writes do - a compensation that
// inherits the cancellation that caused it is no compensation.
//
// The in-use check is svc.UnregisterSourceIfUnused's, not a separate call
// here (#333 Minor #1): checking and removing under the SAME beginOp is
// what stops a POST /api/v1/games mapping this source from landing in the
// gap between an ungated check and a later-gated unregister.
func DeleteSourceDefinition(ctx context.Context, svc *core.Service, sourceID string) error {
	if IsBuiltinSourceID(sourceID) {
		return fmt.Errorf("%w: %s", ErrBuiltinSourceID, sourceID)
	}
	path, err := SourceDefinitionFile(svc.ConfigDir(), sourceID)
	if err != nil {
		return err
	}

	// Keep the live source so a failed file removal can be undone. A
	// definition that never constructed is registered nowhere; that is not
	// an error (see Registry.Unregister).
	previous, lookupErr := svc.GetSource(sourceID)
	wasRegistered := lookupErr == nil
	if _, err := svc.UnregisterSourceIfUnused(ctx, sourceID); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if wasRegistered {
			if _, rerr := svc.ReplaceSource(context.WithoutCancel(ctx), previous); rerr != nil {
				return fmt.Errorf("removing %s failed (%w) and restoring the source failed too: %w", path, err, rerr)
			}
		}
		return fmt.Errorf("removing definition: %w", err)
	}
	return nil
}

// attachAPIKey sets src's API key the way registerSource does - gated on
// the source declaring Auth, resolved env-var-first - for a source being
// constructed outside startup.
//
// A stored credential that will not decrypt (#79) leaves the source
// keyless, exactly as a source with no credential at all: this is the
// re-key path, so the alternative would be refusing to rebuild a source
// over a key that is already unusable.
func attachAPIKey(ctx context.Context, svc *core.Service, src source.ModSource) {
	setter, ok := src.(interface{ SetAPIKey(string) })
	if !ok || !source.CapabilitiesOf(src).Auth {
		return
	}
	if key, _ := ResolveAPIKey(ctx, svc, src); key != "" { //nolint:errcheck // reported by the status surface, not fatal here
		setter.SetAPIKey(key)
	}
}

// writeFileAtomic writes data to path via a temp file in the same directory
// and a rename, creating the directory if needed. The temp file is removed
// on every failure path, so a failed save leaves nothing behind.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".lmm-source-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() //nolint:errcheck // best-effort cleanup; a successful rename makes this a no-op

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close() //nolint:errcheck // the write error is the one that matters
		return fmt.Errorf("writing definition: %w", err)
	}
	// 0644 matches every other config file this tool writes (games.yaml,
	// profiles); a definition carries no secret - keys come from the
	// environment or the token store, never from the file.
	if err := tmp.Chmod(0644); err != nil {
		_ = tmp.Close() //nolint:errcheck // the chmod error is the one that matters
		return fmt.Errorf("setting permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming into place: %w", err)
	}
	return nil
}

// RebuildSource constructs a FRESH source object for sourceID - a built-in
// from its own factory, a user-defined one from its definition file - with
// its API key attached exactly as registerSource attaches it (env var
// first, then the stored token). It performs no live I/O and does not touch
// the registry.
//
// "Fresh" is the point: a source's key is set by an unsynchronised field
// write (custom.API.SetAPIKey and the built-ins' equivalents), so re-keying
// the object another request may be mid-search on would be a data race.
// Building a new one and swapping it in under the mutation gate
// (RekeySource, below) is the same operation with no shared mutable state.
func RebuildSource(ctx context.Context, svc *core.Service, sourceID string) (source.ModSource, error) {
	// The rebuilt source must be configured exactly as the startup one was,
	// so a source with an on-disk cache (#269) keeps pointing at it.
	p := Paths{ConfigDir: svc.ConfigDir(), CacheDir: svc.CacheDir()}
	for _, factory := range builtinSourceFactories {
		if src := factory(p); src.ID() == sourceID {
			attachAPIKey(ctx, svc, src)
			return src, nil
		}
	}

	defs, _, err := LoadSourceDefinitions(svc.ConfigDir())
	if err != nil {
		return nil, fmt.Errorf("loading source definitions: %w", err)
	}
	for _, def := range defs {
		if def.ID != sourceID {
			continue
		}
		src, err := ConstructSource(def)
		if err != nil {
			return nil, fmt.Errorf("constructing source %q: %w", sourceID, err)
		}
		attachAPIKey(ctx, svc, src)
		return src, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrSourceDefinitionNotFound, sourceID)
}

// RekeySource makes a credential change take effect on the RUNNING service:
// it rebuilds sourceID with whatever key ResolveAPIKey now resolves (a
// freshly stored token, or - after a logout - the environment variable or
// nothing) and swaps it into the registry under the mutation gate, so the
// next search or install uses the new key with no restart.
//
// It reports true when a source was actually swapped. A source that is not
// registered, or that declares no Auth capability, is left alone and
// reports false with no error: a key stored for either of those changes
// nothing about how any request behaves (registerSource does not even
// attach one), and an orphaned token's "source" may not exist at all.
//
// A rebuild failure is returned. The credential itself has already been
// stored or deleted by the time this runs - that write is the durable
// effect and is not undone by a failure here; what is lost is only the
// no-restart part, which is exactly what the caller should say.
func RekeySource(ctx context.Context, svc *core.Service, sourceID string) (bool, error) {
	current, err := svc.GetSource(sourceID)
	if err != nil {
		return false, nil
	}
	if !source.CapabilitiesOf(current).Auth {
		return false, nil
	}
	rebuilt, err := RebuildSource(ctx, svc, sourceID)
	if err != nil {
		return false, err
	}
	if _, err := svc.ReplaceSource(ctx, rebuilt); err != nil {
		return false, err
	}
	return true, nil
}
