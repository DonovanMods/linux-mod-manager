package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSameFile pins #662's copy rule: same stem, copies; another stem, a
// separate component.
func TestSameFile(t *testing.T) {
	f := func(name, fileName string) DownloadableFile { return DownloadableFile{Name: name, FileName: fileName} }
	copies := []struct {
		name string
		a, b DownloadableFile
	}{
		{"one file at two versions", f("", "SkyUI_5_2_SE.7z"), f("", "SkyUI_5_1_SE.7z")},
		{"NexusMods download names: mod id, version and timestamp", f("", "MyMod-1234-1-0-1600000000.7z"), f("", "MyMod-1234-1-2-1610000000.7z")},
		{"display names only", f("SkyUI 5.2", ""), f("SkyUI 5.1", "")},
		{"a v-prefixed version", f("", "Mod v1.2.zip"), f("", "Mod-v2.zip")},
		{"the word version", f("", "Mod version 3.zip"), f("", "Mod 2.zip")},
		{"case", f("", "SKYUI_5_2.zip"), f("", "skyui-5-1.zip")},
		{"a double extension", f("", "Mod-1.0.tar.gz"), f("", "Mod-2.0.zip")},
		{"nothing to tell them apart", f("", "1234-1-0.zip"), f("", "5678.zip")},
	}
	for _, tc := range copies {
		t.Run(tc.name, func(t *testing.T) {
			assert.True(t, SameFile(tc.a, tc.b), "%q vs %q", FileCopyKey(tc.a), FileCopyKey(tc.b))
		})
	}
	separate := []struct {
		name string
		a, b DownloadableFile
	}{
		{"two components", f("Core 1.2", ""), f("Textures 1.0", "")},
		{"two components by file name", f("Main", "Core-1234-1-2-1600000000.7z"), f("Main", "Textures-1234-1-0-1600000000.7z")},
		{"a word that starts with v is no version", f("", "Vanilla-1.0.zip"), f("", "anilla-1.0.zip")},
		{"an edition", f("", "multi-regular.zip"), f("", "multi-definitive.zip")},
	}
	for _, tc := range separate {
		t.Run(tc.name, func(t *testing.T) {
			assert.False(t, SameFile(tc.a, tc.b), "%q vs %q", FileCopyKey(tc.a), FileCopyKey(tc.b))
		})
	}
	assert.Equal(t, "skyuise", FileCopyKey(f("SkyUI", "SkyUI_5_2_SE.7z")), "the file name decides when there is one")
	assert.Equal(t, "core", FileCopyKey(f("Core 1.2", "")))
}
