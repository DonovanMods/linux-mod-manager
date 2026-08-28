package core_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestJSONV2Assumptions pins the encoding/json/v2 behaviours the contract
// relies on. If any assertion fails on this toolchain, STOP and ask — do
// not adapt the contract to the library silently.
func TestJSONV2Assumptions(t *testing.T) {
	type sample struct {
		List  []string          `json:"list"`
		Map   map[string]string `json:"map"`
		Empty string            `json:"empty,omitempty"`
		Zero  int               `json:"zero,omitzero"`
	}
	b, err := json.Marshal(sample{}, json.Deterministic(true))
	require.NoError(t, err)
	require.JSONEq(t, `{"list":[],"map":{}}`, string(b), "nil slices/maps marshal as empty, omitempty/omitzero drop the rest")

	var s sample
	err = json.Unmarshal([]byte(`{"list":["a"],"list":["b"]}`), &s)
	require.Error(t, err, "v2 rejects duplicate object names")
	s = sample{}
	require.NoError(t, json.Unmarshal([]byte(`{"LIST":["b"]}`), &s))
	require.Nil(t, s.List, "v2 matches names case-sensitively; LIST is an unknown member and is ignored")

	b, err = json.Marshal(map[string]int{"b": 1, "a": 2}, json.Deterministic(true), jsontext.WithIndent("  "))
	require.NoError(t, err)
	require.Equal(t, "{\n  \"a\": 2,\n  \"b\": 1\n}", string(b), "deterministic ordering + indent for goldens")
}
