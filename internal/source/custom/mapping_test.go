package custom

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jsonDoc decodes a JSON literal the way the API source does (numbers become
// float64), so tests exercise the real coercion paths.
func jsonDoc(t *testing.T, s string) any {
	t.Helper()
	var doc any
	require.NoError(t, json.Unmarshal([]byte(s), &doc))
	return doc
}

func TestLookupPath(t *testing.T) {
	doc := jsonDoc(t, `{"a": {"b": {"c": 42}}, "top": "x", "arr": [1,2]}`)

	v, ok := lookupPath(doc, "a.b.c")
	require.True(t, ok)
	assert.Equal(t, float64(42), v)

	v, ok = lookupPath(doc, "top")
	require.True(t, ok)
	assert.Equal(t, "x", v)

	_, ok = lookupPath(doc, "a.missing")
	assert.False(t, ok)

	_, ok = lookupPath(doc, "top.deeper") // traversing into a non-map
	assert.False(t, ok)

	_, ok = lookupPath(doc, "arr.0") // array indexing unsupported in v1
	assert.False(t, ok)
}

func TestCoercions(t *testing.T) {
	assert.Equal(t, "hello", coerceString("hello"))
	assert.Equal(t, "123", coerceString(float64(123))) // integer id, no ".0"
	assert.Equal(t, "1.5", coerceString(float64(1.5)))
	assert.Equal(t, "", coerceString(nil))
	assert.Equal(t, "", coerceString(true))

	assert.Equal(t, int64(42), coerceInt64(float64(42)))
	assert.Equal(t, int64(42), coerceInt64("42"))
	assert.Equal(t, int64(0), coerceInt64("not a number"))
	assert.Equal(t, int64(0), coerceInt64(nil))
}

func TestMapMod(t *testing.T) {
	doc := jsonDoc(t, `{
		"id": 77, "name": "Cool Mod", "latest_version": "1.2.0",
		"author": {"name": "someone"}, "description": "Makes things cooler",
		"download_count": 12345, "updated": "2026-07-01T00:00:00Z",
		"web_url": "https://x.test/mods/77"
	}`)
	mapping := map[string]string{
		"id": "id", "name": "name", "version": "latest_version",
		"author": "author.name", "summary": "description",
		"downloads": "download_count", "updated_at": "updated", "url": "web_url",
	}

	mod, err := mapMod(doc, mapping, "my-api")
	require.NoError(t, err)
	assert.Equal(t, "77", mod.ID) // numeric id coerced without ".0"
	assert.Equal(t, "my-api", mod.SourceID)
	assert.Equal(t, "Cool Mod", mod.Name)
	assert.Equal(t, "1.2.0", mod.Version)
	assert.Equal(t, "someone", mod.Author)
	assert.Equal(t, "Makes things cooler", mod.Summary)
	assert.Equal(t, int64(12345), mod.Downloads)
	assert.Equal(t, 2026, mod.UpdatedAt.Year())
	assert.Equal(t, "https://x.test/mods/77", mod.SourceURL)
}

func TestMapModRequiredFields(t *testing.T) {
	mapping := map[string]string{"id": "id", "name": "name"}

	_, err := mapMod(jsonDoc(t, `{"name": "NoID"}`), mapping, "s")
	assert.ErrorContains(t, err, `required field "id"`)

	_, err = mapMod(jsonDoc(t, `{"id": 1}`), mapping, "s")
	assert.ErrorContains(t, err, `required field "name"`)
}

func TestMapModOptionalMissingYieldsZero(t *testing.T) {
	mapping := map[string]string{"id": "id", "name": "name", "version": "nope.nothere", "updated_at": "bad_time"}
	mod, err := mapMod(jsonDoc(t, `{"id": "x", "name": "X", "bad_time": "yesterday-ish"}`), mapping, "s")
	require.NoError(t, err)
	assert.Empty(t, mod.Version)
	assert.True(t, mod.UpdatedAt.IsZero())
}

func TestMapFile(t *testing.T) {
	doc := jsonDoc(t, `{"id": 900, "title": "Main File", "file_name": "cool-1.2.0.zip", "version": "1.2.0", "size_bytes": 123456}`)
	mapping := map[string]string{"id": "id", "name": "title", "filename": "file_name", "version": "version", "size": "size_bytes"}

	f, err := mapFile(doc, mapping)
	require.NoError(t, err)
	assert.Equal(t, "900", f.ID)
	assert.Equal(t, "Main File", f.Name)
	assert.Equal(t, "cool-1.2.0.zip", f.FileName)
	assert.Equal(t, "1.2.0", f.Version)
	assert.Equal(t, int64(123456), f.Size)

	_, err = mapFile(jsonDoc(t, `{"title": "no id"}`), mapping)
	assert.ErrorContains(t, err, `required field "id"`)
}
