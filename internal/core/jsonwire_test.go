package core_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/require"
)

// TestEncodeJSON_ByteIdenticalToRecordedGolden pins EncodeJSON's framing
// against an already-recorded golden fragment (enable_result.golden, from
// TestJSONGoldens) rather than a fresh literal: the two must never drift
// apart, since a future --json/API consumer relies on both producing the
// exact same bytes for the exact same value.
func TestEncodeJSON_ByteIdenticalToRecordedGolden(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "json", "enable_result.golden"))
	require.NoError(t, err)

	var buf bytes.Buffer
	err = core.EncodeJSON(&buf, core.EnableResult{
		Changed:  true,
		Notes:    []string{"forced reinstall before enabling"},
		Warnings: []string{"could not sync merged pak"},
	})
	require.NoError(t, err)

	require.Equal(t, string(want), buf.String())
}
