// tree_inputs.go is the acquire step for a mod reference's exact file lists
// and binary patches (#610 C5; design docs/plans/2026-11-10-collections-
// design.md §6): what a NexusMods collection records as an item's `hashes`
// (Vortex's replicate mode) and `patches`, carried on the profile as the
// reference's file_list and patches, keyed by file id.
//
// WHERE IT RUNS. In acquire, on the tree a file's archive extracted to,
// after the FOMOD resolve step and before the game's adapter lays the tree
// out (extractPristine): the paths both record are relative to the mod's
// installed tree as the curator's tool saw it, which is the archive's own
// layout, after its installer.
//
// WHAT IT DOES, in Vortex's order:
//
//  1. the file list: every file the list does not name is dropped, and
//     every file it names must be there;
//  2. the patches: each target must be there, and its CRC32 must be the
//     baseline the patch was made against; an import must have admitted the
//     patch for that target, and allowed code there when the patched file
//     is code by its name or its content (patch_admission.go). A mismatch REFUSES the install
//     (FilePatchError) - a deliberate divergence from Vortex, which warns
//     and installs the unpatched file: a collection whose patch did not
//     apply is the "looks right, plays wrong" outcome the gate exists to
//     prevent. Only BSDIFF40 is applied (bsdiff.go);
//  3. the md5 of every listed file, which describes the curator's file as
//     it was installed, so after any patch.
//
// Paths are a stranger's: the importer refuses one that is absolute,
// escapes the tree, or holds a control or formatting character, and none
// is ever rewritten. A path matches a member case-insensitively with either
// separator (the curator's tool runs on Windows), and must match exactly
// one.
//
// THE VARIANT. A file list or a patch changes the tree a file installs to,
// so it is an input of the mod's cache variant (treeInputsVariant), beside
// its FOMOD choices: two different lists never share an entry, and a mod
// with neither keeps exactly the variant it had.
package core

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

const (
	// maxPatchBytes caps one patch file. The largest of the 42 surveyed is
	// under 16 KB; Vortex itself refuses a patch over a fifth of its target.
	maxPatchBytes = 64 << 20
	// maxPatchedFileBytes caps a patch's target and its output: both are
	// held in memory while it is applied.
	maxPatchedFileBytes = 256 << 20
	// maxTreeInputPath caps one recorded path, in bytes.
	maxTreeInputPath = 1024
	// maxFileListEntries caps a reference's file lists together. The
	// largest surveyed list has 370 entries.
	maxFileListEntries = 50000
	// maxFilePatches caps a reference's patches together.
	maxFilePatches = 4096
)

// treeVariantScheme names treeInputsVariant's inputs and their encoding.
const treeVariantScheme = "lmm-tree-inputs-1"

// ErrTreeInput is an install refused because the archive is not the tree
// its file list or patches describe.
var ErrTreeInput = errors.New("the mod's files are not the ones the collection recorded")

// ErrPatchNotKept is a patch lmm does not have: the collection that
// carries it was never imported on this machine, or its copy was removed.
var ErrPatchNotKept = errors.New("the patch is not kept on this machine")

// FilePatchError refuses one file's install because of what its file list
// or patches found (ErrTreeInput): a listed file missing or with another
// md5, a patch's target missing or not the file the patch was made against
// (the CRC32 baseline), or a patch lmm could not apply.
type FilePatchError struct {
	// SourceID, ModID and Name are the mod; FileID the file installed.
	SourceID string
	ModID    string
	Name     string
	FileID   string
	// Path is the recorded path it is about.
	Path string
	// Reason says what was wrong, for a person.
	Reason string
	err    error
}

// Error implements error.
func (e *FilePatchError) Error() string {
	name := firstNonEmpty(e.Name, domain.ModKey(e.SourceID, e.ModID))
	return fmt.Sprintf("%s: %s: %s", name, e.Path, e.Reason)
}

// Unwrap implements errors.Unwrap: always ErrTreeInput, and the cause when
// there is one.
func (e *FilePatchError) Unwrap() []error {
	if e.err != nil {
		return []error{ErrTreeInput, e.err}
	}
	return []error{ErrTreeInput}
}

// treeInputsVariant is the cache variant of a mod whose files have the
// tree inputs inputs (by file id), over the variant its FOMOD choices name
// (fomodPart): fomodPart itself when no file has any, so a mod with none
// keeps the variant it always had; else the first 12 hex digits of a
// sha256 over the scheme, fomodPart, and every file's inputs in file-id
// order, each as recorded.
func treeInputsVariant(fomodPart string, inputs map[string]domain.TreeInputs) string {
	ids := make([]string, 0, len(inputs))
	for id, in := range inputs {
		if !in.IsZero() {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return fomodPart
	}
	slices.Sort(ids)
	h := sha256.New()
	_, _ = io.WriteString(h, treeVariantScheme+"\n"+fomodPart)
	for _, id := range ids {
		in := inputs[id]
		_, _ = io.WriteString(h, "\nfile "+strconv.Quote(id))
		for _, e := range in.FileList {
			_, _ = io.WriteString(h, "\nlist "+strconv.Quote(e.Path)+" "+strings.ToLower(e.MD5))
		}
		for _, p := range in.Patches {
			_, _ = io.WriteString(h, "\npatch "+strconv.Quote(p.Path)+" "+strings.ToUpper(p.CRC32)+" "+strings.ToLower(p.SHA256))
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// refTreeInputs is ref's tree inputs for the files it pins: none when it
// pins none.
func refTreeInputs(ref *domain.ModReference) map[string]domain.TreeInputs {
	return treeInputsFor(ref.FileList, ref.Patches, ref.FileIDs)
}

// treeInputsFor is lists' and patches' inputs for the files ids: a list
// or a patch applies only to a file the mod pins, so for no ids, none.
func treeInputsFor(lists domain.FileLists, patches domain.FilePatches, ids []string) map[string]domain.TreeInputs {
	if lists.IsZero() && patches.IsZero() {
		return nil
	}
	out := map[string]domain.TreeInputs{}
	for _, id := range ids {
		if in := (domain.TreeInputs{FileList: lists[id], Patches: patches[id]}); !in.IsZero() {
			out[id] = in
		}
	}
	return out
}

// treeKey is a recorded path as a tree member is matched against it:
// slash-separated and lower case.
func treeKey(p string) string {
	return strings.ToLower(strings.Join(domain.SplitTreePath(p), "/"))
}

// checkTreeInputPath refuses a recorded path lmm could not match as
// written: empty, over the cap, absolute (a leading separator or a drive
// letter), with an empty, "." or ".." segment, or holding a control,
// escape or bidi/format character. It is never rewritten.
func checkTreeInputPath(p string) error {
	switch {
	case p == "" || len(p) > maxTreeInputPath:
		return fmt.Errorf("a path (%q) is empty or over the %d-byte limit", p, maxTreeInputPath)
	case hasUnsafeRunes(p):
		return fmt.Errorf("a path (%q) contains control or formatting characters", p)
	case len(p) >= 2 && p[1] == ':':
		return fmt.Errorf("a path (%q) names a drive", p)
	}
	for _, seg := range domain.SplitTreePath(p) {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("a path (%q) is absolute, or has an empty, \".\" or \"..\" segment", p)
		}
	}
	return nil
}

// isHexDigits reports whether s is n hex digits, in either case.
func isHexDigits(s string, n int) bool {
	if len(s) != n {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// checkTreeInputs refuses a reference's file lists and patches lmm would
// not apply as written (#610): over the caps, a path checkTreeInputPath
// refuses, a path listed twice for one file (in any case or either
// separator), an md5 that is not 32 hex digits, a CRC32 that is not 8, or
// a patch sha256 that is not lmm's own form (64 lower-case hex digits).
func checkTreeInputs(lists domain.FileLists, patches domain.FilePatches) error {
	var entries, count int
	for id, list := range lists {
		if hasUnsafeRunes(id) || len(id) > maxCollectionLine {
			return fmt.Errorf("a file list's file id (%q) contains control or formatting characters, or is over the %d-byte limit", id, maxCollectionLine)
		}
		entries += len(list)
		if entries > maxFileListEntries {
			return fmt.Errorf("its file lists hold more than %d files", maxFileListEntries)
		}
		seen := map[string]bool{}
		for _, e := range list {
			if err := checkTreeInputPath(e.Path); err != nil {
				return fmt.Errorf("file list: %v", err)
			}
			if !isHexDigits(e.MD5, md5.Size*2) {
				return fmt.Errorf("file list: %q has an md5 (%q) that is not 32 hex digits", e.Path, e.MD5)
			}
			if k := treeKey(e.Path); seen[k] {
				return fmt.Errorf("file list: %q is listed twice", e.Path)
			} else {
				seen[k] = true
			}
		}
	}
	for id, list := range patches {
		if hasUnsafeRunes(id) || len(id) > maxCollectionLine {
			return fmt.Errorf("a patch's file id (%q) contains control or formatting characters, or is over the %d-byte limit", id, maxCollectionLine)
		}
		count += len(list)
		if count > maxFilePatches {
			return fmt.Errorf("it holds more than %d patches", maxFilePatches)
		}
		seen := map[string]bool{}
		for _, p := range list {
			if err := checkTreeInputPath(p.Path); err != nil {
				return fmt.Errorf("patches: %v", err)
			}
			if !isHexDigits(p.CRC32, 8) {
				return fmt.Errorf("patches: %q has a CRC32 (%q) that is not 8 hex digits", p.Path, p.CRC32)
			}
			if !isLowerHexSHA256(p.SHA256) {
				return fmt.Errorf("patches: %q names a patch (%q) that is not 64 lower-case hex digits", p.Path, p.SHA256)
			}
			if k := treeKey(p.Path); seen[k] {
				return fmt.Errorf("patches: %q is patched twice", p.Path)
			} else {
				seen[k] = true
			}
		}
	}
	return nil
}

// patchStoreDir is where game's patches are kept: content-addressed by
// their sha256, beside the collections they came with.
func (s *Service) patchStoreDir(game *domain.Game) string {
	return filepath.Join(s.collectionsRoot(game), "patches")
}

// keepPatch keeps data, a BSDIFF40 patch an import admitted, in game's
// patch store, and returns its sha256: written beside its final name and
// renamed into place, once - a copy already there whose bytes are data's
// is left alone.
func (s *Service) keepPatch(game *domain.Game, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:])
	dir := s.patchStoreDir(game)
	path := filepath.Join(dir, name+".diff")
	if fileSHA256(path) == name {
		return name, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("keeping a patch: %w", err)
	}
	if err := writeAtomic(dir, path, data); err != nil {
		return "", fmt.Errorf("keeping a patch: %w", err)
	}
	return name, nil
}

// readPatch reads the patch named sha from game's patch store: a regular
// file within the cap whose content hashes to its name. Absent is
// ErrPatchNotKept.
func (s *Service) readPatch(game *domain.Game, sha string) ([]byte, error) {
	if !isLowerHexSHA256(sha) {
		return nil, fmt.Errorf("%w: %q does not name a patch", ErrPatchNotKept, sha)
	}
	path := filepath.Join(s.patchStoreDir(game), sha+".diff")
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w (import the collection that carries it again)", ErrPatchNotKept)
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: its copy is not a regular file", ErrPatchNotKept)
	}
	data, err := readCapped(path, maxPatchBytes)
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != sha {
		return nil, fmt.Errorf("%w: its copy is damaged (import the collection that carries it again)", ErrPatchNotKept)
	}
	return data, nil
}

// treeMembers indexes tree's regular files by treeKey, each key holding
// every member that matches it (several when two differ only in case).
func treeMembers(tree string) (map[string][]string, error) {
	listed, err := relativeFileMembers(tree)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(listed))
	for _, rel := range listed {
		k := treeKey(filepath.ToSlash(rel))
		out[k] = append(out[k], rel)
	}
	return out, nil
}

// applyTreeInputs applies one file's tree inputs to tree, the file's
// extracted (and FOMOD-resolved) archive (see the file comment). Nothing
// is done for none. A refusal is a *FilePatchError.
func (s *Service) applyTreeInputs(game *domain.Game, mod *domain.Mod, fileID, tree string, in domain.TreeInputs) error {
	if in.IsZero() {
		return nil
	}
	refuse := func(path, reason string, err error) error {
		return &FilePatchError{SourceID: mod.SourceID, ModID: mod.ID, Name: mod.Name, FileID: fileID, Path: path, Reason: reason, err: err}
	}
	members, err := treeMembers(tree)
	if err != nil {
		return fmt.Errorf("listing extracted members: %w", err)
	}
	find := func(path string) (string, error) {
		switch got := members[treeKey(path)]; len(got) {
		case 0:
			return "", refuse(path, "the archive holds no such file", nil)
		case 1:
			return got[0], nil
		default:
			return "", refuse(path, "the archive holds more than one file of that name, differing only in case", nil)
		}
	}

	// 1. The file list: only the listed files are kept.
	if len(in.FileList) > 0 {
		listed := make(map[string]bool, len(in.FileList))
		for _, e := range in.FileList {
			if _, err := find(e.Path); err != nil {
				return err
			}
			listed[treeKey(e.Path)] = true
		}
		for k, rels := range members {
			if listed[k] {
				continue
			}
			for _, rel := range rels {
				if err := os.Remove(filepath.Join(tree, rel)); err != nil {
					return fmt.Errorf("dropping %s, which the file list does not name: %w", rel, err)
				}
			}
		}
		if err := pruneEmptyDirs(tree); err != nil {
			return err
		}
	}

	// 2. The patches, each only onto its baseline.
	for _, p := range in.Patches {
		rel, err := find(p.Path)
		if err != nil {
			return err
		}
		target := filepath.Join(tree, rel)
		old, err := readCapped(target, maxPatchedFileBytes)
		if err != nil {
			return refuse(p.Path, "its file could not be read: "+err.Error(), err)
		}
		if got := fmt.Sprintf("%08X", crc32.ChecksumIEEE(old)); !strings.EqualFold(got, p.CRC32) {
			return refuse(p.Path, fmt.Sprintf("its CRC32 is %s, not the %s the collection's patch was made against, so the patch is not applied and the mod is not installed (Vortex would install it unpatched; lmm does not)", got, strings.ToUpper(p.CRC32)), nil)
		}
		patch, err := s.readPatch(game, p.SHA256)
		if err != nil {
			return refuse(p.Path, "its patch could not be read: "+err.Error(), err)
		}
		out, err := applyBSDIFF40(old, patch, maxPatchedFileBytes)
		if err != nil {
			return refuse(p.Path, "its patch could not be applied: "+err.Error(), err)
		}
		// #610 review B1: only where an import admitted it, and code only
		// where that import allowed code (patch_admission.go).
		if err := s.checkPatchAdmitted(game, p, out); err != nil {
			return refuse(p.Path, err.Error(), err)
		}
		if err := replaceFile(target, out); err != nil {
			return fmt.Errorf("writing the patched %s: %w", rel, err)
		}
	}

	// 3. Every listed file is the curator's, after its patch.
	for _, e := range in.FileList {
		rel, err := find(e.Path)
		if err != nil {
			return err
		}
		sum, err := md5File(filepath.Join(tree, rel))
		if err != nil {
			return refuse(e.Path, "its file could not be read: "+err.Error(), err)
		}
		if !strings.EqualFold(sum, e.MD5) {
			return refuse(e.Path, fmt.Sprintf("its md5 is %s, not the %s the collection recorded", sum, strings.ToLower(e.MD5)), nil)
		}
	}
	return nil
}

// replaceFile writes data to path through a temporary file beside it,
// renamed into place, never executable.
func replaceFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), cache.ReservedPrefix+"patch-*")
	if err != nil {
		return err
	}
	_, werr := io.Copy(tmp, bytes.NewReader(data))
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), 0o644)
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		_ = os.Remove(tmp.Name())
	}
	return werr
}

// pruneEmptyDirs removes every directory under root (not root itself)
// left with nothing in it, deepest first.
func pruneEmptyDirs(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		entries, err := os.ReadDir(dirs[i])
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			if err := os.Remove(dirs[i]); err != nil {
				return err
			}
		}
	}
	return nil
}
