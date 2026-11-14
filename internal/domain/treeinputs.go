package domain

import (
	"maps"
	"slices"
	"strings"
)

// FileListEntry is one file of an exact file list (#610): a path inside
// the mod's tree, as the curator's tool wrote it (Vortex writes
// backslashes, and the path is never rewritten), and the md5 its content
// must have.
type FileListEntry struct {
	Path string `yaml:"path" json:"path"`
	MD5  string `yaml:"md5" json:"md5"`
}

// FileLists are a mod's exact file lists (#610, Vortex's "replicate"
// install mode), keyed by the file id they describe: installing that file
// extracts only the listed paths, and each must have its md5. A list keyed
// by a file the reference does not pin is inert - it describes a file the
// mod is not installed from (an update moved it), and comes back into
// force if a rollback pins that file again. A reference that pins no file
// installs by none.
type FileLists map[string][]FileListEntry

// IsZero reports that no file has a list.
func (f FileLists) IsZero() bool {
	for _, l := range f {
		if len(l) > 0 {
			return false
		}
	}
	return true
}

// Clone is a copy of f whose map and lists can be changed without changing
// f's; nil for a zero f.
func (f FileLists) Clone() FileLists {
	if f.IsZero() {
		return nil
	}
	out := make(FileLists, len(f))
	for id, l := range f {
		if len(l) > 0 {
			out[id] = slices.Clone(l)
		}
	}
	return out
}

// Equal reports whether f and o hold the same lists for the same files.
func (f FileLists) Equal(o FileLists) bool {
	return maps.EqualFunc(f.Clone(), o.Clone(), slices.Equal)
}

// FilePatch is one binary patch of a mod's file (#610): the file's path
// inside the mod's tree (never rewritten), the CRC32 of the file the patch
// was made against - 8 hex digits, as the curator's tool wrote it - and
// the sha256 of the patch itself, which names it in lmm's patch store. The
// patch is applied only to a file whose CRC32 is the baseline's; any other
// file refuses the install.
type FilePatch struct {
	Path   string `yaml:"path" json:"path"`
	CRC32  string `yaml:"crc32" json:"crc32"`
	SHA256 string `yaml:"sha256" json:"sha256"`
}

// FilePatches are a mod's binary patches (#610), keyed by the file id
// whose tree they patch; inert for a file the reference does not pin, as
// FileLists are.
type FilePatches map[string][]FilePatch

// IsZero reports that no file has a patch.
func (f FilePatches) IsZero() bool {
	for _, l := range f {
		if len(l) > 0 {
			return false
		}
	}
	return true
}

// Clone is a copy of f whose map and lists can be changed without changing
// f's; nil for a zero f.
func (f FilePatches) Clone() FilePatches {
	if f.IsZero() {
		return nil
	}
	out := make(FilePatches, len(f))
	for id, l := range f {
		if len(l) > 0 {
			out[id] = slices.Clone(l)
		}
	}
	return out
}

// Equal reports whether f and o hold the same patches for the same files.
func (f FilePatches) Equal(o FilePatches) bool {
	return maps.EqualFunc(f.Clone(), o.Clone(), slices.Equal)
}

// Count is how many patches f holds across its files.
func (f FilePatches) Count() int {
	n := 0
	for _, l := range f {
		n += len(l)
	}
	return n
}

// TreeInputs are what a reference records that changes the tree a file
// installs to beyond its archive and its FOMOD choices (#610): an exact
// file list and binary patches, one file's.
type TreeInputs struct {
	FileList []FileListEntry
	Patches  []FilePatch
}

// IsZero reports that the file installs its archive's tree as it is.
func (t TreeInputs) IsZero() bool { return len(t.FileList) == 0 && len(t.Patches) == 0 }

// TreeInputsFor is the reference's file list and patches for one file.
func (r ModReference) TreeInputsFor(fileID string) TreeInputs {
	return TreeInputs{FileList: r.FileList[fileID], Patches: r.Patches[fileID]}
}

// HasTreeInputs reports whether any file the reference pins has a file
// list or patches: one for a file it does not pin applies to nothing.
func (r ModReference) HasTreeInputs() bool {
	return slices.ContainsFunc(r.FileIDs, func(id string) bool { return !r.TreeInputsFor(id).IsZero() })
}

// SplitTreePath splits a tree path as a file list or a patch records it -
// with '/' or '\' between its segments - into its segments, empty ones
// included. It does not judge them: the importer refuses a path with an
// empty, "." or ".." segment before anything uses one.
func SplitTreePath(p string) []string {
	return strings.Split(strings.ReplaceAll(p, `\`, "/"), "/")
}
