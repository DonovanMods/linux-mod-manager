package domain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeLine(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"clean ascii", "Better Inventory 1.2", "Better Inventory 1.2"},
		{"empty", "", ""},
		{"clear screen CSI", "Evil\x1b[2JMod", "Evil[2JMod"},
		{"cursor up CSI", "a\x1b[1Ab", "a[1Ab"},
		{"OSC 0 title BEL", "x\x1b]0;pwned\x07y", "x]0;pwnedy"},
		{"OSC 8 link ST", "\x1b]8;;https://evil.example\x1b\\click\x1b]8;;\x1b\\", "]8;;https://evil.example\\click]8;;\\"},
		{"C1 CSI", "a\u009b2Jb", "a2Jb"},
		{"C1 OSC", "a\u009d0;t\u009cb", "a0;tb"},
		{"DEL", "a\x7fb", "ab"},
		{"NUL and BEL", "a\x00b\x07c", "abc"},
		{"bidi override", "user\u202Egnp.exe", "usergnp.exe"},
		{"bidi isolates", "\u2066a\u2067b\u2068c\u2069", "abc"},
		{"bidi embeddings", "\u202Aa\u202Bb\u202Cc\u202Dd", "abcd"},
		{"tab newline cr to space", "a\tb\nc\rd", "a b c d"},
		{"crlf is one space", "a\r\nb", "a b"},
		{"invalid utf8", "a\xffb", "a�b"},
		{"cjk emoji accents kept", "日本語 🎮 café Ñandú", "日本語 🎮 café Ñandú"},
		{"zero-width joiner kept", "👩\u200D💻", "👩\u200D💻"},
		{"LRM kept", "a\u200Eb", "a\u200Eb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SanitizeLine(tt.in))
		})
	}
}

func TestSanitizeText(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"keeps newline and tab", "line one\n\tline two", "line one\n\tline two"},
		{"crlf to lf", "a\r\nb\r\n", "a\nb\n"},
		{"lone cr to lf", "a\rb", "a\nb"},
		{"strips ESC", "ok\x1b[2J\x1b]0;title\x07done", "ok[2J]0;titledone"},
		{"strips C1 CSI", "a\u009bb", "ab"},
		{"strips bidi", "a\u202Eb\u2066c", "abc"},
		{"invalid utf8", "\xc3\x28", "�("},
		{"non-ascii kept", "Привет\n世界 ✓", "Привет\n世界 ✓"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SanitizeText(tt.in))
		})
	}
}

func TestSanitizeCleanInputDoesNotAllocate(t *testing.T) {
	s := "Better Inventory 日本語 🎮"
	allocs := testing.AllocsPerRun(100, func() { _ = SanitizeLine(s) })
	assert.Zero(t, allocs)
}

func TestModSanitizeSourceText(t *testing.T) {
	const evil = "\x1b[2J\u009b\u202E"
	m := &Mod{
		ID: "id\x1b", SourceID: "src", GameID: "g",
		Name: "N" + evil, Version: "1.0" + evil, Author: "A" + evil, AuthorName: "AN" + evil,
		Summary: "S\n" + evil, Description: "D\r\n" + evil, Category: "4" + evil,
		CategoryName: "C" + evil, PictureURL: "P" + evil, SourceURL: "U" + evil,
		DisplayVersion: "DV" + evil,
		Dependencies:   []ModReference{{SourceID: "s", ModID: "m", Version: "2" + evil}},
	}
	m.SanitizeSourceText()
	assert.Equal(t, "N[2J", m.Name)
	assert.Equal(t, "1.0[2J", m.Version)
	assert.Equal(t, "A[2J", m.Author)
	assert.Equal(t, "AN[2J", m.AuthorName)
	assert.Equal(t, "S [2J", m.Summary)
	assert.Equal(t, "D\n[2J", m.Description)
	assert.Equal(t, "4[2J", m.Category)
	assert.Equal(t, "C[2J", m.CategoryName)
	assert.Equal(t, "P[2J", m.PictureURL)
	assert.Equal(t, "U[2J", m.SourceURL)
	assert.Equal(t, "DV[2J", m.DisplayVersion)
	assert.Equal(t, "2[2J", m.Dependencies[0].Version)
	assert.Equal(t, "id\x1b", m.ID, "ids are keys, not display text")

	var nilMod *Mod
	nilMod.SanitizeSourceText()
}

func TestInstalledFileUpdateSanitizeSourceText(t *testing.T) {
	im := &InstalledMod{Mod: Mod{Name: "a\x1b"}, PreviousVersion: "1\x1b", SkippedVersion: "2\x1b"}
	im.SanitizeSourceText()
	assert.Equal(t, "a", im.Name)
	assert.Equal(t, "1", im.PreviousVersion)
	assert.Equal(t, "2", im.SkippedVersion)

	f := &DownloadableFile{ID: "x\x1b", Name: "n\x1b", FileName: "f\x1b.zip", Version: "v\x1b", Category: "MAIN\x1b", Description: "d\n\x1b"}
	f.SanitizeSourceText()
	assert.Equal(t, DownloadableFile{ID: "x\x1b", Name: "n", FileName: "f.zip", Version: "v", Category: "MAIN", Description: "d\n"}, *f)

	u := &Update{InstalledMod: InstalledMod{Mod: Mod{Name: "m\u009b"}}, NewVersion: "2\x1b", Changelog: "c\r\n\x1b"}
	u.SanitizeSourceText()
	assert.Equal(t, "m", u.InstalledMod.Name)
	assert.Equal(t, "2", u.NewVersion)
	assert.Equal(t, "c\n", u.Changelog)

	var nilIM *InstalledMod
	nilIM.SanitizeSourceText()
	var nilF *DownloadableFile
	nilF.SanitizeSourceText()
	var nilU *Update
	nilU.SanitizeSourceText()
}
