package core_test

// The PLAN half of the adapter layout guard (#422).
//
// rewriteExtractedTree validates an adapter's whole rewrite table before it
// renames anything, but rewritePlannedPaths - its pure twin, the one
// PlanImportArchive renders - ran no validation at all. So a plan promised
// "../escaped.txt", ".", "/abs/x.txt" or a silently-shortened chain that
// Apply then refused: a plan must never promise what Apply refuses.
//
// The reserved-namespace refusal (#422 item 1) is driven from both sides
// here for the same reason it is the most serious of the three: a
// ".lmm-"-prefixed destination forges a cache completion marker, and the
// failure mode is a SUCCESSFUL import of a mod whose file never deploys.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedDestStub rewrites the archive's sole member to one fixed
// destination, which is how a hostile or buggy table is spelled in one
// line.
type fixedDestStub struct{ dest string }

func (fixedDestStub) ID() string    { return "fixed-dest" }
func (fixedDestStub) Label() string { return "Fixed destination" }

func (s fixedDestStub) NormalizeArchive(req adapter.NormalizeRequest) (adapter.Layout, error) {
	rewrites := make(map[string]string, len(req.Members))
	for _, m := range req.Members {
		rewrites[m] = s.dest
	}
	return adapter.NewLayout("fixed-dest", rewrites), nil
}

// TestPlanImportArchive_RefusesAReservedDestination drives #422 item 1
// through the plan AND the ingest: a destination in lmm's reserved
// namespace is refused by both, so the forged marker is unreachable from
// either direction.
func TestPlanImportArchive_RefusesAReservedDestination(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	svc.RegisterAdapter(fixedDestStub{dest: ".lmm-file-deadbeef"})
	game.Adapter = "fixed-dest"
	require.NoError(t, svc.SaveGame(context.Background(), game))

	archivePath := filepath.Join(t.TempDir(), "Flat-2.0.zip")
	createImportTestZip(t, archivePath, map[string]string{"a.txt": "A"})

	_, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.Error(t, err, "the plan must refuse a reserved destination rather than render it")
	var typed *core.AdapterLayoutError
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, []string{"a.txt"}, typed.Members)
}

// TestPlanImportArchive_RefusesWhatApplyWouldRefuse is #422 item 3: every
// shape rewriteExtractedTree refuses on disk must be refused at plan time
// too, by the same validation, with the same typed error.
func TestPlanImportArchive_RefusesWhatApplyWouldRefuse(t *testing.T) {
	for name, dest := range map[string]string{
		"escaping the cache entry": "../escaped.txt",
		"the cache entry itself":   ".",
		"an absolute path":         "/abs/x.txt",
	} {
		t.Run(name, func(t *testing.T) {
			svc, game := newImportArchiveTestService(t)
			svc.RegisterAdapter(fixedDestStub{dest: dest})
			game.Adapter = "fixed-dest"
			require.NoError(t, svc.SaveGame(context.Background(), game))

			archivePath := filepath.Join(t.TempDir(), "Flat-2.0.zip")
			createImportTestZip(t, archivePath, map[string]string{"a.txt": "A"})

			_, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
			require.Error(t, err, "the plan must refuse the destination Apply would refuse")
			var typed *core.AdapterLayoutError
			require.ErrorAs(t, err, &typed)
		})
	}
}

// TestPlanImportArchive_RefusesACollidingTable is the other half of item 3:
// a table mapping two members onto one destination silently SHORTENED the
// plan's file list (rewritePlannedPaths compacts), so the plan promised one
// file where the archive held two and Apply then refused the whole import.
func TestPlanImportArchive_RefusesACollidingTable(t *testing.T) {
	svc, game := newImportArchiveTestService(t)
	svc.RegisterAdapter(fixedDestStub{dest: "out/only.txt"})
	game.Adapter = "fixed-dest"
	require.NoError(t, svc.SaveGame(context.Background(), game))

	archivePath := filepath.Join(t.TempDir(), "Flat-2.0.zip")
	createImportTestZip(t, archivePath, map[string]string{"a.txt": "A", "b.txt": "B"})

	_, err := svc.PlanImportArchive(context.Background(), game, "default", archivePath, core.ImportArchiveOptions{})
	require.Error(t, err, "the plan must refuse a colliding table rather than shorten its own file list")
	var typed *core.AdapterLayoutError
	require.ErrorAs(t, err, &typed)
	assert.Equal(t, []string{"a.txt", "b.txt"}, typed.Members)
}
