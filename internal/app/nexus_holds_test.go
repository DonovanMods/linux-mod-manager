package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// TestOpen_NexusModsHoldsPersistUnderTheDataDir (#675): the NexusMods source
// keeps its throttle holds in <DataDir>/nexusmods-holds.json, so a hold one
// lmm process recorded is reported - and honoured - by the next one.
func TestOpen_NexusModsHoldsPersistUnderTheDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "lmm")
	require.NoError(t, os.MkdirAll(dataDir, 0o700))
	until := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	doc := `{"schema":1,"host":{"until":"` + until.Format(time.RFC3339) + `","reason":"rate limited by NexusMods","named":true}}`
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "nexusmods-holds.json"), []byte(doc), 0o600))

	svc, err := Open(t.Context(), Options{ConfigDir: t.TempDir(), DataDir: dataDir})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	src, err := svc.GetSource("nexusmods")
	require.NoError(t, err)
	reporter, ok := src.(source.HoldReporter)
	require.True(t, ok)
	holds := reporter.Holds(t.Context())
	require.Len(t, holds, 1)
	assert.Equal(t, "NexusMods", holds[0].Source)
	assert.True(t, holds[0].Until.Equal(until), "%v != %v", holds[0].Until, until)
}
