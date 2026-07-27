package steam

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVDF_LibraryFolders(t *testing.T) {
	vdf := `
"libraryfolders"
{
	"0"
	{
		"path"		"/home/user/.steam/steam"
		"label"		""
	}
	"1"
	{
		"path"		"/mnt/games/steam"
		"label"		"Games"
	}
}
`
	root, err := ParseVDF(strings.NewReader(vdf))
	require.NoError(t, err)
	require.NotNil(t, root)
	lf, ok := root["libraryfolders"].(VDFMap)
	require.True(t, ok)
	// Keys in libraryfolders are "0", "1", ...; values are nested blocks with "path"
	for _, k := range []string{"0", "1"} {
		entry := lf[k]
		require.NotNil(t, entry, "lf[%q] is nil; keys in lf: %v", k, mapKeys(lf))
		m, ok := entry.(VDFMap)
		require.True(t, ok, "lf[%q] is %T", k, entry)
		if k == "0" {
			assert.Equal(t, "/home/user/.steam/steam", m["path"])
		} else {
			assert.Equal(t, "/mnt/games/steam", m["path"])
		}
	}
}

func mapKeys(m VDFMap) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestGetLibraryPaths(t *testing.T) {
	vdf := `
"libraryfolders"
{
	"0"
	{
		"path"		"/home/user/.steam/steam"
	}
	"1"
	{
		"path"		"/mnt/steam"
	}
}
`
	root, err := ParseVDF(strings.NewReader(vdf))
	require.NoError(t, err)
	lf, ok := root["libraryfolders"].(VDFMap)
	require.True(t, ok)
	e0, _ := lf["0"].(VDFMap)
	e1, _ := lf["1"].(VDFMap)
	assert.Equal(t, "/home/user/.steam/steam", e0["path"])
	assert.Equal(t, "/mnt/steam", e1["path"])
}

func TestParseAppManifest(t *testing.T) {
	acf := `
"AppState"
{
	"appid"		"489830"
	"name"		"Skyrim Special Edition"
	"installdir"		"Skyrim Special Edition"
}
`
	m, err := ParseAppManifest(acf)
	require.NoError(t, err)
	assert.Equal(t, "489830", m.AppID)
	assert.Equal(t, "Skyrim Special Edition", m.Name)
	assert.Equal(t, "Skyrim Special Edition", m.InstallDir)
}

func TestParseVDF_MalformedNoValue(t *testing.T) {
	// Single key with no value (would panic before bounds check)
	vdf := `"libraryfolders"`
	_, err := ParseVDF(strings.NewReader(vdf))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected end after key")
}

// --- getLibraryPaths (map -> []string extraction), tested directly ---

func TestGetLibraryPaths_MissingLibraryFoldersKey(t *testing.T) {
	root := VDFMap{"somethingelse": VDFMap{}}
	assert.Nil(t, getLibraryPaths(root))
}

func TestGetLibraryPaths_LibraryFoldersNotAMap(t *testing.T) {
	// "libraryfolders" present but as a plain string value, not a nested block.
	root := VDFMap{"libraryfolders": "not-a-map"}
	assert.Nil(t, getLibraryPaths(root))
}

func TestGetLibraryPaths_SkipsEntriesWithoutPathButKeepsScanning(t *testing.T) {
	vdf := `
"libraryfolders"
{
	"0"
	{
		"label"		"no path field here"
	}
	"1"
	{
		"path"		"/mnt/steam"
	}
}
`
	root, err := ParseVDF(strings.NewReader(vdf))
	require.NoError(t, err)
	assert.Equal(t, []string{"/mnt/steam"}, getLibraryPaths(root))
}

func TestGetLibraryPaths_SkipsEmptyPathValue(t *testing.T) {
	vdf := `
"libraryfolders"
{
	"0"
	{
		"path"		""
	}
}
`
	root, err := ParseVDF(strings.NewReader(vdf))
	require.NoError(t, err)
	assert.Nil(t, getLibraryPaths(root))
}

func TestGetLibraryPaths_StopsAtFirstNonSequentialKey(t *testing.T) {
	// Keys "0" and "2" (no "1") - extraction stops at the first gap, so "2" is
	// never reached even though it's present in the map.
	vdf := `
"libraryfolders"
{
	"0"
	{
		"path"		"/first"
	}
	"2"
	{
		"path"		"/skipped"
	}
}
`
	root, err := ParseVDF(strings.NewReader(vdf))
	require.NoError(t, err)
	assert.Equal(t, []string{"/first"}, getLibraryPaths(root))
}
