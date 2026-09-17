package safeyaml_test

import (
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/safeyaml"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// panicky is a value whose own decoding panics, standing in for a decoder
// bug nobody has found yet.
type panicky struct{}

func (*panicky) UnmarshalYAML(*yaml.Node) error { panic("a decoder bug") }

func TestUnmarshal_ADecoderPanicIsAnError(t *testing.T) {
	var v panicky
	var err error
	require.NotPanics(t, func() { err = safeyaml.Unmarshal([]byte("x: 1\n"), &v) })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a decoder bug")
}

// TestUnmarshal_TheFuzzersCrasher is #452's reproduction: a merge key over
// a mapping keyed by a mapping, which gopkg.in/yaml.v3 v3.0.1 panicked on
// (FuzzMarkModsDisabled's corpus). It is an error, whatever the target.
func TestUnmarshal_TheFuzzersCrasher(t *testing.T) {
	for name, target := range map[string]any{
		"a map":    &map[string]any{},
		"a struct": &struct{ Mods []map[string]any }{},
	} {
		t.Run(name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() { err = safeyaml.Unmarshal([]byte("<<:\n? 0:"), target) })
			require.Error(t, err)
		})
	}
}

func TestUnmarshal_DecodesAnOrdinaryDocument(t *testing.T) {
	var v struct {
		Name string `yaml:"name"`
	}
	require.NoError(t, safeyaml.Unmarshal([]byte("name: skyrim\n"), &v))
	assert.Equal(t, "skyrim", v.Name)

	var bad struct{ Name int }
	err := safeyaml.Unmarshal([]byte("name: [\n"), &bad)
	require.Error(t, err)
	var typeErr *yaml.TypeError
	assert.False(t, errors.As(err, &typeErr), "a syntax error is the decoder's own error, unchanged")
}
