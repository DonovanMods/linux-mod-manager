package core_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #664: an update reported "Download done" and then nothing at all while it
// extracted, ran its hooks, replaced the deployed files, recorded the new
// version and synced the profile's lists. These pin the steps it now names
// after the download, in order, for one mod - ApplyUpdate and
// ApplyUpdateFromArchive alike, since both end in commitUpdate.

// updateStep is one step event, as the sequence tests compare them.
type updateStep struct {
	Phase  core.DeployPhase
	Detail string
}

// updateSteps is the ordered list of step and mod events in seen (the
// download ticks and warnings left out), each checked to name modName and
// carry the mod's ref.
func updateSteps(t *testing.T, seen []core.Event, modName, sourceID, modID string) []updateStep {
	t.Helper()
	var steps []updateStep
	for _, e := range seen {
		var scope core.Scope
		var step updateStep
		switch ev := e.(type) {
		case core.StepEvent:
			scope, step = ev.Scope, updateStep{Phase: ev.Phase, Detail: ev.Detail}
		case core.ModEvent:
			scope, step = ev.Scope, updateStep{Phase: ev.Phase, Detail: ev.Version}
		default:
			continue
		}
		assert.Equal(t, core.OpUpdate, scope.Op, "%s", step.Phase)
		assert.Equal(t, modName, scope.ModName, "%s names the mod", step.Phase)
		if assert.NotNil(t, scope.Mod, "%s carries the mod's ref", step.Phase) {
			assert.Equal(t, sourceID, scope.Mod.SourceID)
			assert.Equal(t, modID, scope.Mod.ModID)
		}
		steps = append(steps, step)
	}
	return steps
}

// seedAllUpdateHooks configures every hook an update runs, each a script
// that succeeds.
func seedAllUpdateHooks(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	dir := t.TempDir()
	ok := func(name string) string {
		return createTestScript(t, dir, name, "#!/bin/bash\nexit 0")
	}
	seedHooks(t, svc, game, "default", domain.GameHooks{
		Install:   domain.HookConfig{BeforeEach: ok("ib.sh"), AfterEach: ok("ia.sh")},
		Uninstall: domain.HookConfig{BeforeEach: ok("ub.sh"), AfterEach: ok("ua.sh")},
	})
}

func TestService_ApplyUpdate_NamesEveryStepAfterTheDownload(t *testing.T) {
	for _, tc := range []struct {
		name      string
		skipHooks bool
		want      []updateStep
	}{
		{
			name: "with hooks",
			want: []updateStep{
				{Phase: core.ArchiveExtracting},
				{Phase: core.UpdateDownloadDone},
				{Phase: core.UpdateRunningHook, Detail: "uninstall.before_each"},
				{Phase: core.UpdateRunningHook, Detail: "install.before_each"},
				{Phase: core.UpdateInstalling},
				{Phase: core.UpdateRunningHook, Detail: "uninstall.after_each"},
				{Phase: core.UpdateRunningHook, Detail: "install.after_each"},
				{Phase: core.UpdateRecording},
				{Phase: core.UpdateModUpdated, Detail: "2.0"},
			},
		},
		{
			// A skipped hook is not a step: nothing runs.
			name:      "hooks skipped",
			skipHooks: true,
			want: []updateStep{
				{Phase: core.ArchiveExtracting},
				{Phase: core.UpdateDownloadDone},
				{Phase: core.UpdateInstalling},
				{Phase: core.UpdateRecording},
				{Phase: core.UpdateModUpdated, Detail: "2.0"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFlowsTestService(t)
			game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
			old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"},
				map[string][]byte{"mod1-old.esp": []byte("old-content")})
			seedAllUpdateHooks(t, svc, game)

			mock := &multiFileDownloadSource{
				mockSourceWithDownloads: newMockSourceWithDownloads("src"),
				files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-2.0.zip", IsPrimary: true}},
			}
			defer mock.Close()
			svc.RegisterSource(mock)
			mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
			mock.AddDownload("new-1", createTestZipBytes(t, map[string]string{"mod1-new.esp": "new-content"}))

			plan, err := svc.NewUpdatePlanForApplyTest(context.Background(), game.ID, "default",
				domain.Update{InstalledMod: *old, NewVersion: "2.0"})
			require.NoError(t, err)
			sink, seen := core.RecordEvents()
			_, err = svc.ApplyUpdate(context.Background(), game, plan, core.UpdateOptions{SkipHooks: tc.skipHooks}, sink)
			require.NoError(t, err)

			assert.Equal(t, tc.want, updateSteps(t, *seen, "Mod One", "src", "mod1"))
		})
	}
}

// The extracting step names the file being extracted.
func TestService_ApplyUpdate_ExtractingNamesTheFile(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")})
	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-2.0.zip", IsPrimary: true}},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	mock.AddDownload("new-1", createTestZipBytes(t, map[string]string{"mod1-new.esp": "new-content"}))

	plan, err := svc.NewUpdatePlanForApplyTest(context.Background(), game.ID, "default",
		domain.Update{InstalledMod: *old, NewVersion: "2.0"})
	require.NoError(t, err)
	sink, seen := core.RecordEvents()
	_, err = svc.ApplyUpdate(context.Background(), game, plan, core.UpdateOptions{}, sink)
	require.NoError(t, err)

	var extracting []core.StepEvent
	for _, e := range *seen {
		if s, ok := e.(core.StepEvent); ok && s.Phase == core.ArchiveExtracting {
			extracting = append(extracting, s)
		}
	}
	require.Len(t, extracting, 1)
	require.NotNil(t, extracting[0].File)
	assert.Equal(t, "new-1", extracting[0].File.ID)
}

// A file the update installs whole (no archive to extract) has no
// extracting step.
func TestService_ApplyUpdate_NoExtractingStepForAPlainFile(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")})
	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-new.esp", IsPrimary: true}},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	mock.AddDownload("new-1", []byte("new-content"))

	plan, err := svc.NewUpdatePlanForApplyTest(context.Background(), game.ID, "default",
		domain.Update{InstalledMod: *old, NewVersion: "2.0"})
	require.NoError(t, err)
	sink, seen := core.RecordEvents()
	_, err = svc.ApplyUpdate(context.Background(), game, plan, core.UpdateOptions{}, sink)
	require.NoError(t, err)

	assert.Equal(t, []updateStep{
		{Phase: core.UpdateDownloadDone},
		{Phase: core.UpdateInstalling},
		{Phase: core.UpdateRecording},
		{Phase: core.UpdateModUpdated, Detail: "2.0"},
	}, updateSteps(t, *seen, "Mod One", "src", "mod1"))
}

// A failed before_each hook stops the update before it installs, and it is
// never reported updated.
func TestService_ApplyUpdate_FailedBeforeEachHookStopsBeforeInstalling(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	old := seedUpdatableMod(t, svc, game, "src", "mod1", "Mod One", "1.0", []string{"old-1"},
		map[string][]byte{"mod1-old.esp": []byte("old-content")})
	seedHooks(t, svc, game, "default", domain.GameHooks{
		Install: domain.HookConfig{BeforeEach: createTestScript(t, t.TempDir(), "fail.sh", "#!/bin/bash\nexit 1")},
	})
	mock := &multiFileDownloadSource{
		mockSourceWithDownloads: newMockSourceWithDownloads("src"),
		files:                   []domain.DownloadableFile{{ID: "new-1", Name: "New File", FileName: "mod1-new.esp", IsPrimary: true}},
	}
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddMod("g1", &domain.Mod{ID: "mod1", SourceID: "src", Name: "Mod One", Version: "2.0", GameID: "g1"})
	mock.AddDownload("new-1", []byte("new-content"))

	plan, err := svc.NewUpdatePlanForApplyTest(context.Background(), game.ID, "default",
		domain.Update{InstalledMod: *old, NewVersion: "2.0"})
	require.NoError(t, err)
	sink, seen := core.RecordEvents()
	_, err = svc.ApplyUpdate(context.Background(), game, plan, core.UpdateOptions{}, sink)
	require.Error(t, err)

	assert.Equal(t, []updateStep{
		{Phase: core.UpdateDownloadDone},
		{Phase: core.UpdateRunningHook, Detail: "install.before_each"},
	}, updateSteps(t, *seen, "Mod One", "src", "mod1"))
}

func TestUpdateFromArchive_NamesEveryStepAfterTheArchive(t *testing.T) {
	f := newFromFileFixture(t)
	seedAllUpdateHooks(t, f.svc, f.game)
	archive := f.archive(t, "Auctionator-2.0.zip")
	plan := f.plan(t, archive, core.UpdateFromArchiveOptions{})

	sink, seen := core.RecordEvents()
	_, err := f.svc.ApplyUpdateFromArchive(context.Background(), f.game, plan, core.UpdateFromArchiveOptions{}, sink)
	require.NoError(t, err)

	assert.Equal(t, []updateStep{
		{Phase: core.ArchiveExtracting},
		{Phase: core.UpdateRunningHook, Detail: "uninstall.before_each"},
		{Phase: core.UpdateRunningHook, Detail: "install.before_each"},
		{Phase: core.UpdateInstalling},
		{Phase: core.UpdateRunningHook, Detail: "uninstall.after_each"},
		{Phase: core.UpdateRunningHook, Detail: "install.after_each"},
		{Phase: core.UpdateRecording},
		{Phase: core.UpdateModUpdated, Detail: "2.0"},
	}, updateSteps(t, *seen, "Auctionator", "msrc", "42"))
}

// The new phases are part of the event vocabulary: each has a wire name
// that round-trips.
func TestUpdateProgressPhases_WireNames(t *testing.T) {
	for phase, name := range map[core.DeployPhase]string{
		core.ArchiveExtracting: "archive_extracting",
		core.UpdateRunningHook: "update_running_hook",
		core.UpdateInstalling:  "update_installing",
		core.UpdateRecording:   "update_recording",
		core.UpdateModUpdated:  "update_mod_updated",
	} {
		assert.Equal(t, name, phase.String())
		var back core.DeployPhase
		require.NoError(t, back.UnmarshalText([]byte(name)))
		assert.Equal(t, phase, back)
	}
}

// #664: deploy's cache-miss redownload went silent the same way between its
// last download tick and its "download done" while the archive extracted.
// The shared extracting step is forwarded under the deploy's own scope, so
// it carries the mod's batch position like the rest of its lines.
func TestService_DeployProfile_RedownloadNamesItsExtraction(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}

	mock := newMockSourceWithDownloads("src")
	defer mock.Close()
	svc.RegisterSource(mock)
	mock.AddDownload("1", createTestZipBytes(t, map[string]string{"plugin.esp": "payload"}))
	mockMod := &domain.Mod{ID: "1", SourceID: "src", Name: "Redownload Mod", Version: "1.0", GameID: "g1"}
	mock.AddMod("g1", mockMod)
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod: *mockMod, ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true,
	}))
	seedProfileWithMod(t, svc, "g1", "default", "src", "1", "1.0")

	sink, seen := core.RecordEvents()
	_, err := svc.DeployProfile(context.Background(), game, "default", core.DeployOptions{}, sink)
	require.NoError(t, err)

	var phases []core.DeployPhase
	for _, e := range *seen {
		fe, ok := e.(core.FlowEvent)
		if !ok || fe.EventScope().ModName != "Redownload Mod" {
			continue
		}
		if _, tick := e.(core.DownloadEvent); tick {
			continue
		}
		phases = append(phases, fe.FlowPhase())
		if fe.FlowPhase() == core.ArchiveExtracting {
			assert.Equal(t, core.OpDeploy, fe.EventScope().Op)
			assert.Equal(t, 1, fe.EventScope().Index)
			assert.Equal(t, 1, fe.EventScope().Total)
		}
	}
	assert.Equal(t, []core.DeployPhase{
		core.DeployRedownloading, core.ArchiveExtracting, core.DeployDownloadDone, core.DeployDeployed,
	}, phases)
}
