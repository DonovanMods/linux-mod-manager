package core

// snapshot_prune_internal_test.go covers the automatic-snapshot prune
// (coordinator ruling (b) on the #350 review's note 13). Internal, because
// it drives createSnapshot/pruneAutoSnapshots directly: the exported
// autoSnapshot path names its snapshots by the SECOND, so five in a row
// would collide on name rather than exercise the prune.

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newPruneFixture returns a Service whose config.yaml keeps `keep`
// automatic snapshots, plus a game to snapshot.
func newPruneFixture(t *testing.T, configYAML string) (*Service, *domain.Game) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))

	configDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(configYAML), 0644))

	svc, err := NewService(ServiceConfig{
		ConfigDir: configDir, DataDir: t.TempDir(), CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir()}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	_, err = svc.NewProfileManager().Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	return svc, game
}

// seedAutoSnapshot records an automatic snapshot and back-dates it by
// `age` seconds.
//
// Back-dating is necessary because a listing orders by created_at and
// createSnapshot stamps time.Now() truncated to the second: five snapshots
// taken inside one test are all the SAME second, which falls through to the
// name tiebreak and would order them oldest-first. In production that tie
// cannot happen among automatic snapshots at all - their names are the
// second they were taken, so two in one second collide on name first.
func seedAutoSnapshot(t *testing.T, svc *Service, game *domain.Game, name string, age time.Duration) {
	t.Helper()
	_, err := svc.createSnapshot(context.Background(), game, "default", name, true)
	require.NoError(t, err)

	path := svc.snapshotPath(game.ID, name)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var doc Snapshot
	require.NoError(t, json.Unmarshal(data, &doc))
	doc.CreatedAt = doc.CreatedAt.Add(-age)
	out, err := json.Marshal(doc, json.Deterministic(true), jsontext.WithIndent("  "))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, out, 0600))
}

// autoNames returns the automatic and named snapshots of g1, newest first.
func autoNames(t *testing.T, svc *Service) (auto, named []string) {
	t.Helper()
	listing, err := svc.ListSnapshots(context.Background(), "g1")
	require.NoError(t, err)
	for _, row := range listing.Snapshots {
		if row.Auto {
			auto = append(auto, row.Name)
		} else {
			named = append(named, row.Name)
		}
	}
	return auto, named
}

func TestPruneAutoSnapshots_KeepsTheNewestAndNeverTouchesANamedOne(t *testing.T) {
	svc, game := newPruneFixture(t, "auto_snapshot: true\nauto_snapshot_keep: 3\n")
	ctx := context.Background()
	require.Equal(t, 3, svc.config.AutoSnapshotKeep)

	_, err := svc.createSnapshot(ctx, game, "default", "mine", false)
	require.NoError(t, err)
	for i := range 5 {
		// 04 is the newest, 00 the oldest.
		seedAutoSnapshot(t, svc, game, fmt.Sprintf("auto-deploy-%02d", i), time.Duration(5-i)*time.Minute)
	}

	assert.Empty(t, svc.pruneAutoSnapshots(ctx, "g1"))

	auto, named := autoNames(t, svc)
	assert.Len(t, auto, 3, "auto_snapshot_keep: 3 keeps the three newest automatic snapshots")
	assert.Contains(t, auto, "auto-deploy-04", "the newest survives")
	assert.NotContains(t, auto, "auto-deploy-00", "the oldest does not")
	assert.Equal(t, []string{"mine"}, named, "a snapshot the user named is never pruned")
}

func TestPruneAutoSnapshots_ZeroMeansUnlimited(t *testing.T) {
	svc, game := newPruneFixture(t, "auto_snapshot: true\nauto_snapshot_keep: 0\n")
	ctx := context.Background()
	require.Equal(t, 0, svc.config.AutoSnapshotKeep, "an EXPLICIT 0 survives the default")

	for i := range 4 {
		seedAutoSnapshot(t, svc, game, fmt.Sprintf("auto-deploy-%02d", i), time.Duration(4-i)*time.Minute)
	}
	assert.Empty(t, svc.pruneAutoSnapshots(ctx, "g1"))

	auto, _ := autoNames(t, svc)
	assert.Len(t, auto, 4, "0 means unlimited, so nothing is pruned")
}

func TestPruneAutoSnapshots_AnAbsentKeyGetsTheDefault(t *testing.T) {
	svc, _ := newPruneFixture(t, "auto_snapshot: true\n")
	assert.Equal(t, 10, svc.config.AutoSnapshotKeep,
		"an absent auto_snapshot_keep is the documented default, not unlimited")
}
