package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #608 re-review: matching stops for a cancelled context before it opens
// another file.
func TestMatchFillFiles_StopsOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plan := &ProfileFillPlan{}
	want := &fillWant{key: "nexusmods:1", fileID: "10", recipeFile: recipeFile{md5: "00112233445566778899aabbccddeeff", size: 4}}
	err := (&Service{}).matchFillFiles(ctx, plan,
		[]fillCandidate{{dir: t.TempDir(), name: "a.zip", display: "a.zip", size: 4}},
		map[int64][]*fillWant{4: {want}})
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, plan.Scanned, "nothing was looked at")
}
