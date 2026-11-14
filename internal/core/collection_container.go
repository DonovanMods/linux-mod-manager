// collection_container.go is the `.lmmc` container of the lmm collection
// format (#621; docs/collection-format.md): a zip holding the version-1
// document as collection.yaml, an author's own bundled files under files/
// (#623, collection_bundle.go), and collection.yaml.sig reserved for a
// signature (#624).
//
// The container is untrusted input. Members are read from memory, never
// extracted by this reader: collection.yaml is inflated through a limit,
// after its name passed the extractor's zip-slip and reserved-name guards
// and its declared sizes passed the caps; files/ is held to the bundle's
// own rules (collection_bundle.go). Every other member is named in a
// warning and left unread.
package core

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

const (
	// CollectionContainerExt is the `.lmmc` container's file extension.
	CollectionContainerExt = ".lmmc"

	// collectionDocMember, collectionSigMember and collectionFilesDir are
	// the container's three known names. The signature is not read yet.
	collectionDocMember = "collection.yaml"
	collectionSigMember = "collection.yaml.sig"
	collectionFilesDir  = "files/"
	// collectionPatchesDir holds a collection's binary patches (#610), one
	// member per patch, named by its sha256: patches/<sha256>.diff.
	collectionPatchesDir = "patches/"

	// maxCollectionDocument caps the version-1 document, inflated or
	// plain: a mod list of thousands of entries with long instructions is
	// well under it.
	maxCollectionDocument = 4 << 20
	// maxCollectionContainer caps the container file itself, bundled files
	// included (#623 keeps L1's cap: an older lmm refuses a larger
	// container outright, where it ignores a bundle it cannot install with
	// a warning).
	maxCollectionContainer = 64 << 20
	// maxCollectionMembers caps the member count, so a container cannot
	// make the reader walk millions of headers.
	maxCollectionMembers = 4096
	// maxCollectionRatio caps a member's compression ratio once it is
	// past ratioFloor bytes - a zip bomb's signature - so it is refused
	// before it is inflated, whatever its header claims.
	maxCollectionRatio = 100
	ratioFloor         = 1 << 20
	// maxIgnoredWarnings caps the members named in warnings.
	maxIgnoredWarnings = 20
)

// MaxCollectionFileBytes is the largest profile document file an import
// reads: a `.lmmc` container at most, the largest form a collection takes.
// A frontend reading a file to import (the web UI's upload) stops there.
const MaxCollectionFileBytes = maxCollectionContainer

// zipMagic is the local-file-header signature every zip starts with: what
// an import sniffs to tell a container from a YAML document.
var zipMagic = []byte("PK\x03\x04")

// IsCollectionContainer reports whether data is a `.lmmc` container (a
// zip) rather than a YAML document, by its first bytes.
func IsCollectionContainer(data []byte) bool {
	return bytes.HasPrefix(data, zipMagic)
}

// containerFile is one bundled file a container is written with: its
// member name (files/<item>/<path>) and its content.
type containerFile struct {
	name string
	open func() (io.ReadCloser, error)
}

// writeCollectionContainer wraps a version-1 document, and the bundled
// files it names (#623), in a `.lmmc` container: deflated members with no
// timestamp, so the same content always makes the same bytes. Bundled files
// are written as plain files (mode 0644) in the order given.
func writeCollectionContainer(doc []byte, files ...containerFile) ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.CreateHeader(&zip.FileHeader{Name: collectionDocMember, Method: zip.Deflate})
	if err != nil {
		return nil, fmt.Errorf("writing collection container: %w", err)
	}
	if _, err := f.Write(doc); err != nil {
		return nil, fmt.Errorf("writing collection container: %w", err)
	}
	for _, cf := range files {
		hdr := &zip.FileHeader{Name: cf.name, Method: zip.Deflate}
		hdr.SetMode(0o644)
		dst, err := w.CreateHeader(hdr)
		if err != nil {
			return nil, fmt.Errorf("writing collection container: %w", err)
		}
		src, err := cf.open()
		if err != nil {
			return nil, fmt.Errorf("writing collection container: %w", err)
		}
		_, err = io.Copy(dst, io.LimitReader(src, maxBundleFileBytes+1))
		_ = src.Close()
		if err != nil {
			return nil, fmt.Errorf("writing collection container: %s: %w", cf.name, err)
		}
		if buf.Len() > maxCollectionContainer {
			return nil, fmt.Errorf("%w: the container is over the %d-byte limit an import reads", ErrBundleTooLarge, maxCollectionContainer)
		}
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("writing collection container: %w", err)
	}
	if buf.Len() > maxCollectionContainer {
		return nil, fmt.Errorf("%w: the container is %d bytes, over the %d-byte limit an import reads", ErrBundleTooLarge, buf.Len(), maxCollectionContainer)
	}
	return buf.Bytes(), nil
}

// ErrBundleTooLarge refuses an export whose container an import would
// refuse for its size: the export keeps to the importer's limits.
var ErrBundleTooLarge = errors.New("the collection is too large to export")

// errBadContainer marks a container the reader refused.
var errBadContainer = errors.New("not a valid lmm collection container")

// readCollectionContainer returns the version-1 document a `.lmmc`
// container holds and its bundled files (#623; nil when it has none, each
// item read and its digest computed), with a warning for every member it
// left unread.
func readCollectionContainer(data []byte) (doc []byte, bundle *collectionBundle, warnings []string, err error) {
	return parseCollectionContainer(data, true)
}

// parseCollectionContainer is readCollectionContainer; readItems false
// leaves the bundle's items unread (each item's own read() hashes it), for
// a kept container a rebuild needs one item of. Every member is checked
// either way.
func parseCollectionContainer(data []byte, readItems bool) (doc []byte, bundle *collectionBundle, warnings []string, err error) {
	if len(data) > maxCollectionContainer {
		return nil, nil, nil, fmt.Errorf("%w: it is %d bytes, over the %d-byte limit", errBadContainer, len(data), maxCollectionContainer)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %v", errBadContainer, err)
	}
	if len(zr.File) > maxCollectionMembers {
		return nil, nil, nil, fmt.Errorf("%w: it has %d members, over the %d limit", errBadContainer, len(zr.File), maxCollectionMembers)
	}

	guard := NewExtractor()
	var docFile *zip.File
	var sig bool
	var ignored []string
	for _, f := range zr.File {
		// The extractor's own guards, against a root that is never
		// written to: a member is refused for the name it claims, whether
		// or not this reader would have read it.
		if _, err := guard.sanitizePath("/lmmc", f.Name); err != nil {
			return nil, nil, nil, fmt.Errorf("%w: %v", errBadContainer, err)
		}
		name := path.Clean(strings.ReplaceAll(f.Name, `\`, "/"))
		switch {
		case name == collectionDocMember:
			if docFile != nil {
				return nil, nil, nil, fmt.Errorf("%w: it holds more than one %s", errBadContainer, collectionDocMember)
			}
			docFile = f
		case name == collectionSigMember:
			sig = true
		case strings.HasPrefix(f.Name, collectionFilesDir):
			// #623: the author's own files, held to the bundle's rules - by
			// the member's own name, never a cleaned one.
			if bundle == nil {
				bundle = &collectionBundle{items: map[string]*bundledItem{}}
			}
			if err := bundle.addBundleMember(f); err != nil {
				return nil, nil, nil, fmt.Errorf("%w: %v", errBadContainer, err)
			}
		case strings.HasPrefix(name+"/", collectionFilesDir):
			// A files/ member spelled another way (./files/…, files\…):
			// refused rather than read under a name it does not have.
			return nil, nil, nil, fmt.Errorf("%w: %v: the member %q", errBadContainer, ErrInvalidBundle, f.Name)
		case strings.HasPrefix(f.Name, collectionPatchesDir):
			// #610: a patch, named by its own sha256, read whole (it is
			// small: capped) and checked against its name.
			if bundle == nil {
				bundle = &collectionBundle{items: map[string]*bundledItem{}}
			}
			if err := bundle.addPatchMember(f); err != nil {
				return nil, nil, nil, fmt.Errorf("%w: %v", errBadContainer, err)
			}
		case strings.HasPrefix(name+"/", collectionPatchesDir):
			return nil, nil, nil, fmt.Errorf("%w: the member %q is a patch spelled another way than patches/<sha256>.diff", errBadContainer, f.Name)
		default:
			ignored = append(ignored, f.Name)
		}
	}
	if docFile == nil {
		return nil, nil, nil, fmt.Errorf("%w: it holds no %s", errBadContainer, collectionDocMember)
	}

	if docFile.UncompressedSize64 > maxCollectionDocument {
		return nil, nil, nil, fmt.Errorf("%w: %s is %d bytes, over the %d-byte limit", errBadContainer, collectionDocMember, docFile.UncompressedSize64, maxCollectionDocument)
	}
	if docFile.UncompressedSize64 > ratioFloor &&
		docFile.UncompressedSize64 > maxCollectionRatio*max(docFile.CompressedSize64, 1) {
		return nil, nil, nil, fmt.Errorf("%w: %s's compression ratio is over %d:1", errBadContainer, collectionDocMember, maxCollectionRatio)
	}
	rc, err := docFile.Open()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %v", errBadContainer, err)
	}
	defer func() { _ = rc.Close() }()
	// The header's size is a claim; the limit is what holds.
	doc, err = io.ReadAll(io.LimitReader(rc, maxCollectionDocument+1))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: reading %s: %v", errBadContainer, collectionDocMember, err)
	}
	if len(doc) > maxCollectionDocument {
		return nil, nil, nil, fmt.Errorf("%w: %s is over the %d-byte limit", errBadContainer, collectionDocMember, maxCollectionDocument)
	}

	if sig {
		warnings = append(warnings, "the container's signature ("+collectionSigMember+") was not checked: this version of lmm does not verify collection signatures")
	}
	if bundle != nil {
		check := bundle.check
		if readItems {
			check = bundle.finish
		}
		if err := check(); err != nil {
			return nil, nil, nil, fmt.Errorf("%w: %v", errBadContainer, err)
		}
	}
	// Named one by one up to a point, then counted, so a container with
	// thousands of stray members does not bury the plan (#621 review N4).
	for i, name := range ignored {
		if i == maxIgnoredWarnings {
			warnings = append(warnings, fmt.Sprintf("%d more of the container's members were ignored: they are not part of the collection format", len(ignored)-i))
			break
		}
		warnings = append(warnings, fmt.Sprintf("the container's member %q was ignored: it is not part of the collection format", name))
	}
	return doc, bundle, warnings, nil
}
