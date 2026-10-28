package main

import (
	"context"
	"regexp"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/require"
)

// #315: the install conflict block groups its paths per owning mod, and it
// used to build those groups in a map and iterate it, so the "From <mod>
// (<id>):" headers came out in a different order on every run. core now
// hands the list over sorted by owning mod then path; the renderer must
// follow that order rather than re-scrambling it.
func TestConfirmInstallConflicts_GroupOrderIsDeterministic(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	// The order core produces: owning mod first, path second.
	conflicts := []core.Conflict{
		{RelativePath: "b.txt", CurrentSourceID: "src", CurrentModID: "aaa"},
		{RelativePath: "d.txt", CurrentSourceID: "src", CurrentModID: "aaa"},
		{RelativePath: "a.txt", CurrentSourceID: "src", CurrentModID: "mmm"},
		{RelativePath: "c.txt", CurrentSourceID: "src", CurrentModID: "zzz"},
	}
	header := regexp.MustCompile(`(?m)^  From \S+ \((\S+)\):$`)

	for i := 0; i < 25; i++ {
		var out string
		withStdin(t, "n\n", func() {
			out = captureStdout(t, func() error {
				_, err := confirmInstallConflicts(context.Background(), svc, game, "default", conflicts)
				return err
			})
		})
		var got []string
		for _, m := range header.FindAllStringSubmatch(out, -1) {
			got = append(got, m[1])
		}
		require.Equal(t, []string{"aaa", "mmm", "zzz"}, got, "run %d printed the groups in the wrong order", i)
	}
}
