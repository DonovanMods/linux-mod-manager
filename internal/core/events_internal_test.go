package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeployPhase_TextRoundTrip walks every phase that has a wire name,
// deriving its upper bound from len(deployPhaseNames) rather than a
// hardcoded last constant (M5) — so appending a phase and its name grows
// this loop automatically, instead of needing a matching manual bump here.
func TestDeployPhase_TextRoundTrip(t *testing.T) {
	seen := make(map[string]DeployPhase, len(deployPhaseNames))
	for i := DeployPurging; int(i) < len(deployPhaseNames); i++ {
		text, err := i.MarshalText()
		require.NoError(t, err)
		name := string(text)
		require.NotEmpty(t, name, "phase %d has an empty wire name", int(i))

		if prev, ok := seen[name]; ok {
			t.Fatalf("wire name %q used by both phase %d and phase %d", name, int(prev), int(i))
		}
		seen[name] = i

		var got DeployPhase
		require.NoError(t, got.UnmarshalText(text))
		assert.Equal(t, i, got, "round-trip mismatch for phase %d (%q)", int(i), name)
	}
}
