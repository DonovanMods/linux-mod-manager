package core

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
)

// EncodeJSON writes v to w as exactly one JSON document (2-space indent,
// deterministic map/key ordering) followed by exactly one trailing newline.
// This IS the wire contract every --json document and future frontend
// (a local API included) shares (Ruling 3): one document, one newline,
// identical framing wherever a caller encodes a response.
func EncodeJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}
