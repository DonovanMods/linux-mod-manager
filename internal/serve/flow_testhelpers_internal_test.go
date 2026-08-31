package serve

// Shared fixtures for the package-internal tests that drive a whole
// mutation flow - Plan, then Apply as a job - and then assert on the state
// it left behind (the DB row, the profile, the deployed tree).
//
// These are the surviving half of what was mutations_testhelpers_internal_
// test.go: the browser-form helpers went with the server-rendered page
// layer (docs/plans/2026-08-31-serve-spa-design.md), while the fixture
// seeding and the end-state readers below are entry-point-agnostic and are
// what the /api/v1 flow tests assert with.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/require"
)

// deployFixtureProfile runs a REAL deploy of the fixture profile, so the
// deployed_files rows a conflict check reads and the game-dir file an
// uninstall removes both genuinely exist. It goes through the plan/apply
// pair (never DeployProfile, the Ruling-10 convenience wrapper) for the same
// reason serve itself does.
func deployFixtureProfile(t *testing.T, s *Server, game *domain.Game) {
	t.Helper()
	plan, err := s.svc.PlanDeploy(t.Context(), game, "default", core.DeployOptions{})
	require.NoError(t, err)
	_, err = s.svc.ApplyDeploy(t.Context(), game, plan, core.DeployOptions{}, nil)
	require.NoError(t, err)
}

// deployedPath is where a relative game-directory path lands under the
// fixture game's ModPath - the tree half of every flow's end-state
// assertion (task-8 review: install's tests asserted the DB row and the
// profile but never the file, so a regression that stopped install
// deploying would have gone unnoticed).
func deployedPath(game *domain.Game, rel string) string {
	return filepath.Join(game.ModPath, filepath.FromSlash(rel))
}

// deployedContent reads what a deployed path actually resolves to. The
// deployment is a symlink into the cache, so this follows it - which is
// exactly the point: it proves WHICH cached file the game directory is
// pointing at, not merely that something is there.
func deployedContent(t *testing.T, game *domain.Game, rel string) string {
	t.Helper()
	data, err := os.ReadFile(deployedPath(game, rel))
	require.NoError(t, err)
	return string(data)
}
