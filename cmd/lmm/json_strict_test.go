package main

// decodeStrict is the v2 pre-cut addendum's (#309) framing helper: unlike
// decodeSingleDoc (single_update_json_test.go), which decodes into `any` to
// prove there is exactly one document, this decodes into the CALLER'S
// declared wire type via encoding/json/v2 with RejectUnknownMembers, so a
// field the type doesn't declare - a stray key a hand-rolled emitJSON call
// could introduce - fails the test instead of silently round-tripping
// through an untyped map.

import (
	"encoding/json/v2"
	"testing"

	"github.com/stretchr/testify/require"
)

// decodeStrict unmarshals raw into out, rejecting any JSON object member
// that does not match a field on out's declared type.
func decodeStrict(t *testing.T, raw string, out any) {
	t.Helper()
	require.NoError(t, json.Unmarshal([]byte(raw), out, json.RejectUnknownMembers(true)),
		"--json document must decode into the declared type with no unknown members")
}
