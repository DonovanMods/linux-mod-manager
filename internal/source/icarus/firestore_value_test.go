package icarus

import (
	"reflect"
	"testing"
)

func TestDecodeFields(t *testing.T) {
	// Shape of a real Firestore REST document's "fields" object.
	raw := map[string]any{
		"name":    map[string]any{"stringValue": "Bear Mount"},
		"version": map[string]any{"stringValue": "3.3"},
		"files": map[string]any{"mapValue": map[string]any{"fields": map[string]any{
			"pak":    map[string]any{"stringValue": "https://example.com/mod.pak"},
			"exmodz": map[string]any{"stringValue": "https://example.com/mod.exmodz"},
		}}},
		"missing": map[string]any{"nullValue": nil},
	}

	got := decodeFields(raw)

	want := map[string]any{
		"name":    "Bear Mount",
		"version": "3.3",
		"files": map[string]any{
			"pak":    "https://example.com/mod.pak",
			"exmodz": "https://example.com/mod.exmodz",
		},
		"missing": nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decodeFields() = %#v, want %#v", got, want)
	}
}
