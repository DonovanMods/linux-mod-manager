package metadata

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 7D2D "V2" layout: fields directly under <xml>.
const modInfoV2 = `<?xml version="1.0" encoding="UTF-8" ?>
<xml>
	<Name value="BiggerBackpack"/>
	<DisplayName value="Bigger Backpack"/>
	<Version value="1.2.0"/>
	<Description value="Carry more stuff"/>
	<Author value="Donovan"/>
</xml>`

// 7D2D "V1" layout: fields nested in <ModInfo>.
const modInfoV1 = `<?xml version="1.0" encoding="UTF-8" ?>
<xml>
	<ModInfo>
		<Name value="OldMod"/>
		<Version value="0.9"/>
		<Description value="Legacy layout"/>
		<Author value="Someone"/>
	</ModInfo>
</xml>`

// V1 layout missing the required <Name> element.
const modInfoV1NoName = `<?xml version="1.0" encoding="UTF-8" ?>
<xml>
	<ModInfo>
		<Version value="0.9"/>
	</ModInfo>
</xml>`

func writeModDir(t *testing.T, xml string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ModInfo.xml"), []byte(xml), 0644))
	return dir
}

func TestResolveModInfoV2(t *testing.T) {
	info := Resolve(writeModDir(t, modInfoV2))
	require.NotNil(t, info)
	assert.Equal(t, "BiggerBackpack", info.Name)
	assert.Equal(t, "Bigger Backpack", info.DisplayName)
	assert.Equal(t, "1.2.0", info.Version)
	assert.Equal(t, "Carry more stuff", info.Summary)
	assert.Equal(t, "Donovan", info.Author)
}

func TestResolveModInfoV1(t *testing.T) {
	info := Resolve(writeModDir(t, modInfoV1))
	require.NotNil(t, info)
	assert.Equal(t, "OldMod", info.Name)
	assert.Equal(t, "0.9", info.Version)
}

func TestResolveNoMetadata(t *testing.T) {
	assert.Nil(t, Resolve(t.TempDir()))
}

func TestResolveMalformedXML(t *testing.T) {
	assert.Nil(t, Resolve(writeModDir(t, "<xml><unclosed")))
}

// TestResolveModInfoV1MissingNameFallsBack pins that a V1 document without a
// <Name> element is treated as unparseable (issue #52 item 6): parseModInfo
// only recognizes the V1 layout via doc.ModInfo.Name.Value being non-empty,
// so a nameless <ModInfo> block falls through to the empty V2-shaped fields,
// which then also lacks a Name and fails - Resolve returns nil so callers
// fall back to filename-based detection instead of erroring.
func TestResolveModInfoV1MissingNameFallsBack(t *testing.T) {
	assert.Nil(t, Resolve(writeModDir(t, modInfoV1NoName)))
}

// TestResolveModInfoCaseInsensitiveFilename pins that Detect finds ModInfo.xml
// regardless of case (issue #52 item 5): some 7 Days to Die mod packagers ship
// lowercase modinfo.xml on Linux filesystems, where filenames are case-sensitive.
func TestResolveModInfoCaseInsensitiveFilename(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "modinfo.xml"), []byte(modInfoV2), 0644))

	info := Resolve(dir)
	require.NotNil(t, info, "Resolve must find modinfo.xml despite case mismatch")
	assert.Equal(t, "BiggerBackpack", info.Name)
}
