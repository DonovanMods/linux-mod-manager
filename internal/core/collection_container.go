// collection_container.go is the `.lmmc` container of the lmm collection
// format (#621; docs/collection-format.md): a zip holding the version-1
// document as collection.yaml, with collection.yaml.sig reserved for a
// signature (#624) and files/ for an author's own bundled files (#623).
//
// The container is untrusted input. Members are read from memory, never
// extracted: only collection.yaml is inflated, through a limit, after its
// name passed the extractor's zip-slip and reserved-name guards and its
// declared sizes passed the caps. Every other member is named in a warning
// and left unread.
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
	// the container's three known names. Only the first is read today.
	collectionDocMember = "collection.yaml"
	collectionSigMember = "collection.yaml.sig"
	collectionFilesDir  = "files/"

	// maxCollectionDocument caps the version-1 document, inflated or
	// plain: a mod list of thousands of entries with long instructions is
	// well under it.
	maxCollectionDocument = 4 << 20
	// maxCollectionContainer caps the container file itself. L3 (#623)
	// raises it when the container may carry bundled files.
	maxCollectionContainer = 64 << 20
	// maxCollectionMembers caps the member count, so a container cannot
	// make the reader walk millions of headers.
	maxCollectionMembers = 4096
	// maxCollectionRatio caps a member's compression ratio once it is
	// past ratioFloor bytes - a zip bomb's signature - so it is refused
	// before it is inflated, whatever its header claims.
	maxCollectionRatio = 100
	ratioFloor         = 1 << 20
)

// zipMagic is the local-file-header signature every zip starts with: what
// an import sniffs to tell a container from a YAML document.
var zipMagic = []byte("PK\x03\x04")

// IsCollectionContainer reports whether data is a `.lmmc` container (a
// zip) rather than a YAML document, by its first bytes.
func IsCollectionContainer(data []byte) bool {
	return bytes.HasPrefix(data, zipMagic)
}

// writeCollectionContainer wraps a version-1 document in a `.lmmc`
// container: one deflated member, with no timestamp, so the same document
// always makes the same bytes.
func writeCollectionContainer(doc []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.CreateHeader(&zip.FileHeader{Name: collectionDocMember, Method: zip.Deflate})
	if err != nil {
		return nil, fmt.Errorf("writing collection container: %w", err)
	}
	if _, err := f.Write(doc); err != nil {
		return nil, fmt.Errorf("writing collection container: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("writing collection container: %w", err)
	}
	return buf.Bytes(), nil
}

// errBadContainer marks a container the reader refused.
var errBadContainer = errors.New("not a valid lmm collection container")

// readCollectionContainer returns the version-1 document a `.lmmc`
// container holds, with a warning for every member it left unread.
func readCollectionContainer(data []byte) (doc []byte, warnings []string, err error) {
	if len(data) > maxCollectionContainer {
		return nil, nil, fmt.Errorf("%w: it is %d bytes, over the %d-byte limit", errBadContainer, len(data), maxCollectionContainer)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errBadContainer, err)
	}
	if len(zr.File) > maxCollectionMembers {
		return nil, nil, fmt.Errorf("%w: it has %d members, over the %d limit", errBadContainer, len(zr.File), maxCollectionMembers)
	}

	guard := NewExtractor()
	var docFile *zip.File
	var sig, bundled bool
	var ignored []string
	for _, f := range zr.File {
		// The extractor's own guards, against a root that is never
		// written to: a member is refused for the name it claims, whether
		// or not this reader would have read it.
		if _, err := guard.sanitizePath("/lmmc", f.Name); err != nil {
			return nil, nil, fmt.Errorf("%w: %v", errBadContainer, err)
		}
		name := path.Clean(strings.ReplaceAll(f.Name, `\`, "/"))
		switch {
		case name == collectionDocMember:
			if docFile != nil {
				return nil, nil, fmt.Errorf("%w: it holds more than one %s", errBadContainer, collectionDocMember)
			}
			docFile = f
		case name == collectionSigMember:
			sig = true
		case strings.HasPrefix(name+"/", collectionFilesDir):
			bundled = true
		default:
			ignored = append(ignored, f.Name)
		}
	}
	if docFile == nil {
		return nil, nil, fmt.Errorf("%w: it holds no %s", errBadContainer, collectionDocMember)
	}

	if docFile.UncompressedSize64 > maxCollectionDocument {
		return nil, nil, fmt.Errorf("%w: %s is %d bytes, over the %d-byte limit", errBadContainer, collectionDocMember, docFile.UncompressedSize64, maxCollectionDocument)
	}
	if docFile.UncompressedSize64 > ratioFloor &&
		docFile.UncompressedSize64 > maxCollectionRatio*max(docFile.CompressedSize64, 1) {
		return nil, nil, fmt.Errorf("%w: %s's compression ratio is over %d:1", errBadContainer, collectionDocMember, maxCollectionRatio)
	}
	rc, err := docFile.Open()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", errBadContainer, err)
	}
	defer func() { _ = rc.Close() }()
	// The header's size is a claim; the limit is what holds.
	doc, err = io.ReadAll(io.LimitReader(rc, maxCollectionDocument+1))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: reading %s: %v", errBadContainer, collectionDocMember, err)
	}
	if len(doc) > maxCollectionDocument {
		return nil, nil, fmt.Errorf("%w: %s is over the %d-byte limit", errBadContainer, collectionDocMember, maxCollectionDocument)
	}

	if sig {
		warnings = append(warnings, "the container's signature ("+collectionSigMember+") was not checked: this version of lmm does not verify collection signatures")
	}
	if bundled {
		warnings = append(warnings, "the container's bundled files ("+collectionFilesDir+") were ignored: this version of lmm does not install bundled collection files")
	}
	for _, name := range ignored {
		warnings = append(warnings, fmt.Sprintf("the container's member %q was ignored: it is not part of the collection format", name))
	}
	return doc, warnings, nil
}
