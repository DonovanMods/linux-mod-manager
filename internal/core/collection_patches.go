// collection_patches.go is the import half of a collection's binary
// patches (#610 C5): finding every patch a document names - in a `.lmmc`
// container's patches/, a converted source collection's recipe, or this
// machine's patch store - keeping each in the store at apply, before
// anything is acquired, and the code gate that holds back a patch to code.
package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ErrCodePatch refuses a collection import whose patches change code - a
// native library, an executable, a script the system runs or a loader's
// configuration - without ProfileImportOptions.AllowAnyOverrides. It is the
// caller's to answer (`--allow-any-overrides`).
var ErrCodePatch = errors.New("the collection patches code")

// collectionPatchSources finds every patch doc's pinned files name, by
// sha256, and returns the ones this import brings: a container's
// patches/ (bundle) or a converted collection's recipe (recipe), each
// written into game's patch store now - content-addressed, checked
// against its sha256 and its format - so the plan holds no patch's bytes
// (#610 review B2). A patch kept there is inert until an import admits it
// for a file (patch_admission.go). A patch neither brings must already be
// in the store, else the document is refused: an import that cannot apply
// a patch must not install the mod unpatched.
func (s *Service) collectionPatchSources(game *domain.Game, doc *domain.CollectionDocument, bundle *collectionBundle, recipe *recipeFiles) (map[string]bool, error) {
	files := recipe.patchFiles()
	var out map[string]bool
	for _, item := range doc.Items {
		for _, in := range refTreeInputs(&item.Ref) {
			for _, p := range in.Patches {
				if _, done := out[p.SHA256]; done {
					continue
				}
				var data []byte
				switch {
				case bundle != nil && bundle.patches[p.SHA256] != nil:
					data = bundle.patches[p.SHA256]
				case files[p.SHA256] != "":
					var err error
					if data, err = readCapped(files[p.SHA256], maxPatchBytes); err != nil {
						return nil, fmt.Errorf("reading the collection's patch for %q: %w", p.Path, err)
					}
				default:
					if _, err := s.readPatch(game, p.SHA256); err != nil {
						return nil, fmt.Errorf("%w: %s:%s patches %q, and the document carries no patch %s (import the .lmmc container it came from): %v",
							ErrInvalidCollection, item.Ref.SourceID, item.Ref.ModID, p.Path, p.SHA256, err)
					}
				}
				if out == nil {
					out = map[string]bool{}
				}
				out[p.SHA256] = data != nil
				if data == nil {
					continue
				}
				if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != p.SHA256 || !isBSDIFF40(data) {
					return nil, fmt.Errorf("%w: the collection's patch for %q is not the BSDIFF40 patch %s it names", ErrInvalidCollection, p.Path, p.SHA256)
				}
				if _, err := s.keepPatch(game, data); err != nil {
					return nil, err
				}
			}
		}
	}
	if bundle != nil {
		bundle.patches = nil // kept: the plan does not hold them
	}
	return out, nil
}

// patchCodeReason is why a patch to the file at p (a recorded path) is
// held back - it changes native code, a script the system runs or a
// loader's configuration (bundledCodeReason, by the file's name) - or ""
// when it is not.
func patchCodeReason(p string) string {
	segs := domain.SplitTreePath(p)
	return bundledCodeReason(path.Base(segs[len(segs)-1]), nil)
}

// restrictedPatchNames names a collection plan's restricted patches, "<mod>: <path>",
// at most maxIgnoredWarnings of them, then a count.
func restrictedPatchNames(c *CollectionImport) []string {
	var out []string
	n := 0
	for _, it := range c.Items {
		for _, p := range it.Patches {
			if p.Restricted == "" {
				continue
			}
			if n++; n <= maxIgnoredWarnings {
				out = append(out, firstNonEmpty(it.Name, it.ModID)+": "+p.Path)
			}
		}
	}
	if n > maxIgnoredWarnings {
		out = append(out, fmt.Sprintf("and %d more", n-maxIgnoredWarnings))
	}
	return out
}

// pinnedOnly is m without the entries for files ids does not name, which
// are inert: a file list or a patch applies only to a file the mod pins.
func pinnedOnly[M ~map[string][]T, T any](m M, ids []string) M {
	if len(m) == 0 {
		return m
	}
	var out M
	for id, l := range m {
		if len(l) > 0 && slices.Contains(ids, id) {
			if out == nil {
				out = M{}
			}
			out[id] = l
		}
	}
	return out
}

// keepPendingBundle keeps b's bundled items for the lmm-collection source,
// admitting each the import lets in: a container is kept whole with its
// index; a converted source collection's recipe (#610) keeps its bundled/
// trees where they are, with an index beside the recipe's entry.
func (s *Service) keepPendingBundle(game *domain.Game, b *pendingBundle, anyOverrides bool) error {
	if b.recipeDir == "" {
		return s.keepCollectionContainer(game, b.collectionID, b.data, b.bundle, b.names, b.admitted(anyOverrides))
	}
	return s.keepRecipeBundle(game, b, b.admitted(anyOverrides))
}

// exportPatches reads every patch doc's pinned files name from game's
// patch store, by sha256, for an export's container; missing names the
// ones the store does not keep (the profile's collection was imported on
// another machine), which a recipient cannot apply.
func (s *Service) exportPatches(game *domain.Game, doc *domain.CollectionDocument) (patches map[string][]byte, missing []string) {
	for _, item := range doc.Items {
		for _, in := range refTreeInputs(&item.Ref) {
			for _, p := range in.Patches {
				if _, done := patches[p.SHA256]; done || slices.Contains(missing, p.Path) {
					continue
				}
				data, err := s.readPatch(game, p.SHA256)
				if err != nil {
					missing = append(missing, firstNonEmpty(item.Name, item.Ref.ModID)+": "+p.Path)
					continue
				}
				if patches == nil {
					patches = map[string][]byte{}
				}
				patches[p.SHA256] = data
			}
		}
	}
	return patches, missing
}

// patchContainerFiles lists patches as container members, in name order.
func patchContainerFiles(patches map[string][]byte) []containerFile {
	shas := make([]string, 0, len(patches))
	for sha := range patches {
		shas = append(shas, sha)
	}
	sort.Strings(shas)
	out := make([]containerFile, len(shas))
	for i, sha := range shas {
		data := patches[sha]
		out[i] = containerFile{name: collectionPatchesDir + sha + recipePatchExt, open: func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(data)), nil
		}}
	}
	return out
}

// patchNotes is what an export with patches tells its author.
func patchNotes(kept int, missing []string) []string {
	var notes []string
	if kept > 0 {
		notes = append(notes, fmt.Sprintf("The collection patches its mods' files with %d binary patch(es), which the .lmmc container carries; a plain YAML document cannot, so a recipient needs the container.", kept))
	}
	if len(missing) > 0 {
		notes = append(notes, fmt.Sprintf("%d patch(es) are not kept on this machine (%s), so a recipient cannot install the mods they patch: import the collection they came from here first.",
			len(missing), strings.Join(missing, ", ")))
	}
	return notes
}
