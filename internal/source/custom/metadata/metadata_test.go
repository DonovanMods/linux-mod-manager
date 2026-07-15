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
