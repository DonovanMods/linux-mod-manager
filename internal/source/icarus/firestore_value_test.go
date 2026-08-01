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

// TestDecodeValue table-drives every value kind decodeValue recognizes,
// plus its two fallback-to-nil paths (an unrecognized kind, and input that
// isn't a wrapped {"kind": ...} object at all) — decodeFields' own test
// above only exercises stringValue/mapValue/nullValue.
func TestDecodeValue(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want any
	}{
		{"stringValue", map[string]any{"stringValue": "hello"}, "hello"},
		{"booleanValue", map[string]any{"booleanValue": true}, true},
		// Firestore's REST API encodes integerValue as a decimal STRING
		// (avoiding int64-precision loss in JSON numbers), not a JSON number.
		{"integerValue", map[string]any{"integerValue": "42"}, "42"},
		{"doubleValue", map[string]any{"doubleValue": 3.5}, 3.5},
		{
			"mapValue decodes its nested fields recursively",
			map[string]any{"mapValue": map[string]any{"fields": map[string]any{
				"inner": map[string]any{"stringValue": "nested"},
			}}},
			map[string]any{"inner": "nested"},
		},
		{
			"arrayValue decodes each element recursively",
			map[string]any{"arrayValue": map[string]any{"values": []any{
				map[string]any{"stringValue": "a"},
				map[string]any{"integerValue": "1"},
				map[string]any{"booleanValue": false},
			}}},
			[]any{"a", "1", false},
		},
		{"arrayValue with no values key decodes to an empty slice", map[string]any{"arrayValue": map[string]any{}}, []any{}},
		{"nullValue", map[string]any{"nullValue": nil}, nil},
		{"unrecognized kind decodes to nil, not a panic", map[string]any{"geoPointValue": map[string]any{"latitude": 1.0}}, nil},
		{"non-map input decodes to nil", "not a wrapped value", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeValue(tt.in)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("decodeValue(%#v) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}
