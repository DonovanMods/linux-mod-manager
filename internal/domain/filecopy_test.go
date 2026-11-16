package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSameFile pins #662's copy rule: two files are copies only when every
// token one has and the other lacks is a version marker of its own.
func TestSameFile(t *testing.T) {
	f := func(name, fileName, version string) DownloadableFile {
		return DownloadableFile{Name: name, FileName: fileName, Version: version}
	}
	type pair struct {
		name string
		a, b DownloadableFile
	}
	copies := []pair{
		{"one file at two versions", f("", "SkyUI_5_2_SE.7z", "5.2"), f("", "SkyUI_5_1_SE.7z", "5.1")},
		{"NexusMods download names: mod id, version and timestamp", f("", "MyMod-1234-1-0-1600000000.7z", "1.0"), f("", "MyMod-1234-1-2-1610000000.7z", "1.2")},
		{"display names only", f("SkyUI 5.2", "", "5.2"), f("SkyUI 5.1", "", "5.1")},
		{"a v-prefixed version", f("", "Mod v1.2.zip", "1.2"), f("", "Mod-v2.zip", "2")},
		{"the word version", f("", "Mod version 3.zip", "3"), f("", "Mod v2.zip", "2")},
		{"a one-part version after v", f("Mod v1", "", "1"), f("Mod v2", "", "2")},
		{"a joined version after v", f("Mod v12", "", "1.2"), f("Mod v13", "", "1.3")},
		{"a joined version after a separate ver", f("Mod ver 12", "", "1.2"), f("Mod ver 13", "", "1.3")},
		{"case", f("", "SKYUI_5_2.zip", "5.2"), f("", "skyui-5-1.zip", "5.1")},
		{"a double extension", f("", "Mod-1.0.tar.gz", "1.0"), f("", "Mod-2.0.zip", "2.0")},
		{"a name with no version beside one with", f("", "main.esp", ""), f("", "main-2.0.esp", "2.0")},
		{"a version with a suffix", f("", "SkyUI_5_2SE.7z", "5.2SE"), f("", "SkyUI_5_1SE.7z", "5.1SE")},
		{"a NexusMods name carrying its version twice, no v", f("", "SkyUI_5_2_SE-12604-5-2-1573245213.7z", "5.2"), f("", "SkyUI_5_1_SE-12604-5-1-1580000000.7z", "5.1")},
	}
	for _, tc := range copies {
		t.Run("copies/"+tc.name, func(t *testing.T) {
			assert.True(t, SameFile(tc.a, tc.b))
			assert.True(t, SameFile(tc.b, tc.a), "symmetric")
		})
	}
	separate := []pair{
		{"two components", f("Core 1.2", "", "1.2"), f("Textures 1.0", "", "1.0")},
		{"two components by file name", f("Main", "Core-1234-1-2-1600000000.7z", "1.2"), f("Main", "Textures-1234-1-0-1600000000.7z", "1.0")},
		{"an edition", f("", "multi-regular.zip", "1.0"), f("", "multi-definitive.zip", "1.0")},
		{"resolutions", f("Textures 2K", "", "1.0"), f("Textures 4K", "", "1.1")},
		{"resolutions by file name", f("", "Textures_2K.zip", "1.0"), f("", "Textures_4K.zip", "1.1")},
		{"numbered parts", f("Part1", "", "1.0"), f("Part2", "", "1.1")},
		{"numbered parts with a space", f("Part 1", "", "1.0"), f("Part 2", "", "1.1")},
		{"an option by resolution", f("Option A 1080p", "", "1.0"), f("Option A 1440p", "", "1.1")},
		{"a word one lacks", f("", "mod1.zip", "1.5"), f("", "mod1-old.zip", "1.0")},
		{"nothing to tell them apart", f("", "", "1.0"), f("", "", "2.0")},
		// A lone number that is only one part of the file's version is no
		// version marker: the whole version must appear in the name.
		{"parts whose numbers are parts of their versions", f("Part 1", "", "1.2"), f("Part 2", "", "2.0")},
		{"chapters", f("Chapter 1", "", "1.0"), f("Chapter 2", "", "1.2")},
		{"discs", f("", "Disc_1.zip", "1.0"), f("", "Disc_2.zip", "2.0")},
		{"a name carrying only part of its version", f("Main 3", "", "3.0"), f("", "main-1.0.zip", "1.0")},
		// A version that is one token - a one-part version, or one written
		// joined ("12" for 1.2) - counts only after v, ver or version: a
		// bare number is as likely a part's ("Part 1", "Disc 2"). The cost:
		// SkyUI-5.zip at 5 and SkyUI-4.zip at 4 are separate too, which only
		// leaves a stale file installed, never drops a component.
		{"parts at one-part versions", f("Part 1", "", "1"), f("Part 2", "", "2")},
		{"discs at one-part versions", f("", "Disc_1.zip", "1"), f("", "Disc_2.zip", "2")},
		{"a bare number at a one-part version", f("Mod 2", "", "2"), f("Mod 3", "", "3")},
		{"a bare number spelling a joined version", f("Part 12", "", "1.2"), f("Part 13", "", "1.3")},
		{"the accepted cost: a bare one-part version", f("", "SkyUI-5.zip", "5"), f("", "SkyUI-4.zip", "4")},
	}
	for _, tc := range separate {
		t.Run("separate/"+tc.name, func(t *testing.T) {
			assert.False(t, SameFile(tc.a, tc.b))
			assert.False(t, SameFile(tc.b, tc.a), "symmetric")
		})
	}
}

func TestGroupCopies(t *testing.T) {
	files := []DownloadableFile{
		{FileName: "Core-1.2.zip", Version: "1.2"},
		{FileName: "Textures_2K-1.0.zip", Version: "1.0"},
		{FileName: "Core-1.1.zip", Version: "1.1"},
		{FileName: "Textures_4K-1.1.zip", Version: "1.1"},
		{FileName: "Textures_2K-0.9.zip", Version: "0.9"},
	}
	assert.Equal(t, []int{0, 1, 0, 2, 1}, GroupCopies(files))
}

// TestGroupCopies_NoChains: an item joins a group only when it relates to
// every member. b relates to a and to c, but a and c do not relate, so c
// starts a group of its own rather than joining a's through b. (SameFile
// compares canonical forms, so it never chains itself; the grouping does
// not rely on that.)
func TestGroupCopies_NoChains(t *testing.T) {
	related := map[[2]int]bool{{0, 1}: true, {1, 2}: true}
	same := func(i, j int) bool { return related[[2]int{min(i, j), max(i, j)}] }
	assert.Equal(t, []int{0, 0, 1}, groupBy(3, same))
}
