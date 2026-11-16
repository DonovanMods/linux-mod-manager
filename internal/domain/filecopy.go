package domain

import (
	"path"
	"slices"
	"strings"
	"unicode"
)

// SameFile reports whether a and b are copies of one file at different
// versions (#662): the rule every one-version check applies to a mod's
// main files - only copies of one file must share a version.
//
// Each file's name (its file name without the extension, else its display
// name) is split into lower-case tokens on everything that is not a letter
// or a digit. From each, its own version markers are taken out: every run
// of tokens spelling the file's WHOLE Version ("5" "2" for 5.2, or "52"),
// with a "v", "version" or "ver" before it ("v1" "2", "v12"), and every
// long all-digit token (a NexusMods download name's upload timestamp). The
// two are copies only when what is left is the same, in any order. A token
// mixing letters and digits ("2k", "1080p", "part1"), a word, or a number
// that is only part of the file's version ("Part 2" at 2.0) stays, so it
// makes them separate.
//
// So SkyUI_5_2_SE.7z (5.2) and SkyUI_5_1_SE.7z (5.1) are copies, as are two
// NexusMods downloads of one file (MyMod-1234-1-0-1600000000.7z at 1.0,
// MyMod-1234-1-2-1610000000.7z at 1.2); "Core 1.2" and "Textures 1.0",
// "Textures 2K" and "Textures 4K", or "Disc_1" and "Disc_2" are separate.
// "Copies" is the strict answer on purpose: a false one drops a component
// a heal or an update should have kept, where a false "separate" only
// leaves a stale file installed, as before #662.
func SameFile(a, b DownloadableFile) bool {
	ka, kb := copyKey(a), copyKey(b)
	if len(fileTokens(a)) == 0 && len(fileTokens(b)) == 0 {
		return false // nothing names either: no ground to call them copies
	}
	return slices.Equal(ka, kb)
}

// GroupCopies groups files into copies of one file (SameFile): a file
// joins the first group of which it is a copy of EVERY member, else starts
// its own, in list order. It returns each file's group index.
func GroupCopies(files []DownloadableFile) []int {
	return groupBy(len(files), func(i, j int) bool { return SameFile(files[i], files[j]) })
}

// groupBy is GroupCopies over n items and a copy relation: an item joins a
// group only when it relates to every member, so a chain (a~b, b~c, a!~c)
// never puts two unrelated items in one group.
func groupBy(n int, same func(i, j int) bool) []int {
	group := make([]int, n)
	var members [][]int
	for i := range n {
		group[i] = -1
		for g, ms := range members {
			if !slices.ContainsFunc(ms, func(j int) bool { return !same(i, j) }) {
				group[i] = g
				break
			}
		}
		if group[i] < 0 {
			group[i] = len(members)
			members = append(members, nil)
		}
		members[group[i]] = append(members[group[i]], i)
	}
	return group
}

// copyKey is f's name tokens without its own version markers, sorted - the
// form SameFile compares.
func copyKey(f DownloadableFile) []string {
	tokens := fileTokens(f)
	marked := make([]bool, len(tokens))
	version := nameTokens(f.Version)
	joined := strings.Join(version, "")
	for i := 0; i < len(tokens); i++ {
		n := versionRunAt(tokens, i, version, joined)
		if n == 0 {
			continue
		}
		for k := i; k < i+n; k++ {
			marked[k] = true
		}
		if i > 0 && (tokens[i-1] == "v" || tokens[i-1] == "version" || tokens[i-1] == "ver") {
			marked[i-1] = true
		}
		i += n - 1
	}
	var key []string
	for i, t := range tokens {
		if marked[i] || isTimestamp(t) {
			continue
		}
		key = append(key, t)
	}
	slices.Sort(key)
	return key
}

// versionRunAt is the length of the run of tokens at i that spells the
// whole version - its parts one per token, or joined into one, either way
// optionally with "v" fused to the first - or 0.
func versionRunAt(tokens []string, i int, version []string, joined string) int {
	if len(version) == 0 {
		return 0
	}
	t := strings.TrimPrefix(tokens[i], "v")
	if t == joined || tokens[i] == joined {
		return 1
	}
	if i+len(version) > len(tokens) || (t != version[0] && tokens[i] != version[0]) {
		return 0
	}
	if slices.Equal(tokens[i+1:i+len(version)], version[1:]) {
		return len(version)
	}
	return 0
}

// isTimestamp reports whether t is a long all-digit token: an upload time
// in a NexusMods download name.
func isTimestamp(t string) bool {
	return len(t) >= 9 && strings.IndexFunc(t, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

// fileTokens is f's name as SameFile compares it.
func fileTokens(f DownloadableFile) []string {
	if t := nameTokens(stripExtensions(f.FileName)); len(t) > 0 {
		return t
	}
	return nameTokens(f.Name)
}

// stripExtensions is a file name's base without its extensions (".7z",
// ".tar.gz"); a dot followed by digits only is part of a version, kept.
func stripExtensions(name string) string {
	name = path.Base(strings.ReplaceAll(strings.TrimSpace(name), `\`, "/"))
	if name == "." || name == "/" {
		return ""
	}
	for {
		ext := path.Ext(name)
		if ext == "" || ext == name || len(ext) > 6 || !strings.ContainsFunc(ext, unicode.IsLetter) {
			return name
		}
		name = strings.TrimSuffix(name, ext)
	}
}

// nameTokens splits s into lower-case runs of letters and digits.
func nameTokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}
