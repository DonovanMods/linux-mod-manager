// patch_admission.go binds a kept patch to the files an import admitted it
// for (#610 review B1). A BSDIFF40 patch's extra block can carry any bytes,
// so a patch the store keeps is a payload that could be re-aimed: a profile
// document naming its sha256 against another file - a DLL - would install
// whatever the patch makes. So every use of a patch is admitted per TARGET,
// by the import that brought it in or named it, and recorded beside the
// patch:
//
//	<cache>/collections/patches/<sha256>.json   {"targets": {"nexusmods:21|mod/manifest.json": false}}
//
// keyed by the mod and the target as a tree member is matched (treeKey),
// each with whether the import allowed it to make code
// (--allow-any-overrides).
//
// Every profile import - a version-0 document, a collection, a collection
// update - plans its patches the same way (importTreeInputs) and is gated
// the same way at apply (checkPatchGate, then admitImportPatches once
// nothing can refuse the import any more): a patch to code by its name, or a
// patch the store keeps for other files, needs --allow-any-overrides, and
// without it the import is refused rather than the patch dropped (the mod
// would install unpatched). The acquire step (applyTreeInputs) applies a
// patch only to a target its record admits, and one whose OUTPUT is native
// code or a script only when that admission allowed code.
package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ImportTreeInputs is one mod of an import plan that installs with an exact
// file list or binary patches (#610): what an import of any profile
// document - a version-0 export or a collection - shows before anything is
// written.
type ImportTreeInputs struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// FileList counts the files its exact file lists name.
	FileList int `json:"file_list,omitzero"`
	// Patches are its binary patches, each with why it needs
	// --allow-any-overrides when it does.
	Patches []CollectionItemPatch `json:"patches,omitempty"`
}

// patchUse is one patch a reference applies.
type patchUse struct {
	sourceID, modID, name string
	fileID                string
	patch                 domain.FilePatch
}

// patchUses lists every patch refs apply to the files they pin, in ref,
// file and path order.
func patchUses(refs []domain.ModReference, names map[string]string) []patchUse {
	var out []patchUse
	for i := range refs {
		ref := &refs[i]
		inputs := refTreeInputs(ref)
		ids := make([]string, 0, len(inputs))
		for id := range inputs {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			for _, p := range inputs[id].Patches {
				out = append(out, patchUse{sourceID: ref.SourceID, modID: ref.ModID, name: names[domain.ModKey(ref.SourceID, ref.ModID)], fileID: id, patch: p})
			}
		}
	}
	return out
}

// patchAdmission is a kept patch's record: the targets imports admitted
// it for, each with whether it may make code there. A target is a mod and
// a path in its tree (admissionKey): a patch admitted for one mod's file
// is not admitted for the same path in another mod (#610 re-review).
type patchAdmission struct {
	Targets map[string]bool `json:"targets"`
}

// admissionKey is the record's key for the file path of the mod
// sourceID:modID: "<source>:<mod>|<path as treeKey>".
func admissionKey(sourceID, modID, path string) string {
	return domain.ModKey(sourceID, modID) + "|" + treeKey(path)
}

// key is u's admission key.
func (u patchUse) key() string { return admissionKey(u.sourceID, u.modID, u.patch.Path) }

// use is u as a use set names it: "<sha256>|<admission key>" - this patch,
// on this mod's file.
func (u patchUse) use() string { return u.patch.SHA256 + "|" + u.key() }

// broughtUses is the uses of refs whose patch an import brings (shas,
// from collectionPatchSources): the document that carries a patch says
// where it applies, so those uses - and no other mod's or file's use of
// the same patch - come in with it (#610 re-review).
func broughtUses(refs []domain.ModReference, shas map[string]bool) map[string]bool {
	var out map[string]bool
	for _, u := range patchUses(refs, nil) {
		if shas[u.patch.SHA256] {
			if out == nil {
				out = map[string]bool{}
			}
			out[u.use()] = true
		}
	}
	return out
}

// patchAdmissionPath is the record of the patch sha.
func (s *Service) patchAdmissionPath(game *domain.Game, sha string) string {
	return filepath.Join(s.patchStoreDir(game), sha+keptIndexSuffix)
}

// readPatchAdmission is the targets the patch sha is admitted for; none
// when it has no record (or one lmm cannot read, which admits nothing).
func (s *Service) readPatchAdmission(game *domain.Game, sha string) map[string]bool {
	if !isLowerHexSHA256(sha) {
		return nil
	}
	data, err := os.ReadFile(s.patchAdmissionPath(game, sha))
	if err != nil {
		return nil
	}
	var rec patchAdmission
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil
	}
	return rec.Targets
}

// admitPatches records each use's target as admitted for its patch, code
// allowed when code is: an admission is only ever added to, and allowing
// code is never taken back. undo puts every record it changed back as it
// was (removing one it created), for an import refused after it admitted:
// a refused import changes nothing.
func (s *Service) admitPatches(game *domain.Game, uses []patchUse, code bool) (undo func(), err error) {
	type prior struct {
		data   []byte
		exists bool
	}
	changed := map[string]prior{}
	undo = func() {
		for sha, p := range changed {
			path := s.patchAdmissionPath(game, sha)
			if !p.exists {
				_ = os.Remove(path)
				continue
			}
			_ = writeAtomic(filepath.Dir(path), path, p.data)
		}
	}
	byPatch := map[string][]string{}
	for _, u := range uses {
		byPatch[u.patch.SHA256] = append(byPatch[u.patch.SHA256], u.key())
	}
	shas := make([]string, 0, len(byPatch))
	for sha := range byPatch {
		shas = append(shas, sha)
	}
	slices.Sort(shas)
	for _, sha := range shas {
		if !isLowerHexSHA256(sha) {
			continue
		}
		rec := patchAdmission{Targets: s.readPatchAdmission(game, sha)}
		if rec.Targets == nil {
			rec.Targets = map[string]bool{}
		}
		dirty := false
		for _, t := range byPatch[sha] {
			was, ok := rec.Targets[t]
			if !ok || (code && !was) {
				rec.Targets[t] = was || code
				dirty = true
			}
		}
		if !dirty {
			continue
		}
		data, err := json.Marshal(rec)
		if err != nil {
			undo()
			return func() {}, err
		}
		dir := s.patchStoreDir(game)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			undo()
			return func() {}, fmt.Errorf("recording a patch's admission: %w", err)
		}
		path := s.patchAdmissionPath(game, sha)
		old, readErr := os.ReadFile(path)
		if err := writeAtomic(dir, path, data); err != nil {
			undo()
			return func() {}, fmt.Errorf("recording a patch's admission: %w", err)
		}
		changed[sha] = prior{data: old, exists: readErr == nil}
	}
	return undo, nil
}

// patchRestriction is why u needs --allow-any-overrides, or "" when it
// does not: a patch to code, by the file's name; or - for a patch this
// import does not itself bring (fromImport false) - a patch the store
// keeps but no import admitted for this mod's file.
func (s *Service) patchRestriction(game *domain.Game, u patchUse, fromImport bool) string {
	if reason := patchCodeReason(u.patch.Path); reason != "" {
		return reason
	}
	if fromImport {
		return ""
	}
	if _, err := os.Lstat(filepath.Join(s.patchStoreDir(game), u.patch.SHA256+recipePatchExt)); err != nil {
		return "" // not kept here at all: its install fails on its own (ErrPatchNotKept)
	}
	if _, ok := s.readPatchAdmission(game, u.patch.SHA256)[u.key()]; !ok {
		return "a patch lmm keeps for another file: applying it here could make any content, so it is applied only with --allow-any-overrides"
	}
	return ""
}

// importTreeInputs is an import plan's tree inputs: every ref's file lists
// and patches, each patch with its restriction (patchRestriction; sources
// names the uses the import itself brings, broughtUses).
func (s *Service) importTreeInputs(game *domain.Game, refs []domain.ModReference, sources map[string]bool) []ImportTreeInputs {
	var out []ImportTreeInputs
	for i := range refs {
		ref := &refs[i]
		inputs := refTreeInputs(ref)
		if len(inputs) == 0 {
			continue
		}
		it := ImportTreeInputs{SourceID: ref.SourceID, ModID: ref.ModID}
		for _, u := range patchUses(refs[i:i+1], nil) {
			it.Patches = append(it.Patches, CollectionItemPatch{FileID: u.fileID, Path: u.patch.Path, CRC32: strings.ToUpper(u.patch.CRC32),
				Restricted: s.patchRestriction(game, u, sources[u.use()])})
		}
		for _, in := range inputs {
			it.FileList += len(in.FileList)
		}
		out = append(out, it)
	}
	return out
}

// restrictedTreePatches counts and names the restricted patches of
// inputs: "<source>:<mod>: <path>", the first maxIgnoredWarnings of them.
func restrictedTreePatches(inputs []ImportTreeInputs) (int, []string) {
	n := 0
	var names []string
	for _, it := range inputs {
		for _, p := range it.Patches {
			if p.Restricted == "" {
				continue
			}
			if n++; n <= maxIgnoredWarnings {
				names = append(names, domain.ModKey(it.SourceID, it.ModID)+": "+p.Path)
			}
		}
	}
	if n > maxIgnoredWarnings {
		names = append(names, fmt.Sprintf("and %d more", n-maxIgnoredWarnings))
	}
	return n, names
}

// checkPatchGate is an import's patch gate at apply, recomputed from refs
// rather than trusted from the plan: a restricted patch refuses the import
// (ErrCodePatch) unless anyOverrides. It writes nothing.
func (s *Service) checkPatchGate(game *domain.Game, refs []domain.ModReference, sources map[string]bool, anyOverrides bool) error {
	n, names := restrictedTreePatches(s.importTreeInputs(game, refs, sources))
	if n > 0 && !anyOverrides {
		return fmt.Errorf("%w: %d of its patches change code or apply to a file no import admitted them for (%s); import it with --allow-any-overrides to apply them",
			ErrCodePatch, n, strings.Join(names, ", "))
	}
	return nil
}

// admitImportPatches admits an import's patches for their targets, once
// the gate passed: the ones the import brings (sources), and with
// anyOverrides every other the import listed - listed nil means every
// patch of refs; a collection update lists only the new version's
// (#610 re-review) - code allowed only with anyOverrides. undo takes the
// admissions back, for an import refused later.
func (s *Service) admitImportPatches(game *domain.Game, refs []domain.ModReference, sources, listed map[string]bool, anyOverrides bool) (func(), error) {
	var admit []patchUse
	for _, u := range patchUses(refs, nil) {
		if sources[u.use()] || (anyOverrides && (listed == nil || listed[u.use()])) {
			admit = append(admit, u)
		}
	}
	return s.admitPatches(game, admit, anyOverrides)
}

// listedPatches is the set admitImportPatches takes as listed: every patch
// use of refs, by "<sha256>|<admission key>".
func listedPatches(refs []domain.ModReference) map[string]bool {
	out := map[string]bool{}
	for _, u := range patchUses(refs, nil) {
		out[u.use()] = true
	}
	return out
}

// checkPatchAdmitted refuses applying the patch p (whose output is out)
// to its target in mod unless an import admitted it there - and, when the
// target is code by its name or the output is native code or a script by
// its content, admitted it to make code.
func (s *Service) checkPatchAdmitted(game *domain.Game, mod *domain.Mod, p domain.FilePatch, out []byte) error {
	code, ok := s.readPatchAdmission(game, p.SHA256)[admissionKey(mod.SourceID, mod.ID, p.Path)]
	if !ok {
		return errors.New("no import admitted this patch for this mod's file; import the collection that carries it again")
	}
	if code {
		return nil
	}
	head := out[:min(len(out), codeSniffBytes)]
	segs := domain.SplitTreePath(p.Path)
	if reason := bundledCodeReason(segs[len(segs)-1], head); reason != "" {
		return fmt.Errorf("the patched file would be %s; import the collection with --allow-any-overrides to apply it", reason)
	}
	return nil
}
