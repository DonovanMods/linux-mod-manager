package custom

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNew(t *testing.T) {
	t.Run("unimplemented types return a clear error", func(t *testing.T) {
		for _, typ := range []string{TypeDirectory, TypeManifest, TypeAPI} {
			def := SourceDefinition{ID: "x", Name: "X", Type: typ}
			_, err := New(def)
			assert.ErrorContains(t, err, "not yet supported", "type %s", typ)
		}
	})
}
