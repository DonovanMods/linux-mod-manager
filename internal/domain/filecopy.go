package domain

import (
	"path"
	"strings"
	"unicode"
)

// SameFile reports whether a and b are copies of one file at different
// versions (#662): the rule every one-version check applies to a mod's
// main files - only copies of one file must share a version.
//
// Each file's name (its file name without the extension, else its display
// name) is split into lower-case tokens on everything that is not a letter
// or a digit. The two are copies only when every token one has and the
// other lacks is a version marker of its own: a part of that file's own
// Version ("5", "2" of "5.2"), "v" with such a part ("v5"), the word
// "version" or "ver", or a long all-digit timestamp (a NexusMods download
// name's upload time). Anything else - a token that mixes letters and
// digits ("2k", "1080p", "part1"), a word, or a number its version does
// not explain ("Part 2" at version 1.1) - makes them separate files.
//
// So SkyUI_5_2_SE.7z (5.2) and SkyUI_5_1_SE.7z (5.1) are copies, as are two
// NexusMods downloads of one file (MyMod-1234-1-0-1600000000.7z at 1.0,
// MyMod-1234-1-2-1610000000.7z at 1.2); "Core 1.2" and "Textures 1.0", or
// "Textures 2K" and "Textures 4K", are separate. "Copies" is the strict
// answer on purpose: a false one drops a component a heal or an update
// should have kept, where a false "separate" only leaves a stale file
// installed, as before #662.
func SameFile(a, b DownloadableFile) bool {
	ta, tb := fileTokens(a), fileTokens(b)
	if len(ta) == 0 && len(tb) == 0 {
		return false // nothing names either: no ground to call them copies
	}
	onlyA, onlyB := tokenDifference(ta, tb)
	return allVersionMarkers(onlyA, a.Version) && allVersionMarkers(onlyB, b.Version)
}

// GroupCopies groups files into copies of one file (SameFile), each file
// joining the first group holding a copy of it, in list order. It returns
// each file's group index.
func GroupCopies(files []DownloadableFile) []int {
	group := make([]int, len(files))
	var heads [][]int // each group's member indexes
	for i := range files {
		group[i] = -1
		for g, members := range heads {
			for _, j := range members {
				if SameFile(files[i], files[j]) {
					group[i] = g
					break
				}
			}
			if group[i] >= 0 {
				break
			}
		}
		if group[i] < 0 {
			group[i] = len(heads)
			heads = append(heads, nil)
		}
		heads[group[i]] = append(heads[group[i]], i)
	}
	return group
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

// tokenDifference is the tokens of a that b lacks and those of b that a
// lacks, each counted as many times as it is unmatched.
func tokenDifference(a, b []string) (onlyA, onlyB []string) {
	left := make(map[string]int, len(b))
	for _, t := range b {
		left[t]++
	}
	for _, t := range a {
		if left[t] > 0 {
			left[t]--
			continue
		}
		onlyA = append(onlyA, t)
	}
	right := make(map[string]int, len(a))
	for _, t := range a {
		right[t]++
	}
	for _, t := range b {
		if right[t] > 0 {
			right[t]--
			continue
		}
		onlyB = append(onlyB, t)
	}
	return onlyA, onlyB
}

// allVersionMarkers reports whether every token is explained by version
// (see SameFile).
func allVersionMarkers(tokens []string, version string) bool {
	parts := make(map[string]bool)
	for _, p := range nameTokens(version) {
		parts[p] = true
	}
	for _, t := range tokens {
		switch {
		case t == "version" || t == "ver":
		case parts[t]:
		case strings.HasPrefix(t, "v") && parts[t[1:]]:
		case len(t) >= 9 && strings.IndexFunc(t, func(r rune) bool { return r < '0' || r > '9' }) < 0:
			// a timestamp
		default:
			return false
		}
	}
	return true
}
