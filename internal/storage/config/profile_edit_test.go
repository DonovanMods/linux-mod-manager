package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeProfileFile puts content at the path LoadProfile reads gameID's
// profileName from, and returns that path.
func writeProfileFile(t *testing.T, configDir, gameID, profileName, content string) string {
	t.Helper()
	path, err := config.ProfilePath(configDir, gameID, profileName)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

func ref(sourceID, modID string) domain.ModReference {
	return domain.ModReference{SourceID: sourceID, ModID: modID}
}

// TestMarkModsDisabled_HandEditedFileGainsOnlyTheMarker is fix round 2's R3:
// a hand-edited profile - comments, a flow-style entry, a `~/` hook path, a
// block-scalar override, two-space indentation - gains the marker and not
// one other changed byte. SaveProfile's round trip dropped every comment,
// expanded `~` into this machine's home directory and re-indented the lot.
func TestMarkModsDisabled_HandEditedFileGainsOnlyTheMarker(t *testing.T) {
	dir := t.TempDir()
	original := `# My carefully curated profile
name: default
game_id: tg
is_default: true
hooks:
  install:
    after_all: ~/bin/after.sh   # run my script
mods:
  # Alpha first
  - {source_id: local, mod_id: alpha, version: "1.0"}
  - source_id: local
    mod_id: beta   # the one I turned off
    version: "1.0"

  - source_id: local
    mod_id: gamma
overrides:
  Config/Game.ini: |
    [Section]
    key=1
`
	path := writeProfileFile(t, dir, "tg", "default", original)

	marked, err := config.MarkModsDisabled(path, []domain.ModReference{ref("local", "alpha"), ref("local", "beta")})
	require.NoError(t, err)
	assert.Equal(t, []domain.ModReference{ref("local", "alpha"), ref("local", "beta")}, marked)

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	want := strings.Replace(original,
		`version: "1.0"}`, `version: "1.0", disabled: true}`, 1)
	want = strings.Replace(want,
		"    mod_id: beta   # the one I turned off\n    version: \"1.0\"\n",
		"    mod_id: beta   # the one I turned off\n    version: \"1.0\"\n    disabled: true\n", 1)
	assert.Equal(t, want, string(got))

	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.Len(t, profile.Mods, 3)
	assert.True(t, profile.Mods[0].Disabled)
	assert.True(t, profile.Mods[1].Disabled)
	assert.False(t, profile.Mods[2].Disabled)
	assert.True(t, profile.IsDefault)
}

// TestMarkModsDisabled_MatchesSaveProfileOnAnLmmWrittenFile pins the other
// half of the contract: on a file lmm wrote itself, the in-place edit and a
// full SaveProfile agree byte for byte, so the next ordinary lmm write
// moves nothing the backfill touched.
func TestMarkModsDisabled_MatchesSaveProfileOnAnLmmWrittenFile(t *testing.T) {
	dir := t.TempDir()
	profile := &domain.Profile{
		Name: "default", GameID: "g1", IsDefault: true,
		Mods: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "1", Version: "1.0", FileIDs: []string{"10", "11"}},
			{SourceID: "nexusmods", ModID: "2", Version: "2.0", Locked: true},
			{SourceID: "curseforge", ModID: "3"},
		},
	}
	require.NoError(t, config.SaveProfile(dir, profile))
	path, err := config.ProfilePath(dir, "g1", "default")
	require.NoError(t, err)

	marked, err := config.MarkModsDisabled(path, []domain.ModReference{ref("nexusmods", "1"), ref("curseforge", "3")})
	require.NoError(t, err)
	assert.Len(t, marked, 2)
	edited, err := os.ReadFile(path)
	require.NoError(t, err)

	profile.Mods[0].Disabled = true
	profile.Mods[2].Disabled = true
	other := t.TempDir()
	require.NoError(t, config.SaveProfile(other, profile))
	saved, err := os.ReadFile(filepath.Join(other, "games", "g1", "profiles", "default.yaml"))
	require.NoError(t, err)
	assert.Equal(t, string(saved), string(edited))
}

// TestMarkModsDisabled_Shapes covers the layouts a hand edit can reach.
func TestMarkModsDisabled_Shapes(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		mods     []domain.ModReference
		want     string
		wantMark int
	}{
		{
			name:     "an explicit false becomes true in place",
			content:  "name: p\ngame_id: g\nmods:\n  - source_id: s\n    disabled: false # was on\n    mod_id: m\n",
			mods:     []domain.ModReference{ref("s", "m")},
			want:     "name: p\ngame_id: g\nmods:\n  - source_id: s\n    disabled: true # was on\n    mod_id: m\n",
			wantMark: 1,
		},
		{
			name:     "an entry whose mapping starts on the line after its dash",
			content:  "name: p\ngame_id: g\nmods:\n-\n   source_id: s\n   mod_id: m\n",
			mods:     []domain.ModReference{ref("s", "m")},
			want:     "name: p\ngame_id: g\nmods:\n-\n   source_id: s\n   mod_id: m\n   disabled: true\n",
			wantMark: 1,
		},
		{
			name:     "a nested flow list ending on a later line",
			content:  "name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: m\n    file_ids: [a,\n      b\n    ]\n  - source_id: s\n    mod_id: n\n",
			mods:     []domain.ModReference{ref("s", "m")},
			want:     "name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: m\n    file_ids: [a,\n      b\n    ]\n    disabled: true\n  - source_id: s\n    mod_id: n\n",
			wantMark: 1,
		},
		{
			name:     "CRLF line endings are kept",
			content:  "name: p\r\ngame_id: g\r\nmods:\r\n  - source_id: s\r\n    mod_id: m\r\n",
			mods:     []domain.ModReference{ref("s", "m")},
			want:     "name: p\r\ngame_id: g\r\nmods:\r\n  - source_id: s\r\n    mod_id: m\r\n    disabled: true\r\n",
			wantMark: 1,
		},
		{
			name:     "no trailing newline stays that way",
			content:  "name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: m",
			mods:     []domain.ModReference{ref("s", "m")},
			want:     "name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: m\n    disabled: true",
			wantMark: 1,
		},
		{
			name:     "a flow entry with a trailing comma, quoted braces and an apostrophe",
			content:  "name: p\ngame_id: g\nmods: [ {source_id: 'it''s}', mod_id: don't , } ]\n",
			mods:     []domain.ModReference{ref("it's}", "don't")},
			want:     "name: p\ngame_id: g\nmods: [ {source_id: 'it''s}', mod_id: don't , disabled: true } ]\n",
			wantMark: 1,
		},
		{
			name:     "wide characters before the entry on its line",
			content:  "name: p\ngame_id: g\nmods: [{source_id: \"ünï\", mod_id: m}]\n",
			mods:     []domain.ModReference{ref("ünï", "m")},
			want:     "name: p\ngame_id: g\nmods: [{source_id: \"ünï\", mod_id: m, disabled: true}]\n",
			wantMark: 1,
		},
		{
			name:     "only the first of two references to one mod",
			content:  "name: p\ngame_id: g\nmods:\n  - {source_id: s, mod_id: m}\n  - {source_id: s, mod_id: m}\n",
			mods:     []domain.ModReference{ref("s", "m")},
			want:     "name: p\ngame_id: g\nmods:\n  - {source_id: s, mod_id: m, disabled: true}\n  - {source_id: s, mod_id: m}\n",
			wantMark: 1,
		},
		// Fix round 3's F1: yaml.v3 counts a lone CR, NEL (U+0085), LS
		// (U+2028) and PS (U+2029) as line breaks as well as LF and CRLF.
		// The editor counted LF alone, so its line numbers fell behind
		// yaml's and it indexed past its own line table - a panic reached
		// from app.Open on every command.
		{
			name:     "CR-only line endings are kept",
			content:  "name: p\rgame_id: g\rmods:\r  - source_id: s\r    mod_id: m\r  - source_id: s\r    mod_id: n\r",
			mods:     []domain.ModReference{ref("s", "m"), ref("s", "n")},
			want:     "name: p\rgame_id: g\rmods:\r  - source_id: s\r    mod_id: m\r    disabled: true\r  - source_id: s\r    mod_id: n\r    disabled: true\r",
			wantMark: 2,
		},
		{
			name:     "doubled CRLF endings (a file converted twice) are kept",
			content:  "name: p\r\r\ngame_id: g\r\r\nmods:\r\r\n  - source_id: s\r\r\n    mod_id: m\r\r\n  - source_id: s\r\r\n    mod_id: n\r\r\n",
			mods:     []domain.ModReference{ref("s", "m"), ref("s", "n")},
			want:     "name: p\r\r\ngame_id: g\r\r\nmods:\r\r\n  - source_id: s\r\r\n    mod_id: m\r\r\n    disabled: true\r\r\n  - source_id: s\r\r\n    mod_id: n\r\r\n    disabled: true\r\r\n",
			wantMark: 2,
		},
		{
			name:     "doubled CRLF endings with no final line break",
			content:  "name: p\r\r\ngame_id: g\r\r\nmods:\r\r\n  - source_id: s\r\r\n    mod_id: m",
			mods:     []domain.ModReference{ref("s", "m")},
			want:     "name: p\r\r\ngame_id: g\r\r\nmods:\r\r\n  - source_id: s\r\r\n    mod_id: m\r\r\n    disabled: true",
			wantMark: 1,
		},
		{
			name:     "a NEL inside a quoted name, no final line break",
			content:  "name: \"p\u0085q\"\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: m",
			mods:     []domain.ModReference{ref("s", "m")},
			want:     "name: \"p\u0085q\"\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: m\n    disabled: true",
			wantMark: 1,
		},
		{
			name:     "a NEL and an LS inside a quoted name",
			content:  "name: \"p\u0085q\u2028r\"\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: m\n",
			mods:     []domain.ModReference{ref("s", "m")},
			want:     "name: \"p\u0085q\u2028r\"\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: m\n    disabled: true\n",
			wantMark: 1,
		},
		{
			name:     "a PS inside a quoted name, before a reference that is not the last",
			content:  "name: \"p\u2029q\"\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: m\n    version: \"1\"\n  - source_id: s\n    mod_id: n\n",
			mods:     []domain.ModReference{ref("s", "m")},
			want:     "name: \"p\u2029q\"\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: m\n    version: \"1\"\n    disabled: true\n  - source_id: s\n    mod_id: n\n",
			wantMark: 1,
		},
		{
			name:     "a flow entry broken by lone CRs",
			content:  "name: p\rgame_id: g\rmods:\r  - {source_id: s,\r     mod_id: m\r    }\r",
			mods:     []domain.ModReference{ref("s", "m")},
			want:     "name: p\rgame_id: g\rmods:\r  - {source_id: s,\r     mod_id: m, disabled: true\r    }\r",
			wantMark: 1,
		},
		{
			name:     "a byte-order mark before a flow document on line 1",
			content:  "\ufeff{name: p, game_id: g, mods: [{source_id: s, mod_id: m}]}\n",
			mods:     []domain.ModReference{ref("s", "m")},
			want:     "\ufeff{name: p, game_id: g, mods: [{source_id: s, mod_id: m, disabled: true}]}\n",
			wantMark: 1,
		},
		{
			name:    "an already-marked reference is not rewritten",
			content: "name: p\ngame_id: g\nmods:\n  - {source_id: s, mod_id: m, disabled: yes}\n  - {source_id: s, mod_id: n, disabled: true}\n",
			mods:    []domain.ModReference{ref("s", "n")},
			want:    "name: p\ngame_id: g\nmods:\n  - {source_id: s, mod_id: m, disabled: yes}\n  - {source_id: s, mod_id: n, disabled: true}\n",
		},
		{
			name:    "a mod the file does not list",
			content: "name: p\ngame_id: g\nmods:\n  - {source_id: s, mod_id: m}\n",
			mods:    []domain.ModReference{ref("s", "other")},
			want:    "name: p\ngame_id: g\nmods:\n  - {source_id: s, mod_id: m}\n",
		},
		{
			name:    "an empty mods list",
			content: "name: p\ngame_id: g\nmods: []\n",
			mods:    []domain.ModReference{ref("s", "m")},
			want:    "name: p\ngame_id: g\nmods: []\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeProfileFile(t, dir, "g", "p", tt.content)
			marked, err := config.MarkModsDisabled(path, tt.mods)
			require.NoError(t, err)
			assert.Len(t, marked, tt.wantMark)
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, tt.want, string(got))
		})
	}
}

// TestMarkModsDisabled_RefusesWhatItCannotEditInPlace: a shape the in-place
// edit cannot handle is an error and the file is left exactly as it was -
// never a fall-back to re-serializing the document.
func TestMarkModsDisabled_RefusesWhatItCannotEditInPlace(t *testing.T) {
	tests := map[string]string{
		"an alias stands in for the reference": "name: p\ngame_id: g\nbase: &base {source_id: s, mod_id: m}\nmods:\n  - *base\n",
		"a block scalar ends the reference":    "name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: m\n    version: |\n      1.0\n",
		"a comment sits before the brace":      "name: p\ngame_id: g\nmods:\n  - {source_id: s,\n     mod_id: m  # note\n    }\n",
		"an empty disabled value":              "name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: m\n    disabled: \n",
		// A line break other than LF or CR ending the reference's last line:
		// yaml.v3 reads NEL, LS and PS as breaks, YAML 1.2 and most editors
		// do not, so there is no line ending to give a marker line that
		// every reader of the file agrees on.
		"the reference's last line ends in a NEL":         "name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: m\u0085",
		"the reference's last line ends in an LS":         "name: p\ngame_id: g\nmods:\n  - source_id: s\n    mod_id: m\u2028  - source_id: s\n    mod_id: n\n",
		"the line before a last line with no break is PS": "name: p\ngame_id: g\nmods:\n  - source_id: s\u2029    mod_id: m",
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeProfileFile(t, dir, "g", "p", content)
			_, err := config.MarkModsDisabled(path, []domain.ModReference{ref("s", "m")})
			require.ErrorIs(t, err, config.ErrProfileLayoutUnsupported)
			got, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			assert.Equal(t, content, string(got))
		})
	}
}

// TestMarkModsDisabled_WritesThroughLinksAndKeepsTheMode: a dotfile
// manager's symlink survives (its target is what changes), a hard link is
// not forked from its other name, the file's mode is kept, and no temporary
// file is left behind to be listed as a profile.
func TestMarkModsDisabled_WritesThroughLinksAndKeepsTheMode(t *testing.T) {
	content := "name: p\ngame_id: g\nmods:\n  - {source_id: s, mod_id: m}\n"
	want := "name: p\ngame_id: g\nmods:\n  - {source_id: s, mod_id: m, disabled: true}\n"

	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		dotfiles := filepath.Join(t.TempDir(), "p.yaml")
		require.NoError(t, os.WriteFile(dotfiles, []byte(content), 0600))
		path, err := config.ProfilePath(dir, "g", "p")
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
		require.NoError(t, os.Symlink(dotfiles, path))

		_, err = config.MarkModsDisabled(path, []domain.ModReference{ref("s", "m")})
		require.NoError(t, err)

		info, err := os.Lstat(path)
		require.NoError(t, err)
		assert.Equal(t, os.ModeSymlink, info.Mode()&os.ModeSymlink, "the link itself must survive")
		got, err := os.ReadFile(dotfiles)
		require.NoError(t, err)
		assert.Equal(t, want, string(got))
		target, err := os.Stat(dotfiles)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0600), target.Mode().Perm())
		names, err := config.ListProfiles(dir, "g")
		require.NoError(t, err)
		assert.Equal(t, []string{"p"}, names)
		entries, err := os.ReadDir(filepath.Dir(dotfiles))
		require.NoError(t, err)
		assert.Len(t, entries, 1, "no temporary file left behind")
	})

	t.Run("hard link", func(t *testing.T) {
		dir := t.TempDir()
		path := writeProfileFile(t, dir, "g", "p", content)
		other := filepath.Join(t.TempDir(), "p.yaml")
		require.NoError(t, os.Link(path, other))

		_, err := config.MarkModsDisabled(path, []domain.ModReference{ref("s", "m")})
		require.NoError(t, err)

		got, err := os.ReadFile(other)
		require.NoError(t, err)
		assert.Equal(t, want, string(got), "the other name must see the same edit")
	})
}

func TestMarkModsDisabled_MissingFileIsProfileNotFound(t *testing.T) {
	_, err := config.MarkModsDisabled(filepath.Join(t.TempDir(), "nope.yaml"), []domain.ModReference{ref("s", "m")})
	require.ErrorIs(t, err, domain.ErrProfileNotFound)
}
