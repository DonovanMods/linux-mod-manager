package tui_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCoreProviderDeployProfile_CompileGame_NamesMergedArtifactInMessage is
// #255's TUI parity test: coreProvider.DeployProfile passes nil progress
// (no event stream), so the merge readout must come from DeployResult's own
// fields and land in the one-row outcome Message - NOT in Warnings, whose
// 2+-entry overlay (#253) would otherwise pop a modal on every routine
// compile deploy. The fixture holds one enabled exmodz mod, so the merged
// artifact carries exactly one mod.
func TestCoreProviderDeployProfile_CompileGame_NamesMergedArtifactInMessage(t *testing.T) {
	actions, _, mergedPakPath := newRecompileActionsFixture(t)

	outcome, err := actions.DeployProfile(context.Background())
	require.NoError(t, err)

	require.Equal(t, "Deployed 1 mod(s) — merged 1 → zzz_LMM_Merged_P.pak", outcome.Message,
		"the one-row status message must name the merged artifact and its participant count")
	require.Empty(t, outcome.Warnings,
		"routine merge info must not ride the warnings channel (#253's overlay auto-opens on 2+ warnings)")

	_, statErr := os.Stat(mergedPakPath)
	require.NoError(t, statErr, "the merged artifact must actually be deployed")
}
