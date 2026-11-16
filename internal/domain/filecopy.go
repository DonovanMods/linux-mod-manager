package domain

import (
	"path"
	"regexp"
	"strings"
	"unicode"
)

// versionWord is a version marker that names no component: "v" before a
// digit ("v1.2"), and the words "version" and "ver".
var versionWord = regexp.MustCompile(`(?i)(^|[^\p{L}])(version|ver|v)(\d|[^\p{L}]|$)`)

// FileCopyKey is the part of a file's name that says which file it is,
// whatever its version (#662): its file name - else its display name - in
// lower case, without its extension, version markers ("v1.2", "version"),
// digits and separators. Two files with the same key are copies of one
// file at different versions (SkyUI_5_2_SE.7z and SkyUI_5_1_SE.7z, or a
// NexusMods download name carrying the mod id, the version and an upload
// timestamp); different keys are separate components an author versions
// on their own ("Core 1.2" beside "Textures 1.0"). A file whose name is
// all digits and separators has an empty key, which every other empty key
// matches: with nothing to tell them apart, two such files are treated as
// copies.
//
// SameFile is the one decision built on it.
func FileCopyKey(f DownloadableFile) string {
	if k := nameStem(f.FileName, true); k != "" {
		return k
	}
	return nameStem(f.Name, false)
}

// SameFile reports whether a and b are copies of one file (#662): the
// rule every one-version check applies to a mod's main files - only
// copies of the same file must share a version. See FileCopyKey.
func SameFile(a, b DownloadableFile) bool {
	return FileCopyKey(a) == FileCopyKey(b)
}

// nameStem is FileCopyKey for one name; isFile strips a file name's
// extension first.
func nameStem(name string, isFile bool) string {
	name = strings.TrimSpace(name)
	if isFile {
		name = path.Base(strings.ReplaceAll(name, `\`, "/"))
		for {
			ext := path.Ext(name)
			if ext == "" || ext == name || !strings.ContainsFunc(ext, unicode.IsLetter) || len(ext) > 6 {
				break
			}
			name = strings.TrimSuffix(name, ext)
		}
	}
	name = versionWord.ReplaceAllString(name, "$1 $3")
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
