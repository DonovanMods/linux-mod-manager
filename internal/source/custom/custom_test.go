package custom

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNew(t *testing.T) {
	t.Run("directory type constructs a source", func(t *testing.T) {
		def := SourceDefinition{
			ID:        "my-mods",
			Name:      "My Mods",
			Type:      TypeDirectory,
			Directory: &DirectoryConfig{Path: t.TempDir()},
		}
		src, err := New(def)
		assert.NoError(t, err)
		assert.Equal(t, "my-mods", src.ID())
	})

	t.Run("unimplemented types return a clear error", func(t *testing.T) {
		for _, typ := range []string{TypeManifest, TypeAPI} {
			def := SourceDefinition{ID: "x", Name: "X", Type: typ}
			_, err := New(def)
			assert.ErrorContains(t, err, "not yet supported", "type %s", typ)
		}
	})
}
