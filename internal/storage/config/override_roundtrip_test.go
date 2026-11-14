package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

// awkwardOverrides is override content every writer must round-trip byte
// for byte (#640): a block scalar whose first line starts with a tab was
// written as text the reader rejects.
var awkwardOverrides = map[string]string{
	"tab.ini":         "\tindented = 1\nnext = 2\n",
	"tab-only.ini":    "\t\n",
	"tab-later.ini":   "[S]\n\tkey = 1\n",
	"blank-tab.ini":   "\n\tkey = 1\n",
	"space.ini":       " leading = 1\nnext = 2\n",
	"crlf.ini":        "[General]\r\nkey = 1\r\n",
	"trailing.ini":    "key = 1   \nother = 2  ",
	"empty.ini":       "",
	"no-final-nl.ini": "first\nsecond",
	"colon.ini":       "key: value",
	"hash.ini":        "# a comment",
	"unicode.ini":     "名前 = ünïcödé ✓\nemoji = 🎮\n",
	"blank-lines.ini": "\n\nkey = 1\n\n\n",
	"tab-mixed.ini":   "\t \tx\n",
}

func awkwardBytes() map[string][]byte {
	out := make(map[string][]byte, len(awkwardOverrides))
	for k, v := range awkwardOverrides {
		out[k] = []byte(v)
	}
	return out
}

// requireOverrides compares one by one, so a failure names the content.
func requireOverrides(t *testing.T, got map[string]string) {
	t.Helper()
	for path, want := range awkwardOverrides {
		require.Contains(t, got, path)
		require.Equal(t, want, got[path], "override %s", path)
	}
	require.Len(t, got, len(awkwardOverrides))
}

func stringsOf(m map[string][]byte) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = string(v)
	}
	return out
}

func TestMarshalCollection_OverridesRoundTrip(t *testing.T) {
	doc := collectionDocFixture()
	doc.Overrides = awkwardOverrides
	data, err := config.MarshalCollection(doc)
	require.NoError(t, err)
	back, err := config.ParseCollection(data)
	require.NoError(t, err, "%s", data)
	requireOverrides(t, back.Overrides)
}

func TestExportProfile_OverridesRoundTrip(t *testing.T) {
	profile := &domain.Profile{Name: "p", GameID: "g", Overrides: awkwardBytes()}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)
	back, err := config.ImportProfile(data)
	require.NoError(t, err, "%s", data)
	requireOverrides(t, stringsOf(back.Overrides))
}

func TestSaveProfile_OverridesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	profile := &domain.Profile{Name: "p", GameID: "g", Overrides: awkwardBytes()}
	require.NoError(t, config.SaveProfile(dir, profile))
	back, err := config.LoadProfile(dir, "g", "p")
	require.NoError(t, err)
	requireOverrides(t, stringsOf(back.Overrides))
}

// The text-preserving save: a hand-written file (its comment proves the
// edit was made in place, not by a whole rewrite) gains the overrides.
func TestSaveProfile_InPlaceEditOverridesRoundTrip(t *testing.T) {
	for name, br := range map[string]string{"LF": "\n", "CRLF": "\r\n"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "games", "g", "profiles", "p.yaml")
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			text := strings.ReplaceAll("# hand-written\nname: p\ngame_id: g\nmods: []\noverrides:\n  old.ini: x\n", "\n", br)
			require.NoError(t, os.WriteFile(path, []byte(text), 0o644))

			profile, err := config.LoadProfile(dir, "g", "p")
			require.NoError(t, err)
			profile.Overrides = awkwardBytes()
			report, err := config.SaveProfileReporting(dir, profile)
			require.NoError(t, err)
			require.Empty(t, report.Backup, "the in-place edit was refused")

			data, err := os.ReadFile(path)
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(string(data), "# hand-written"+br), "%q", data)
			back, err := config.LoadProfile(dir, "g", "p")
			require.NoError(t, err)
			requireOverrides(t, stringsOf(back.Overrides))
		})
	}
}

// Content that already round-tripped keeps the form it was always written
// in, so an export of it is byte-identical to one made before #640; only
// content the usual form would corrupt is double-quoted.
func TestExportProfile_OverrideFormIsUnchangedUnlessItWouldNotRoundTrip(t *testing.T) {
	profile := &domain.Profile{Name: "p", GameID: "g", Overrides: map[string][]byte{
		"a.ini": []byte("[S]\nkey = 1\n"),
		"b.ini": []byte(" lead\n"),
		"c.ini": []byte("\tlead\n"),
	}}
	data, err := config.ExportProfile(profile)
	require.NoError(t, err)
	require.Equal(t, "name: p\ngame_id: g\nmods: []\noverrides:\n"+
		"    a.ini: |\n        [S]\n        key = 1\n"+
		"    b.ini: |4\n         lead\n"+
		"    c.ini: \"\\tlead\\n\"\n", string(data))
}
