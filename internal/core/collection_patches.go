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

// patchSource is where one patch a plan names comes from: its bytes (a
// container's), or a file (a recipe's), or neither when the store already
// keeps it.
type patchSource struct {
	data []byte
	file string
}

// collectionPatchSources finds every patch doc's pinned files name, by
// sha256: in the container's patches/ (bundle), the recipe (recipe), or
// game's patch store. A patch none of them has refuses the document: an
// import that cannot apply a patch must not install the mod unpatched.
func (s *Service) collectionPatchSources(game *domain.Game, doc *domain.CollectionDocument, bundle *collectionBundle, recipe *recipeFiles) (map[string]patchSource, error) {
	files := recipe.patchFiles()
	var out map[string]patchSource
	for _, item := range doc.Items {
		for _, in := range refTreeInputs(&item.Ref) {
			for _, p := range in.Patches {
				if _, done := out[p.SHA256]; done {
					continue
				}
				var src patchSource
				switch {
				case bundle != nil && bundle.patches[p.SHA256] != nil:
					src.data = bundle.patches[p.SHA256]
				case files[p.SHA256] != "":
					src.file = files[p.SHA256]
				default:
					if _, err := s.readPatch(game, p.SHA256); err != nil {
						return nil, fmt.Errorf("%w: %s:%s patches %q, and the document carries no patch %s (import the .lmmc container it came from): %v",
							ErrInvalidCollection, item.Ref.SourceID, item.Ref.ModID, p.Path, p.SHA256, err)
					}
				}
				if out == nil {
					out = map[string]patchSource{}
				}
				out[p.SHA256] = src
			}
		}
	}
	return out, nil
}

// keepPatches keeps every patch sources names in game's patch store: a
// container's bytes, or a recipe's file - read again, within the cap, and
// held to the sha256 it was planned with.
func (s *Service) keepPatches(game *domain.Game, sources map[string]patchSource) error {
	shas := make([]string, 0, len(sources))
	for sha := range sources {
		shas = append(shas, sha)
	}
	sort.Strings(shas)
	for _, sha := range shas {
		src := sources[sha]
		data := src.data
		switch {
		case data == nil && src.file == "":
			continue // the store keeps it already
		case data == nil:
			var err error
			if data, err = readCapped(src.file, maxPatchBytes); err != nil {
				return fmt.Errorf("keeping a patch: %w", err)
			}
		}
		if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != sha {
			return fmt.Errorf("%w: the patch %s changed after the import was planned; plan it again", ErrPatchNotKept, sha)
		}
		if _, err := s.keepPatch(game, data); err != nil {
			return err
		}
	}
	return nil
}

// patchCodeReason is why a patch to the file at p (a recorded path) is
// held back - it changes native code, a script the system runs or a
// loader's configuration (bundledCodeReason, by the file's name) - or ""
// when it is not.
func patchCodeReason(p string) string {
	segs := domain.SplitTreePath(p)
	return bundledCodeReason(path.Base(segs[len(segs)-1]), nil)
}

// restrictedPatchNames names a plan's patches to code, "<mod>: <path>",
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

// pinnedOnly is m without the entries for files ids does not name - inert
// in a document, which pins its files - or m itself when ids is empty.
func pinnedOnly[M ~map[string][]T, T any](m M, ids []string) M {
	if len(m) == 0 || len(ids) == 0 {
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
