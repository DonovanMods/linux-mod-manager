package metadata

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
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

func TestResolveArchiveTooDeepNesting(t *testing.T) {
	path := writeZip(t, [][2]string{{"a/b/ModInfo.xml", modInfoV2}})
	assert.Nil(t, ResolveArchive(path), "ModInfo.xml two directories deep must return nil")
}

func TestResolveArchiveDecompressionBomb(t *testing.T) {
	// Create a zip with a ModInfo.xml entry that decompresses to >1 MiB.
	// Use a valid XML structure but pad it with a multi-MiB comment to exceed the cap
	// while remaining valid XML if fully read.
	f, err := os.Create(filepath.Join(t.TempDir(), "bomb.zip"))
	require.NoError(t, err)
	defer f.Close()

	w := zip.NewWriter(f)
	// Create a large valid XML document with padding comment
	header := `<?xml version="1.0" encoding="UTF-8" ?>
<xml>
	<Name value="BiggerBackpack"/>
	<!-- `
	footer := ` -->
</xml>`
	// Padding of ~1.1 MiB (spaces compress well in a zip)
	padding := strings.Repeat("X", 1100000)
	largeXML := header + padding + footer

	fw, err := w.Create("ModInfo.xml")
	require.NoError(t, err)
	_, err = fw.Write([]byte(largeXML))
	require.NoError(t, err)
	require.NoError(t, w.Close())

	// Should return nil because the decompressed size exceeds the cap
	info := ResolveArchive(f.Name())
	assert.Nil(t, info, "oversized ModInfo.xml must return nil to prevent decompression bomb")
}
