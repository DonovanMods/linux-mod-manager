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

	t.Run("manifest type constructs a source", func(t *testing.T) {
		def := SourceDefinition{
			ID:       "my-repo",
			Name:     "My Repo",
			Type:     TypeManifest,
			Manifest: &ManifestConfig{URL: "https://x.test/mods.yaml"},
		}
		src, err := New(def)
		assert.NoError(t, err)
		assert.Equal(t, "my-repo", src.ID())
	})

	t.Run("api type constructs a source", func(t *testing.T) {
		def := validAPIDef()
		src, err := New(def)
		assert.NoError(t, err)
		assert.Equal(t, "my-api", src.ID())
	})

	t.Run("unknown type is rejected", func(t *testing.T) {
		def := SourceDefinition{ID: "x", Name: "X", Type: "ftp"}
		_, err := New(def)
		assert.ErrorContains(t, err, "not yet supported")
	})
}
