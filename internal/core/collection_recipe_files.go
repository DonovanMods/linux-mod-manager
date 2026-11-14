// collection_recipe_files.go reads what a source collection's recipe
// archive holds beside its manifest (#610 C5; design §6): the bundled/
// trees a `bundle` item installs from, and the patches/ files an item's
// `patches` apply. Both are VERIFIED from the 72-recipe survey: a bundle
// item's files are bundled/<source.fileExpression>/, and a patch is
// patches/<the mod's name>/<the patched path>.diff, BSDIFF40 every one.
//
// The recipe's cache entry holds them already, extracted under C2's rules
// (fetchCollectionRecipe: listed first, regular files only, capped, no
// links). This file reads them again as a stranger's, through an os.Root
// so nothing it opens can leave the entry, and turns them into what the
// converter carries: a bundle is a bundledItem - the fs-agnostic member
// list L3's container reader builds, so the same digest and the same code
// gate apply - and a patch is its sha256, kept in lmm's patch store at
// apply time.
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

const (
	// recipeBundledDir and recipePatchesDir are the recipe archive's two
	// trees beside collection.json.
	recipeBundledDir = "bundled"
	recipePatchesDir = "patches"
	// recipePatchExt ends every patch file's name.
	recipePatchExt = ".diff"
)

// recipeFiles is what one recipe's archive carries for its items.
type recipeFiles struct {
	// dir is the recipe's cache entry; sourceID and slug the collection's.
	dir      string
	sourceID string
	slug     string
	// bundles are the bundle items' trees, and patches the items' patch
	// files, both by the item's index in the manifest's mods.
	bundles map[int]recipeBundle
	patches map[int]map[string]recipePatch
}

// recipeBundle is one bundle item's tree, or why it could not be read.
type recipeBundle struct {
	item    *bundledItem
	problem string
}

// recipePatch is one patch file, or why it could not be used.
type recipePatch struct {
	// sha is the file's sha256 (hex), and file its path in the entry.
	sha  string
	file string
	// problem says why it is refused; format reports that the refusal is
	// its format (not BSDIFF40), which is never supported.
	problem string
	format  bool
}

// collectionID is the id a bundled item of this recipe's collection is
// kept under: "<source>-<slug>", its cache entry's directory. "" for nil.
func (r *recipeFiles) collectionID() string {
	if r == nil {
		return ""
	}
	return r.sourceID + "-" + r.slug
}

// bundleFor is the tree of the bundle item at index i, or nil and why not.
func (r *recipeFiles) bundleFor(i int) (*bundledItem, string) {
	if r == nil {
		return nil, "lmm did not read the collection's bundled files"
	}
	b, ok := r.bundles[i]
	if !ok {
		return nil, "the collection's archive holds no files for this bundled item"
	}
	return b.item, b.problem
}

// patchFor is the patch the item at index i records for path.
func (r *recipeFiles) patchFor(i int, path string) (recipePatch, bool) {
	if r == nil {
		return recipePatch{}, false
	}
	p, ok := r.patches[i][path]
	return p, ok
}

// patchFiles lists every usable patch file, by sha256.
func (r *recipeFiles) patchFiles() map[string]string {
	if r == nil {
		return nil
	}
	out := map[string]string{}
	for _, byPath := range r.patches {
		for _, p := range byPath {
			if p.problem == "" {
				out[p.sha] = p.file
			}
		}
	}
	return out
}

// bundle is the recipe's bundled items as a collectionBundle, by item name.
func (r *recipeFiles) bundle() *collectionBundle {
	if r == nil {
		return nil
	}
	b := &collectionBundle{items: map[string]*bundledItem{}}
	for _, rb := range r.bundles {
		if rb.item != nil {
			b.items[rb.item.name] = rb.item
		}
	}
	if len(b.items) == 0 {
		return nil
	}
	return b
}

// plainRecipeSegment reports whether name can name one directory of the
// recipe: a single segment with no separator, no "." or "..", and no
// control or formatting character. It is matched as written, never
// rewritten.
func plainRecipeSegment(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`) && !hasUnsafeRunes(name) &&
		len(name) <= maxLoadOrderName
}

// readRecipeFiles reads, from the recipe's cache entry dir, the bundled
// trees and patch files manifest's items name. A tree or a file that
// cannot be used is recorded with why, for the converter to refuse that
// item; only a failure to read the entry at all is an error.
func readRecipeFiles(dir, sourceID, slug string, manifest *source.CollectionManifest) (*recipeFiles, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	rf := &recipeFiles{dir: dir, sourceID: sourceID, slug: slug, bundles: map[int]recipeBundle{}, patches: map[int]map[string]recipePatch{}}
	names := map[string]bool{}
	for i, mod := range manifest.Mods {
		if mod.SourceType == "bundle" {
			rf.bundles[i] = readRecipeBundle(dir, root, mod.FileExpression, names)
		}
		if len(mod.Patches) == 0 {
			continue
		}
		byPath := map[string]recipePatch{}
		for p := range mod.Patches {
			byPath[p] = readRecipePatch(root, dir, mod.Name, p)
		}
		rf.patches[i] = byPath
	}
	return rf, nil
}

// readRecipeBundleAt is readRecipeBundle for the recipe entry dir alone.
func readRecipeBundleAt(dir, expr string) recipeBundle {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return recipeBundle{problem: err.Error()}
	}
	defer func() { _ = root.Close() }()
	return readRecipeBundle(dir, root, expr, map[string]bool{})
}

// readRecipeBundle reads bundled/<expr>/ of the recipe entry dir (open as
// root) into a bundledItem named by a slug of expr unique among names,
// hashed and checked for code: the same member rules, caps, digest and
// code gate an .lmmc's files/<item>/ gets. Each member opens through a
// root of its own, so the item outlives root.
func readRecipeBundle(dir string, root *os.Root, expr string, names map[string]bool) recipeBundle {
	if !plainRecipeSegment(expr) {
		return recipeBundle{problem: fmt.Sprintf("the collection names its bundled files %q, which is not a plain directory name", expr)}
	}
	item := &bundledItem{name: uniqueBundleName(expr, names), display: expr, paths: map[string]bool{}, dirs: map[string]bool{}}
	base := path.Join(recipeBundledDir, expr)
	info, err := root.Lstat(base)
	if err != nil || !info.IsDir() {
		return recipeBundle{problem: fmt.Sprintf("the collection's archive holds no %s/%s/ directory", recipeBundledDir, expr)}
	}
	err = fs.WalkDir(root.FS(), base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == base {
			return nil
		}
		rel := strings.TrimPrefix(p, base+"/")
		if err := checkBundleMemberName(rel); err != nil {
			return err
		}
		if d.IsDir() {
			return item.addDir(rel, p)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%w: %s is not a regular file", ErrInvalidBundle, p)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		if fi.Size() > maxBundleFileBytes {
			return fmt.Errorf("%w: %s is %d bytes, over the %d-byte limit for one bundled file", ErrInvalidBundle, p, fi.Size(), maxBundleFileBytes)
		}
		if err := item.addFile(rel, p); err != nil {
			return err
		}
		if len(item.members) >= maxCollectionMembers {
			return fmt.Errorf("%w: %s holds more than %d files", ErrInvalidBundle, base, maxCollectionMembers)
		}
		member := p
		item.members = append(item.members, bundleMember{rel: rel, size: fi.Size(), open: func() (io.ReadCloser, error) { return openInRoot(dir, member) }})
		item.bytes += fi.Size()
		if item.bytes > maxBundleBytes {
			return fmt.Errorf("%w: %s is over the %d-byte limit", ErrInvalidBundle, base, maxBundleBytes)
		}
		return nil
	})
	if err == nil && len(item.members) == 0 {
		err = fmt.Errorf("%w: %s holds no file", ErrInvalidBundle, base)
	}
	if err == nil {
		err = item.read()
	}
	if err != nil {
		return recipeBundle{problem: "its bundled files could not be read: " + domain.SanitizeLine(err.Error())}
	}
	return recipeBundle{item: item}
}

// uniqueBundleName is a bundled item's name for the recipe directory expr:
// a slug of it (validBundleItem), "bundled" when nothing of it survives,
// suffixed "-2", "-3", ... until it is not in names, which it joins.
func uniqueBundleName(expr string, names map[string]bool) string {
	base := strings.TrimPrefix(slugify(strings.TrimPrefix(expr, "Bundled - ")), "-")
	if !validBundleItem(base) {
		base = "bundled"
	}
	name := base
	for n := 2; names[name]; n++ {
		suffix := fmt.Sprintf("-%d", n)
		name = strings.TrimRight(base[:min(len(base), 64-len(suffix))], "-.") + suffix
	}
	names[name] = true
	return name
}

// readRecipePatch reads the patch the recipe carries for p, a path inside
// the mod named modName: patches/<modName>/<p>.diff.
func readRecipePatch(root *os.Root, dir, modName, p string) recipePatch {
	if !plainRecipeSegment(modName) {
		return recipePatch{problem: fmt.Sprintf("the mod's name (%q) cannot name its directory of patches", modName)}
	}
	if err := checkTreeInputPath(p); err != nil {
		return recipePatch{problem: err.Error()}
	}
	rel := path.Join(append([]string{recipePatchesDir, modName}, domain.SplitTreePath(p)...)...) + recipePatchExt
	info, err := root.Lstat(rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return recipePatch{problem: fmt.Sprintf("the collection's archive holds no patch for %q", p)}
		}
		return recipePatch{problem: err.Error()}
	}
	if !info.Mode().IsRegular() {
		return recipePatch{problem: fmt.Sprintf("the patch for %q is not a regular file", p)}
	}
	if info.Size() > maxPatchBytes {
		return recipePatch{problem: fmt.Sprintf("the patch for %q is %d bytes, over the %d-byte limit", p, info.Size(), maxPatchBytes)}
	}
	f, err := root.Open(rel)
	if err != nil {
		return recipePatch{problem: err.Error()}
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxPatchBytes+1))
	if err != nil {
		return recipePatch{problem: err.Error()}
	}
	if !isBSDIFF40(data) {
		return recipePatch{problem: fmt.Sprintf("the patch for %q is not a BSDIFF40 patch, the only format lmm applies", p), format: true}
	}
	sum := sha256.Sum256(data)
	return recipePatch{sha: hex.EncodeToString(sum[:]), file: filepath.Join(dir, filepath.FromSlash(rel))}
}

// convertFileList carries mod's exact file list onto it (#610), checked
// as an import checks one (checkTreeInputs): a path or md5 lmm could not
// use as written refuses the item, naming the recipe's key, and a list on
// anything but a source file is the recipe's to explain.
func convertFileList(it *convItem, mod source.ManifestMod) {
	list := make([]domain.FileListEntry, len(mod.FileList))
	for i, h := range mod.FileList {
		list[i] = domain.FileListEntry{Path: h.Path, MD5: h.MD5}
	}
	err := checkTreeInputs(domain.FileLists{"f": list}, nil)
	if err == nil && it.kind != source.CollectionItemModFile {
		err = errors.New("only a mod file can have one")
	}
	if err != nil {
		it.Features = append(it.Features, CollectionFeatureManifestKeyPrefix+"mods.hashes")
		it.Note = "its exact file list cannot be used: " + domain.SanitizeLine(err.Error())
		return
	}
	it.fileList, it.FileList = list, len(list)
}

// convertPatches carries mod's patches onto it (#610), each with the
// sha256 of the recipe's patch file: one the recipe does not carry, or
// carries in another format, refuses the item.
func convertPatches(in convertInput, it *convItem, index int, mod source.ManifestMod) {
	paths := make([]string, 0, len(mod.Patches))
	for p := range mod.Patches {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	refuse := func(feature, why string) {
		it.Features = appendUnique(it.Features, feature)
		if it.Note == "" {
			it.Note = domain.SanitizeLine(why)
		}
	}
	if it.kind != source.CollectionItemModFile {
		refuse(CollectionFeatureManifestKeyPrefix+"mods.patches", "only a mod file can be patched")
		return
	}
	if in.recipe == nil {
		refuse(CollectionFeaturePatches, "lmm did not read the collection's patches")
		return
	}
	var patches []domain.FilePatch
	for _, p := range paths {
		rp, ok := in.recipe.patchFor(index, p)
		switch {
		case !ok:
			refuse(CollectionFeatureManifestKeyPrefix+"mods.patches", fmt.Sprintf("the collection's archive holds no patch for %q", p))
			continue
		case rp.format:
			refuse(CollectionFeaturePatchFormat, rp.problem)
			continue
		case rp.problem != "":
			refuse(CollectionFeatureManifestKeyPrefix+"mods.patches", rp.problem)
			continue
		}
		patches = append(patches, domain.FilePatch{Path: p, CRC32: mod.Patches[p], SHA256: rp.sha})
	}
	if len(it.Features) > 0 {
		return
	}
	if err := checkTreeInputs(nil, domain.FilePatches{"f": patches}); err != nil {
		refuse(CollectionFeatureManifestKeyPrefix+"mods.patches", "its patches cannot be used: "+err.Error())
		return
	}
	it.patches, it.Patches = patches, len(patches)
}

// recipeFilesMarker is the empty file an extracted recipe entry holds when
// its bundled/ and patches/ trees were extracted with its manifest (#610).
// An entry C2 extracted holds collection.json alone, and is fetched again
// the first time a recipe needs either tree. No archive member can make
// it: only collection.json and the two trees are ever extracted.
const recipeFilesMarker = ".lmm-recipe-files"

// recipeHasFiles reports whether dir, a recipe's cache entry, was
// extracted with its bundled/ and patches/ trees.
func recipeHasFiles(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, recipeFilesMarker))
	return err == nil && info.Mode().IsRegular()
}

// recipeNeedsFiles reports whether manifest names a bundled item or a
// patch, which the recipe's trees carry.
func recipeNeedsFiles(m *source.CollectionManifest) bool {
	return slices.ContainsFunc(m.Mods, func(mod source.ManifestMod) bool {
		return mod.SourceType == "bundle" || len(mod.Patches) > 0
	})
}

// Caps on a recipe's bundled/ and patches/ trees, beside the per-file caps
// L3's container reader holds a bundled file to (maxBundleFileBytes) and
// a patch is held to (maxPatchBytes). The largest surveyed recipe carries
// 171 such members and 78 MB of bundled files.
const (
	maxRecipeFileMembers = maxCollectionMembers
	maxRecipePatchBytes  = 64 << 20
)

// recipeFilesBudget checks the members of a recipe's bundled/ and patches/
// trees, from `7z l -slt`, before anything is extracted: each a directory
// or a regular file - never a link of either kind - whose path is plain
// (checkBundleMemberName: relative, no empty, "." or ".." segment, no
// backslash, no control or formatting character), within the per-file
// caps, and all of them within the count and total caps.
type recipeFilesBudget struct {
	members       int
	bundled, diff int64
}

// check checks one listed member, ignoring anything outside the two trees.
func (b *recipeFilesBudget) check(fields map[string]string) error {
	p := fields["Path"]
	top, _, _ := strings.Cut(p, "/")
	if top != recipeBundledDir && top != recipePatchesDir {
		return nil
	}
	refuse := func(why string) error {
		return fmt.Errorf("%w: the recipe's %s %s", source.ErrInvalidManifest, domain.SanitizeLine(p), why)
	}
	if err := checkBundleMemberName(p); err != nil {
		return refuse("is not a plain path")
	}
	if b.members++; b.members > maxRecipeFileMembers {
		return fmt.Errorf("%w: the recipe's bundled/ and patches/ hold more than %d entries", source.ErrInvalidManifest, maxRecipeFileMembers)
	}
	mode := ""
	if attrs := strings.Fields(fields["Attributes"]); len(attrs) > 1 {
		mode = attrs[1]
	}
	if strings.HasPrefix(mode, "l") || fields["Symbolic Link"] != "" || fields["Hard Link"] != "" {
		return refuse("is a link")
	}
	if fields["Folder"] == "+" || strings.HasPrefix(mode, "d") || strings.Contains(fields["Attributes"], "D") {
		return nil
	}
	if mode != "" && !strings.HasPrefix(mode, "-") {
		return refuse("is not a regular file")
	}
	size, err := strconv.ParseInt(fields["Size"], 10, 64)
	if err != nil || size < 0 {
		return refuse("has no size")
	}
	if top == recipeBundledDir {
		if size > maxBundleFileBytes {
			return refuse(fmt.Sprintf("is %d bytes, over the %d-byte limit for one bundled file", size, maxBundleFileBytes))
		}
		if b.bundled += size; b.bundled > maxBundleBytes {
			return fmt.Errorf("%w: the recipe's bundled files are over the %d-byte limit", source.ErrInvalidManifest, maxBundleBytes)
		}
		return nil
	}
	if size > maxPatchBytes {
		return refuse(fmt.Sprintf("is %d bytes, over the %d-byte limit for one patch", size, maxPatchBytes))
	}
	if b.diff += size; b.diff > maxRecipePatchBytes {
		return fmt.Errorf("%w: the recipe's patches are over the %d-byte limit", source.ErrInvalidManifest, maxRecipePatchBytes)
	}
	return nil
}

// rootFile is a file opened through a root of its own, closed with it.
type rootFile struct {
	*os.File
	root *os.Root
}

// Close implements io.Closer: the file, then its root.
func (f rootFile) Close() error {
	err := f.File.Close()
	if rerr := f.root.Close(); err == nil {
		err = rerr
	}
	return err
}

// openInRoot opens name inside dir through an os.Root, so neither a link
// nor a ".." can take it out of dir.
func openInRoot(dir, name string) (io.ReadCloser, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	f, err := root.Open(name)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return rootFile{File: f, root: root}, nil
}
