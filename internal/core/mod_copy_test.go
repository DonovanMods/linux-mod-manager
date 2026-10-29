package core_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingSource is a source that records every call made to it, so a mod
// copy can assert it never asked one anything (#437: the bytes are already
// in the cache).
type countingSource struct {
	*mockSource
	calls atomic.Int64
}

func (c *countingSource) GetMod(ctx context.Context, gameID, modID string) (*domain.Mod, error) {
	c.calls.Add(1)
	return c.mockSource.GetMod(ctx, gameID, modID)
}

func (c *countingSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	c.calls.Add(1)
	return c.mockSource.GetModFiles(ctx, mod)
}

func (c *countingSource) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	c.calls.Add(1)
	return c.mockSource.GetDownloadURL(ctx, mod, fileID)
}

func (c *countingSource) GetDependencies(ctx context.Context, mod *domain.Mod) ([]domain.ModReference, error) {
	c.calls.Add(1)
	return c.mockSource.GetDependencies(ctx, mod)
}

func (c *countingSource) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	c.calls.Add(1)
	return c.mockSource.CheckUpdates(ctx, installed)
}

func (c *countingSource) Search(ctx context.Context, q source.SearchQuery) (source.SearchResult, error) {
	c.calls.Add(1)
	return c.mockSource.Search(ctx, q)
}

type copyFixture struct {
	svc  *core.Service
	game *domain.Game
	src  *countingSource
}

// seedCopyRow caches files for sourceID "src"/modID at version and saves
// profile's row for it.
func (f *copyFixture) seedRow(t *testing.T, profile, modID, version string, files map[string][]byte, deployed bool) {
	t.Helper()
	for path, content := range files {
		require.NoError(t, f.svc.GetGameCache(f.game).Store(f.game.ID, "src", modID, version, path, content))
	}
	require.NoError(t, f.svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:         domain.Mod{ID: modID, SourceID: "src", Name: "Mod " + modID, Version: version, GameID: f.game.ID},
		ProfileName: profile, UpdatePolicy: domain.UpdatePinned, Enabled: true, Deployed: deployed,
		FileIDs: []string{"f-" + modID},
	}))
}

func (f *copyFixture) setRefs(t *testing.T, profile string, refs ...domain.ModReference) {
	t.Helper()
	p, err := f.svc.NewProfileManager().Get(context.Background(), f.game.ID, profile)
	require.NoError(t, err)
	p.Mods = refs
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), p))
}

// newCopyFixture: game g1 with the active "default" and an inactive "alt",
// both empty, and a counting source registered as "src".
func newCopyFixture(t *testing.T) *copyFixture {
	t.Helper()
	svc := newFlowsTestService(t)
	ctx := context.Background()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"src": "g1"}}
	require.NoError(t, svc.SaveGame(ctx, game))
	src := &countingSource{mockSource: newMockSource("src")}
	svc.RegisterSource(src)

	pm := svc.NewProfileManager()
	_, err := pm.Create(ctx, game.ID, "default")
	require.NoError(t, err)
	_, err = pm.Create(ctx, game.ID, "alt")
	require.NoError(t, err)
	require.NoError(t, pm.SetDefault(ctx, game.ID, "default"))
	return &copyFixture{svc: svc, game: game, src: src}
}

// TestCopyMod_IntoAnInactiveProfileRecordsOnly is #437's first test: the
// destination's document and row are right, nothing reaches the game
// directory, and no source is asked anything.
func TestCopyMod_IntoAnInactiveProfileRecordsOnly(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	f.seedRow(t, "default", "a", "1.0", map[string][]byte{"a.esp": []byte("A")}, false)
	f.setRefs(t, "default", domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0", FileIDs: []string{"f-a"}, Locked: true})
	_, err := f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	f.setRefs(t, "alt", domain.ModReference{SourceID: "src", ModID: "z", Version: "9"})

	plan, err := f.svc.PlanCopyMod(ctx, f.game, core.CopyModRequest{From: "default", To: "alt", SourceID: "src", ModID: "a"})
	require.NoError(t, err)
	assert.False(t, plan.DestinationActive)
	require.Len(t, plan.Mods, 1)
	entry := plan.Mods[0]
	assert.Equal(t, "1.0", entry.Version)
	assert.Equal(t, []string{"f-a"}, entry.FileIDs)
	assert.True(t, entry.Locked, "the plan says the lock is carried")
	assert.False(t, entry.AlreadyListed)

	result, err := f.svc.ApplyCopyMod(ctx, f.game, plan, core.CopyModOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, result.Copied, 1)
	assert.False(t, result.Copied[0].Deployed)

	alt, err := f.svc.NewProfileManager().Get(ctx, f.game.ID, "alt")
	require.NoError(t, err)
	assert.Equal(t, []domain.ModReference{
		{SourceID: "src", ModID: "z", Version: "9"},
		{SourceID: "src", ModID: "a", Version: "1.0", FileIDs: []string{"f-a"}, Locked: true},
	}, alt.Mods, "appended after the destination's own refs, at the source's version, lock carried")

	row, err := f.svc.GetInstalledMod(ctx, "src", "a", f.game.ID, "alt")
	require.NoError(t, err)
	assert.Equal(t, "1.0", row.Version)
	assert.Equal(t, []string{"f-a"}, row.FileIDs)
	assert.True(t, row.Enabled)
	assert.False(t, row.Deployed, "the destination is not active")
	assert.Equal(t, domain.UpdateNotify, row.UpdatePolicy, "a fresh row: the source row's policy is its profile's")

	_, _, found, err := f.svc.GetFileOwner(ctx, f.game.ID, "alt", "a.esp")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Zero(t, f.src.calls.Load(), "a copy never asks a source anything")

	// The source profile is untouched.
	src, err := f.svc.GetInstalledMod(ctx, "src", "a", f.game.ID, "default")
	require.NoError(t, err)
	assert.True(t, src.Deployed)
	assert.Equal(t, domain.UpdatePinned, src.UpdatePolicy)
}

// TestCopyMod_IntoTheActiveProfileDeploysUnderItsLinkMethod is #437's
// second test: the active destination gets the files, linked the way IT
// links them, not the way the source profile does.
func TestCopyMod_IntoTheActiveProfileDeploysUnderItsLinkMethod(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	f.seedRow(t, "alt", "b", "2.0", map[string][]byte{"b.esp": []byte("B")}, false)
	f.setRefs(t, "alt", domain.ModReference{SourceID: "src", ModID: "b", Version: "2.0"})
	def, err := f.svc.NewProfileManager().Get(ctx, f.game.ID, "default")
	require.NoError(t, err)
	def.LinkMethod = domain.LinkCopy
	def.LinkMethodExplicit = true
	require.NoError(t, config.SaveProfile(f.svc.ConfigDir(), def))

	plan, err := f.svc.PlanCopyMod(ctx, f.game, core.CopyModRequest{From: "alt", To: "default", SourceID: "src", ModID: "b"})
	require.NoError(t, err)
	assert.True(t, plan.DestinationActive)
	assert.Equal(t, domain.LinkCopy, plan.LinkMethod)
	assert.Equal(t, domain.LinkSymlink, plan.SourceLinkMethod)
	assert.True(t, plan.LinkMethodDiffers)

	result, err := f.svc.ApplyCopyMod(ctx, f.game, plan, core.CopyModOptions{}, nil)
	require.NoError(t, err)
	require.Len(t, result.Copied, 1)
	assert.True(t, result.Copied[0].Deployed)

	deployed := filepath.Join(f.game.ModPath, "b.esp")
	info, err := os.Lstat(deployed)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "deployed by copy, the destination's method - not a symlink")

	row, err := f.svc.GetInstalledMod(ctx, "src", "b", f.game.ID, "default")
	require.NoError(t, err)
	assert.True(t, row.Enabled)
	assert.True(t, row.Deployed)
	assert.Equal(t, domain.LinkCopy, row.LinkMethod)
	owner, _, found, err := f.svc.GetFileOwner(ctx, f.game.ID, "default", "b.esp")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "src", owner)
	assert.Zero(t, f.src.calls.Load())
}

// TestCopyMod_RefusesAnExternalMod: a Workshop item is game-global, so
// there is nothing per-profile to copy.
func TestCopyMod_RefusesAnExternalMod(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "w", SourceID: "src", Name: "Workshop", Version: "1", GameID: f.game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, Deployed: true,
		External: true, ExternalPath: "/steam/w",
	}))

	_, err := f.svc.PlanCopyMod(ctx, f.game, core.CopyModRequest{From: "default", To: "alt", SourceID: "src", ModID: "w"})
	var ext *core.ExternalModError
	require.ErrorAs(t, err, &ext)
	assert.Equal(t, core.ReasonExternalNoCopy, ext.Reason)
}

// TestCopyMod_Refusals: what PlanCopyMod refuses before anything is
// planned.
func TestCopyMod_Refusals(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	f.seedRow(t, "default", "a", "1.0", map[string][]byte{"a.esp": []byte("A")}, false)
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "nocache", SourceID: "src", Name: "No cache", Version: "1", GameID: f.game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))

	_, err := f.svc.PlanCopyMod(ctx, f.game, core.CopyModRequest{From: "default", To: "default", SourceID: "src", ModID: "a"})
	require.Error(t, err, "a copy onto its own profile")
	_, err = f.svc.PlanCopyMod(ctx, f.game, core.CopyModRequest{From: "default", To: "ghost", SourceID: "src", ModID: "a"})
	require.ErrorIs(t, err, domain.ErrProfileNotFound)
	_, err = f.svc.PlanCopyMod(ctx, f.game, core.CopyModRequest{From: "default", To: "alt", SourceID: "src", ModID: "missing"})
	require.ErrorIs(t, err, domain.ErrModNotFound)
	_, err = f.svc.PlanCopyMod(ctx, f.game, core.CopyModRequest{From: "default", To: "alt", SourceID: "src", ModID: "nocache"})
	require.ErrorContains(t, err, "cache")
}

// TestCopyMod_DestinationAlreadyListsTheMod: the plan says so, Apply
// refuses with the typed error, and CopyModOptions.Replace is the answer
// (Ruling 1).
func TestCopyMod_DestinationAlreadyListsTheMod(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	f.seedRow(t, "default", "a", "2.0", map[string][]byte{"a.esp": []byte("A2")}, false)
	f.setRefs(t, "default", domain.ModReference{SourceID: "src", ModID: "a", Version: "2.0"})
	f.seedRow(t, "alt", "a", "1.0", map[string][]byte{"a.esp": []byte("A1")}, false)
	f.setRefs(t, "alt",
		domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0"},
		domain.ModReference{SourceID: "src", ModID: "z", Version: "9"})

	plan, err := f.svc.PlanCopyMod(ctx, f.game, core.CopyModRequest{From: "default", To: "alt", SourceID: "src", ModID: "a"})
	require.NoError(t, err)
	require.Len(t, plan.Mods, 1)
	assert.True(t, plan.Mods[0].AlreadyListed)
	assert.Equal(t, "1.0", plan.Mods[0].ExistingVersion)

	_, err = f.svc.ApplyCopyMod(ctx, f.game, plan, core.CopyModOptions{}, nil)
	var listed *core.ModAlreadyListedError
	require.ErrorAs(t, err, &listed)
	assert.Equal(t, "alt", listed.Profile)
	row, err := f.svc.GetInstalledMod(ctx, "src", "a", f.game.ID, "alt")
	require.NoError(t, err)
	assert.Equal(t, "1.0", row.Version, "a refusal writes nothing")

	result, err := f.svc.ApplyCopyMod(ctx, f.game, plan, core.CopyModOptions{Replace: true}, nil)
	require.NoError(t, err)
	require.Len(t, result.Copied, 1)
	assert.True(t, result.Copied[0].Replaced)
	alt, err := f.svc.NewProfileManager().Get(ctx, f.game.ID, "alt")
	require.NoError(t, err)
	assert.Equal(t, []domain.ModReference{
		{SourceID: "src", ModID: "a", Version: "2.0", FileIDs: []string{"f-a"}},
		{SourceID: "src", ModID: "z", Version: "9"},
	}, alt.Mods, "replaced in place, keeping its load-order position")
	row, err = f.svc.GetInstalledMod(ctx, "src", "a", f.game.ID, "alt")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.Version)
	assert.Zero(t, f.src.calls.Load())
}

// TestCopyMod_AllSkipsAndReportsRatherThanFailing: --all copies every mod
// of the source profile in load order, and an external or already-listed
// mod is skipped and reported, never fatal to the batch.
func TestCopyMod_AllSkipsAndReportsRatherThanFailing(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	f.seedRow(t, "default", "a", "1.0", map[string][]byte{"a.esp": []byte("A")}, false)
	f.seedRow(t, "default", "b", "1.0", map[string][]byte{"b.esp": []byte("B")}, false)
	f.seedRow(t, "default", "d", "1.0", map[string][]byte{"d.esp": []byte("D")}, false)
	require.NoError(t, f.svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "w", SourceID: "src", Name: "Workshop", Version: "1", GameID: f.game.ID},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, Deployed: true,
		External: true, ExternalPath: "/steam/w",
	}))
	f.setRefs(t, "default",
		domain.ModReference{SourceID: "src", ModID: "b", Version: "1.0"},
		domain.ModReference{SourceID: "src", ModID: "w", Version: "1"},
		domain.ModReference{SourceID: "src", ModID: "d", Version: "1.0"},
		domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0", Disabled: true})
	f.seedRow(t, "alt", "d", "0.9", map[string][]byte{"d.esp": []byte("D0")}, false)
	f.setRefs(t, "alt", domain.ModReference{SourceID: "src", ModID: "d", Version: "0.9"})

	plan, err := f.svc.PlanCopyMod(ctx, f.game, core.CopyModRequest{From: "default", To: "alt", All: true})
	require.NoError(t, err)
	var ids []string
	for _, m := range plan.Mods {
		ids = append(ids, m.ModID)
	}
	assert.Equal(t, []string{"b", "d", "a"}, ids, "load order")
	require.Len(t, plan.Skipped, 1)
	assert.Equal(t, "w", plan.Skipped[0].ModID)
	assert.Equal(t, core.ReasonExternalNoCopy, plan.Skipped[0].Reason)

	result, err := f.svc.ApplyCopyMod(ctx, f.game, plan, core.CopyModOptions{}, nil)
	require.NoError(t, err, "already-listed is skipped in a batch, not fatal")
	var copied []string
	for _, c := range result.Copied {
		copied = append(copied, c.ModID)
	}
	assert.Equal(t, []string{"b", "a"}, copied)
	var skipped []string
	for _, s := range result.Skipped {
		skipped = append(skipped, s.ModID)
	}
	assert.Equal(t, []string{"w", "d"}, skipped)

	alt, err := f.svc.NewProfileManager().Get(ctx, f.game.ID, "alt")
	require.NoError(t, err)
	assert.Equal(t, []domain.ModReference{
		{SourceID: "src", ModID: "d", Version: "0.9"},
		{SourceID: "src", ModID: "b", Version: "1.0", FileIDs: []string{"f-b"}},
		{SourceID: "src", ModID: "a", Version: "1.0", FileIDs: []string{"f-a"}, Disabled: true},
	}, alt.Mods, "the disabled marker is carried")
	rowA, err := f.svc.GetInstalledMod(ctx, "src", "a", f.game.ID, "alt")
	require.NoError(t, err)
	assert.False(t, rowA.Enabled, "a disabled mod copies disabled")
	_, err = f.svc.GetInstalledMod(ctx, "src", "w", f.game.ID, "alt")
	require.ErrorIs(t, err, domain.ErrModNotFound)
	assert.Zero(t, f.src.calls.Load())
}

// TestCopyMod_StalePlan: Apply re-checks the plan against the source and
// destination's installed state (Ruling 5).
func TestCopyMod_StalePlan(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	f.seedRow(t, "default", "a", "1.0", map[string][]byte{"a.esp": []byte("A")}, false)

	plan, err := f.svc.PlanCopyMod(ctx, f.game, core.CopyModRequest{From: "default", To: "alt", SourceID: "src", ModID: "a"})
	require.NoError(t, err)
	f.seedRow(t, "alt", "a", "1.0", nil, false)

	_, err = f.svc.ApplyCopyMod(ctx, f.game, plan, core.CopyModOptions{}, nil)
	require.ErrorIs(t, err, core.ErrStalePlan)
}

// TestCopyMod_ReplaceIntoTheActiveProfileMovesTheLiveVersion: replacing a
// mod the active destination has deployed at another version swaps the
// live files to the copied version instead of linking beside them.
func TestCopyMod_ReplaceIntoTheActiveProfileMovesTheLiveVersion(t *testing.T) {
	f := newCopyFixture(t)
	ctx := context.Background()
	f.seedRow(t, "default", "a", "1.0", map[string][]byte{"a.esp": []byte("old"), "old-only.esp": []byte("x")}, false)
	f.setRefs(t, "default", domain.ModReference{SourceID: "src", ModID: "a", Version: "1.0"})
	_, err := f.svc.DeployProfile(ctx, f.game, "default", core.DeployOptions{}, nil)
	require.NoError(t, err)
	f.seedRow(t, "alt", "a", "2.0", map[string][]byte{"a.esp": []byte("new")}, false)
	f.setRefs(t, "alt", domain.ModReference{SourceID: "src", ModID: "a", Version: "2.0"})

	plan, err := f.svc.PlanCopyMod(ctx, f.game, core.CopyModRequest{From: "alt", To: "default", SourceID: "src", ModID: "a"})
	require.NoError(t, err)
	_, err = f.svc.ApplyCopyMod(ctx, f.game, plan, core.CopyModOptions{Replace: true}, nil)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(f.game.ModPath, "a.esp"))
	require.NoError(t, err)
	assert.Equal(t, "new", string(got))
	assert.NoFileExists(t, filepath.Join(f.game.ModPath, "old-only.esp"), "the replaced version's own files go")
	row, err := f.svc.GetInstalledMod(ctx, "src", "a", f.game.ID, "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", row.Version)
	assert.True(t, row.Deployed)
	assert.Zero(t, f.src.calls.Load())
}
