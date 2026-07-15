package metadata

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeZip builds a zip file in t.TempDir() containing entries in the given
// order (order matters for tests asserting deterministic archive traversal).
func writeZip(t *testing.T, entries [][2]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mod.zip")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	w := zip.NewWriter(f)
	for _, entry := range entries {
		fw, err := w.Create(entry[0])
		require.NoError(t, err)
		_, err = fw.Write([]byte(entry[1]))
		require.NoError(t, err)
	}
	require.NoError(t, w.Close())
	return path
}

func TestResolveArchiveWrapperFolder(t *testing.T) {
	path := writeZip(t, [][2]string{{"donovan-aio/ModInfo.xml", modInfoV2}})
	info := ResolveArchive(path)
	require.NotNil(t, info)
	assert.Equal(t, "BiggerBackpack", info.Name)
	assert.Equal(t, "Bigger Backpack", info.DisplayName)
	assert.Equal(t, "1.2.0", info.Version)
	assert.Equal(t, "Carry more stuff", info.Summary)
	assert.Equal(t, "Donovan", info.Author)
}

func TestResolveArchiveRootLevel(t *testing.T) {
	path := writeZip(t, [][2]string{{"ModInfo.xml", modInfoV2}})
	info := ResolveArchive(path)
	require.NotNil(t, info)
	assert.Equal(t, "BiggerBackpack", info.Name)
}

func TestResolveArchiveNoMetadata(t *testing.T) {
	path := writeZip(t, [][2]string{{"readme.txt", "hi"}})
	assert.Nil(t, ResolveArchive(path))
}

func TestResolveArchiveMalformedXML(t *testing.T) {
	path := writeZip(t, [][2]string{{"ModInfo.xml", "<xml><unclosed"}})
	assert.Nil(t, ResolveArchive(path))
}

func TestResolveArchiveNonZip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.zip")
	require.NoError(t, os.WriteFile(path, []byte("not a zip file"), 0644))
	assert.Nil(t, ResolveArchive(path))
}

func TestResolveArchiveRootWinsOverNested(t *testing.T) {
	path := writeZip(t, [][2]string{
		{"othername/ModInfo.xml", modInfoV1},
		{"ModInfo.xml", modInfoV2},
	})
	info := ResolveArchive(path)
	require.NotNil(t, info)
	assert.Equal(t, "BiggerBackpack", info.Name, "root-level ModInfo.xml must win over a nested one")
}

func TestResolveArchiveFirstNestedMatchWinsInDeterministicOrder(t *testing.T) {
	path := writeZip(t, [][2]string{
		{"aaa/ModInfo.xml", modInfoV2},
		{"bbb/ModInfo.xml", modInfoV1},
	})
	info := ResolveArchive(path)
	require.NotNil(t, info)
	assert.Equal(t, "BiggerBackpack", info.Name, "the first one-deep match in archive order should win")
}
