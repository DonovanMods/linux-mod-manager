package core_test

// #660 (owner rulings 2026-11-15): a collection import is never all or
// nothing. It installs every item it can, and each item it could not stays
// in the profile with a to-do - why, and the one action that resolves it.

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// todoReasons is each to-do's reason, by mod id.
func todoReasons(todos []core.CollectionTodoItem) map[string]string {
	out := map[string]string{}
	for _, t := range todos {
		out[t.ModID] = t.Reason
	}
	return out
}

// newTodoFixture is the three-mod collection, every mod planned, with mod
// 11's download failing and mod 12 an installer the collection records no
// choices for.
func newTodoFixture(t *testing.T) *sourceImportFixture {
	t.Helper()
	f := newSourceImportFixture(t, source.TierPremium)
	f.src.urlErrs = map[string]error{"11": errors.New("HTTP 503 from the CDN")}
	f.src.AddDownload("120", fomodZip(t, "4K", "twelve"))
	return f
}

func TestCollectionImport_InstallsTheRestAndRecordsATodoPerFailure(t *testing.T) {
	f := newTodoFixture(t)
	plan := f.plan(t, core.SourceCollectionImportRequest{WithOptional: true})
	result, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)

	var incomplete *core.ProfileImportIncompleteError
	require.ErrorAs(t, err, &incomplete, "an import with to-dos is not a clean one")
	assert.False(t, result.Refused, "nothing refuses a collection import")
	assert.Equal(t, 1, result.Installed, "the item that works is installed")
	assert.Equal(t, 3, result.Total)
	require.Len(t, result.Todos, 2)
	assert.Equal(t, map[string]string{"11": domain.TodoDownloadFailed, "12": domain.TodoInstallerQuestions}, todoReasons(result.Todos))
	for _, todo := range result.Todos {
		switch todo.ModID {
		case "11":
			assert.Equal(t, core.TodoActionRetry, todo.Action)
			assert.Contains(t, todo.Detail, "HTTP 503")
		case "12":
			assert.Equal(t, core.TodoActionAnswerInstaller, todo.Action)
			assert.Equal(t, core.FomodReasonUnanswered, todo.FomodReason)
			assert.Contains(t, todo.Remedy, "answer it to continue")
		}
		assert.NotContains(t, todo.Remedy, "lmm update", "an installed mod's advice is not a collection item's")
	}
	_, err = f.svc.GetInstalledMod(context.Background(), "nexusmods", "10", "skyrim", "Three Mods")
	require.NoError(t, err, "the clean item has its row")

	saved, err := config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Three Mods")
	require.NoError(t, err)
	require.Len(t, saved.Mods, 3, "every item stays in the profile, in its place")
	assert.Nil(t, saved.FindRef("nexusmods", "10").Todo)
	require.NotNil(t, saved.FindRef("nexusmods", "11").Todo)
	assert.Equal(t, domain.TodoDownloadFailed, saved.FindRef("nexusmods", "11").Todo.Reason)
	assert.Equal(t, "Mod 11", saved.FindRef("nexusmods", "11").Todo.Name, "the name the source gave")
	require.NotNil(t, saved.FindRef("nexusmods", "12").Todo)
	assert.Equal(t, domain.TodoInstallerQuestions, saved.FindRef("nexusmods", "12").Todo.Reason)

	report, err := f.svc.CollectionTodos(context.Background(), f.game, "Three Mods")
	require.NoError(t, err)
	assert.Equal(t, 3, report.Total)
	assert.Equal(t, result.Todos, report.Todos, "the list the profile keeps is the one the import reported")
}

func TestCollectionImport_FreeAccountRecordsManualDownloadTodos(t *testing.T) {
	f := newSourceImportFixture(t, source.TierFree)
	result, err := f.svc.ApplyCollectionImport(context.Background(), f.game, f.plan(t, core.SourceCollectionImportRequest{}), core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err, "a free account's pending items are no failure: the exit stays clean, as before")
	assert.Equal(t, 2, result.Skipped)
	assert.Equal(t, map[string]string{"10": domain.TodoManualDownload, "11": domain.TodoManualDownload}, todoReasons(result.Todos))
	for _, todo := range result.Todos {
		assert.Equal(t, core.TodoActionDownload, todo.Action)
	}
}

func TestCollectionImport_AGonePinNothingStandsInForIsATodo(t *testing.T) {
	f := newGonePinFixture(t)
	f.src.listErr = map[string]error{"10": domain.ErrModNotFound}
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	require.Len(t, plan.Collection.Source.GonePins, 1)
	assert.Empty(t, plan.Collection.Source.GonePins[0].Choice, "nothing to install in its place")

	result, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	var incomplete *core.ProfileImportIncompleteError
	require.ErrorAs(t, err, &incomplete, "a gone pin no longer refuses the import, and leaves it unfinished")
	assert.Contains(t, err.Error(), "1 item(s) left to finish")
	assert.Equal(t, 1, result.Installed)
	require.Len(t, result.Todos, 1)
	todo := result.Todos[0]
	assert.Equal(t, domain.TodoGonePin, todo.Reason)
	assert.Equal(t, core.TodoActionChooseFile, todo.Action)
	assert.Equal(t, []string{"100"}, todo.FileIDs)
	assert.NotContains(t, todo.Remedy, "lmm update")
}

func TestCollectionImport_AGonePinDefaultsToTheCurrentFile(t *testing.T) {
	f := newGonePinFixture(t)
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	require.Len(t, plan.Collection.Source.GonePins, 1)
	pin := plan.Collection.Source.GonePins[0]
	assert.Equal(t, core.GonePinUseCurrent, pin.Choice, "the current file is the default")
	assert.True(t, pin.Default)
	assert.True(t, pin.CuratorExact, "the curator pinned the version exactly")
	assert.Contains(t, itemNamed(plan.Collection.Source, "Ten").Note, "the curator pinned 1 exactly")
	assert.Equal(t, []string{"101"}, plan.Profile.FindRef("nexusmods", "10").FileIDs)

	result, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err, "no decision is needed first")
	assert.Equal(t, 2, result.Installed)
	assert.Empty(t, result.Todos)

	skipped := f.plan(t, core.SourceCollectionImportRequest{ProfileName: "skipping", GonePins: core.GonePinChoices{Skip: []string{"nexusmods:10"}}})
	assert.Equal(t, core.GonePinSkip, skipped.Collection.Source.GonePins[0].Choice)
	assert.False(t, skipped.Collection.Source.GonePins[0].Default, "the user's choice")
}

// A pin the curator marked "latest" is not an exact pin.
func TestCollectionImport_AGonePinTheCuratorLetFollowIsNotExact(t *testing.T) {
	f := newSourceImportFixture(t, source.TierPremium)
	f.src.recipe.collection.Items[1].ModID = ""
	f.src.listing = map[string][]domain.DownloadableFile{"11": {{ID: "111", FileName: "f111.zip", Name: "Eleven 2", Version: "2", Category: "MAIN", IsPrimary: true}}}
	f.src.AddDownload("111", createTestZipBytes(t, map[string]string{"mod11.esp": "v2"}))
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	require.Len(t, plan.Collection.Source.GonePins, 1)
	pin := plan.Collection.Source.GonePins[0]
	assert.True(t, pin.Default)
	assert.False(t, pin.CuratorExact)
}

func TestCollectionImport_ACleanImportHasNoTodos(t *testing.T) {
	f := newSourceImportFixture(t, source.TierPremium)
	result, err := f.svc.ApplyCollectionImport(context.Background(), f.game, f.plan(t, core.SourceCollectionImportRequest{}), core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)
	assert.Empty(t, result.Todos)
	assert.Equal(t, 2, result.Total)
	assert.Equal(t, 2, result.Installed)
}

// importWithTodos imports newTodoFixture's collection, leaving mod 11 (a
// failed download) and mod 12 (an installer's questions) to do.
func importWithTodos(t *testing.T) *sourceImportFixture {
	t.Helper()
	f := newTodoFixture(t)
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, f.plan(t, core.SourceCollectionImportRequest{WithOptional: true}), core.ProfileImportOptions{Install: true}, nil)
	var incomplete *core.ProfileImportIncompleteError
	require.ErrorAs(t, err, &incomplete)
	return f
}

func (f *sourceImportFixture) finish(t *testing.T, req core.CollectionFinishRequest, answers map[string]domain.FomodChoices) (*core.CollectionFinishPlan, *core.CollectionFinishResult, error) {
	t.Helper()
	req.Profile = "Three Mods"
	plan, err := f.svc.PlanCollectionFinish(context.Background(), f.game, req)
	require.NoError(t, err)
	result, err := f.svc.ApplyCollectionFinish(context.Background(), f.game, plan, core.CollectionFinishOptions{Fomod: answers}, nil)
	return plan, result, err
}

func TestCollectionFinish_RetryInstallsAFailedDownload(t *testing.T) {
	f := importWithTodos(t)
	f.src.urlErrs = nil // the CDN is back

	plan, result, err := f.finish(t, core.CollectionFinishRequest{Retry: []string{"nexusmods:11"}}, nil)
	require.NoError(t, err)
	require.Len(t, plan.Install, 1)
	assert.Equal(t, core.FinishRetry, plan.Install[0].Choice)
	assert.Equal(t, 1, result.Installed)
	assert.Equal(t, map[string]string{"12": domain.TodoInstallerQuestions}, todoReasons(result.Todos), "only the installer is left")
	saved, err := config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Three Mods")
	require.NoError(t, err)
	assert.Nil(t, saved.FindRef("nexusmods", "11").Todo, "the install clears the record")
	assert.Equal(t, []string{"10", "11", "12"}, []string{saved.Mods[0].ModID, saved.Mods[1].ModID, saved.Mods[2].ModID}, "in its place in the load order")
}

func TestCollectionFinish_AnswersAnInstallerReusingItsDownload(t *testing.T) {
	f := importWithTodos(t)
	downloads := f.src.DownloadCount()

	plan, result, err := f.finish(t, core.CollectionFinishRequest{Retry: []string{"nexusmods:12"}}, nil)
	assert.Equal(t, 1, plan.ReusableDownloads, "the import kept the installer's archive")
	var q *core.FomodChoicesRequiredError
	require.ErrorAs(t, err, &q, "the installer asks; the question is answered and the finish run again")
	require.NotNil(t, result.FomodChoicesRequired)
	assert.Equal(t, map[string]string{"11": domain.TodoDownloadFailed, "12": domain.TodoInstallerQuestions}, todoReasons(result.Todos))

	sink, events := core.RecordEvents()
	plan, err = f.svc.PlanCollectionFinish(context.Background(), f.game, core.CollectionFinishRequest{Profile: "Three Mods", Retry: []string{"nexusmods:12"}})
	require.NoError(t, err)
	result, err = f.svc.ApplyCollectionFinish(context.Background(), f.game, plan, core.CollectionFinishOptions{Fomod: map[string]domain.FomodChoices{"nexusmods:12": answer("4K")}}, sink)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Installed)
	assert.Equal(t, 1, result.ReusedDownloads, "installed from the kept archive")
	assert.Equal(t, downloads, f.src.DownloadCount(), "nothing downloaded again")
	var reused bool
	for _, e := range *events {
		if p, ok := e.(core.StepEvent); ok && p.Phase == core.ImportDownloadReused {
			reused = true
		}
	}
	assert.True(t, reused, "the progress says the download is reused")
	saved, err := config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Three Mods")
	require.NoError(t, err)
	assert.False(t, saved.FindRef("nexusmods", "12").Fomod.IsZero(), "the answers are recorded")
}

func TestCollectionFinish_SkipDropsTheItemAndRecordsTheSkip(t *testing.T) {
	f := importWithTodos(t)
	_, result, err := f.finish(t, core.CollectionFinishRequest{Skip: []string{"nexusmods:11"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"nexusmods:11"}, result.Skipped)
	assert.Equal(t, 2, result.Total)
	saved, err := config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Three Mods")
	require.NoError(t, err)
	assert.Nil(t, saved.FindRef("nexusmods", "11"))
	require.NotNil(t, saved.CollectionBaseline)
	assert.Equal(t, []domain.CollectionSkip{{SourceID: "nexusmods", ModID: "11", Pin: domain.CollectionPin{Version: "1", FileIDs: []string{"110"}}}}, saved.CollectionBaseline.Skipped,
		"a collection update knows the user skipped it")
}

func TestCollectionFinish_UseCurrentForAGonePin(t *testing.T) {
	f := newGonePinFixture(t)
	f.src.listErr = map[string]error{"10": domain.ErrModNotFound}
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, f.plan(t, core.SourceCollectionImportRequest{}), core.ProfileImportOptions{Install: true}, nil)
	require.ErrorAs(t, err, new(*core.ProfileImportIncompleteError))
	f.src.listErr = nil // the mod page is back, with a current file

	plan, result, err := f.finish(t, core.CollectionFinishRequest{UseCurrent: []string{"nexusmods:10"}}, nil)
	require.NoError(t, err)
	require.Len(t, plan.Install, 1)
	assert.Equal(t, &core.CollectionCurrentFile{FileID: "101", Name: "Ten 2", Version: "2"}, plan.Install[0].Current)
	assert.Equal(t, 1, result.Installed)
	assert.Empty(t, result.Todos)
	saved, err := config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Three Mods")
	require.NoError(t, err)
	ref := saved.FindRef("nexusmods", "10")
	assert.Equal(t, []string{"101"}, ref.FileIDs)
	assert.Equal(t, &domain.CollectionPin{Version: "1", FileIDs: []string{"100"}}, ref.SubstitutedFor)
}

func TestCollectionFinish_RefusesWhatAnItemDoesNotOffer(t *testing.T) {
	f := importWithTodos(t)
	for name, req := range map[string]core.CollectionFinishRequest{
		"not on the list":     {Retry: []string{"nexusmods:10"}},
		"use current, no pin": {UseCurrent: []string{"nexusmods:11"}},
		"skip file, no pin":   {SkipFile: []string{"nexusmods:11"}},
		"named two ways":      {Retry: []string{"nexusmods:11"}, Skip: []string{"nexusmods:11"}},
	} {
		req.Profile = "Three Mods"
		_, err := f.svc.PlanCollectionFinish(context.Background(), f.game, req)
		assert.True(t, core.IsBadFinishChoice(err), "%s: %v", name, err)
	}
	plan, err := f.svc.PlanCollectionFinish(context.Background(), f.game, core.CollectionFinishRequest{Profile: "Three Mods"})
	require.NoError(t, err)
	assert.True(t, plan.NoChanges, "naming nothing lists the items and does nothing")
	assert.Len(t, plan.Todos, 2)
}

// #660 ruling 3: an unfinished collection deploys, switches and applies,
// leaving its to-dos alone and warning about them.
func TestCollectionTodos_DeployAndApplyWarnAndLeaveThemAlone(t *testing.T) {
	f := importWithTodos(t)
	ctx := context.Background()
	live, err := f.svc.NewProfileManager().GetDefault(ctx, f.game.ID)
	require.NoError(t, err)
	require.Equal(t, "Three Mods", live.Name, "the import made it the active profile")

	apply, err := f.svc.PlanProfileApply(ctx, f.game, "Three Mods")
	require.NoError(t, err)
	require.NotNil(t, apply.Unresolved)
	assert.Len(t, apply.Unresolved.Items, 2)
	assert.Contains(t, apply.Unresolved.Warning, "the game may not behave as the curator intended")
	assert.Empty(t, apply.ToInstall, "the apply leaves the to-dos to Finish collection")

	deploy, err := f.svc.PlanDeploy(ctx, f.game, "Three Mods", core.DeployOptions{})
	require.NoError(t, err)
	require.NotNil(t, deploy.Unresolved, "the deploy warns")
	assert.Contains(t, deploy.Unresolved.Warning, "Mod 11")
	_, err = f.svc.ApplyDeploy(ctx, f.game, deploy, core.DeployOptions{}, nil)
	require.NoError(t, err, "an unfinished collection deploys")

	status, err := f.svc.GameStatus(ctx, f.game)
	require.NoError(t, err)
	require.NotNil(t, status.ProfileDrift)
	assert.Equal(t, 2, status.ProfileDrift.Unresolved, "the drift summary counts them")
	assert.Zero(t, status.ProfileDrift.NotInstalled)
}

// #660 review S1/S2: switching to an unfinished collection leaves its
// to-dos alone and warns about every one - even when the profile switched
// away from has those mods installed, which must not be enabled or
// converged into the collection.
func TestCollectionTodos_SwitchingBackLeavesThemAloneWhateverTheOutgoingProfileHas(t *testing.T) {
	f := importWithTodos(t)
	ctx := context.Background()
	// Another profile of the same collection, with every mod installed.
	f.src.urlErrs = nil
	other := f.plan(t, core.SourceCollectionImportRequest{WithOptional: true, ProfileName: "Other"})
	_, err := f.svc.ApplyCollectionImport(ctx, f.game, other, core.ProfileImportOptions{Install: true,
		Fomod: map[string]domain.FomodChoices{"nexusmods:12": answer("4K")}}, nil)
	require.NoError(t, err)

	_, err = f.svc.GetInstalledMod(ctx, "nexusmods", "11", "skyrim", "Other")
	require.NoError(t, err, "the outgoing profile has mod 11")

	away, err := f.svc.PlanProfileSwitch(ctx, f.game, "Other")
	require.NoError(t, err)
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, away, nil)
	require.NoError(t, err)
	live, err := f.svc.NewProfileManager().GetDefault(ctx, f.game.ID)
	require.NoError(t, err)
	require.Equal(t, "Other", live.Name)

	back, err := f.svc.PlanProfileSwitch(ctx, f.game, "Three Mods")
	require.NoError(t, err)
	require.NotNil(t, back.Unresolved, "the switch warns")
	assert.Contains(t, back.Unresolved.Warning, "collection incomplete: 2 item(s) unresolved")
	assert.Equal(t, map[string]string{"11": domain.TodoDownloadFailed, "12": domain.TodoInstallerQuestions}, todoReasons(back.Unresolved.Items),
		"every to-do is named, the ones the outgoing profile has included")
	for _, ref := range back.ToInstall {
		assert.NotContains(t, []string{"11", "12"}, ref.ModID, "a to-do is not installed mid-switch")
	}
	for _, row := range back.ToEnable {
		assert.NotContains(t, []string{"11", "12"}, row.ID, "nor enabled from another profile's copy")
	}
	_, err = f.svc.ApplyProfileSwitch(ctx, f.game, back, nil)
	require.NoError(t, err, "an unfinished collection switches")
	_, err = f.svc.GetInstalledMod(ctx, "nexusmods", "11", "skyrim", "Three Mods")
	assert.Error(t, err, "still left to finish")
	report, err := f.svc.CollectionTodos(ctx, f.game, "Three Mods")
	require.NoError(t, err)
	assert.Len(t, report.Todos, 2)
}

// #660: a collection update of a profile with items left to finish retries
// them as the new version pins them, and leaves to finish what fails now.
func TestCollectionUpdate_WithItemsLeftToFinish(t *testing.T) {
	f := newUpdateFixture(t)
	ctx := context.Background()
	f.src.urlErrs = map[string]error{"10": errors.New("HTTP 503 from the CDN")}
	plan, err := f.svc.PlanSourceCollectionImport(ctx, f.game, core.SourceCollectionImportRequest{SourceID: "nexusmods", Ref: "abcdef", ProfileName: "coll"})
	require.NoError(t, err)
	result, err := f.svc.ApplyCollectionImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	var incomplete *core.ProfileImportIncompleteError
	require.ErrorAs(t, err, &incomplete)
	assert.Equal(t, map[string]string{"10": domain.TodoDownloadFailed}, todoReasons(result.Todos))

	f.bumpNexus(t)
	f.src.urlErrs = map[string]error{"13": errors.New("HTTP 502 from the CDN")}
	update, err := f.svc.ApplyCollectionUpdate(ctx, f.game, f.planUpdate(t), core.CollectionUpdateOptions{}, nil)
	require.ErrorAs(t, err, &incomplete, "an update that leaves items to finish is not a clean one")
	assert.Equal(t, 4, f.profile(t).Collection.Version, "the update applied")
	require.NotNil(t, update.Import)
	assert.Equal(t, map[string]string{"13": domain.TodoDownloadFailed}, todoReasons(update.Import.Todos),
		"the item left to finish installed at its new pin; the new one that failed is left to finish")
	saved := f.profile(t)
	assert.Nil(t, saved.FindRef("nexusmods", "10").Todo)
	require.NotNil(t, saved.FindRef("nexusmods", "13").Todo)
	row, err := f.svc.GetInstalledMod(ctx, "nexusmods", "10", "skyrim", "coll")
	require.NoError(t, err)
	assert.Equal(t, []string{"101"}, row.FileIDs)
}

// #660 review S6: a finish that moves a gone pin to the current file and
// stops before the install leaves the ref and its to-do agreeing - not
// installed at the new pin, a retry - and finishing again installs it
// once, never with the current file twice.
func TestCollectionFinish_AStoppedUseCurrentIsRetriedCleanly(t *testing.T) {
	f := newGonePinFixture(t)
	f.src.listErr = map[string]error{"10": domain.ErrModNotFound}
	_, err := f.svc.ApplyCollectionImport(context.Background(), f.game, f.plan(t, core.SourceCollectionImportRequest{}), core.ProfileImportOptions{Install: true}, nil)
	require.ErrorAs(t, err, new(*core.ProfileImportIncompleteError))
	f.src.listErr = nil

	plan, err := f.svc.PlanCollectionFinish(context.Background(), f.game, core.CollectionFinishRequest{Profile: "Three Mods", UseCurrent: []string{"nexusmods:10"}})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopAtInstall := func(e core.Event) {
		if p, ok := e.(core.StepEvent); ok && p.Phase == core.ImportInstalling {
			cancel()
		}
	}
	_, err = f.svc.ApplyCollectionFinish(ctx, f.game, plan, core.CollectionFinishOptions{}, stopAtInstall)
	require.ErrorIs(t, err, context.Canceled)
	saved, err := config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Three Mods")
	require.NoError(t, err)
	ref := saved.FindRef("nexusmods", "10")
	assert.Equal(t, []string{"101"}, ref.FileIDs)
	require.NotNil(t, ref.Todo)
	assert.Equal(t, domain.TodoNotInstalled, ref.Todo.Reason, "the to-do moved with the pin")

	_, err = f.svc.PlanCollectionFinish(context.Background(), f.game, core.CollectionFinishRequest{Profile: "Three Mods", UseCurrent: []string{"nexusmods:10"}})
	assert.True(t, core.IsBadFinishChoice(err), "it is no gone pin any more: %v", err)
	_, result, err := f.finish(t, core.CollectionFinishRequest{Retry: []string{"nexusmods:10"}}, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, result.Installed)
	saved, err = config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Three Mods")
	require.NoError(t, err)
	assert.Equal(t, []string{"101"}, saved.FindRef("nexusmods", "10").FileIDs, "the current file once")
	assert.Nil(t, saved.FindRef("nexusmods", "10").Todo)
}

// #660 review S3: an import cancelled part-way keeps the record of every
// item it left to do - the one it held back from the start (saved with the
// profile itself) and the one that failed before the cancellation.
func TestCollectionImport_ACancelledImportKeepsEveryRecord(t *testing.T) {
	f := newGonePinFixture(t)
	f.src.listErr = map[string]error{"10": domain.ErrModNotFound} // a held gone pin
	f.src.urlErrs = map[string]error{"11": errors.New("HTTP 503 from the CDN")}
	plan := f.plan(t, core.SourceCollectionImportRequest{WithOptional: true})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelAtTheLast := func(e core.Event) {
		if p, ok := e.(core.ModEvent); ok && p.Phase == core.ImportModInstalled && p.Mod != nil && p.Mod.ModID == "12" {
			cancel()
		}
	}
	_, _ = f.svc.ApplyCollectionImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true}, cancelAtTheLast)
	require.Error(t, ctx.Err(), "the import was cancelled")

	saved, err := config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Three Mods")
	require.NoError(t, err)
	require.NotNil(t, saved.FindRef("nexusmods", "10").Todo)
	assert.Equal(t, domain.TodoGonePin, saved.FindRef("nexusmods", "10").Todo.Reason)
	require.NotNil(t, saved.FindRef("nexusmods", "11").Todo, "the failure before the cancellation is recorded")
	assert.Equal(t, domain.TodoDownloadFailed, saved.FindRef("nexusmods", "11").Todo.Reason)
}

// #660 review S3: a held item's record is saved with the profile itself,
// before anything installs - so an import stopped before its end still
// lists why.
func TestCollectionImport_HeldRecordsAreSavedWithTheProfile(t *testing.T) {
	f := newGonePinFixture(t)
	f.src.listErr = map[string]error{"10": domain.ErrModNotFound}
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelOnSave := func(e core.Event) {
		if p, ok := e.(core.StepEvent); ok && p.Phase == core.ImportSaved {
			cancel()
		}
	}
	_, err := f.svc.ApplyCollectionImport(ctx, f.game, plan, core.ProfileImportOptions{Install: true}, cancelOnSave)
	require.Error(t, err)
	saved, err := config.LoadProfile(f.svc.ConfigDir(), "skyrim", "Three Mods")
	require.NoError(t, err)
	require.NotNil(t, saved.FindRef("nexusmods", "10").Todo)
	assert.Equal(t, domain.TodoGonePin, saved.FindRef("nexusmods", "10").Todo.Reason)
}

// #660 review: a mod its source deleted is no download to retry - it is
// gone, and skipping it is the action.
func TestCollectionImport_ADeletedModIsGoneNotADownloadFailure(t *testing.T) {
	f := newSourceImportFixture(t, source.TierPremium)
	plan := f.plan(t, core.SourceCollectionImportRequest{})
	delete(f.src.mods, "skyrimspecialedition/11")
	result, err := f.svc.ApplyCollectionImport(context.Background(), f.game, plan, core.ProfileImportOptions{Install: true}, nil)
	require.ErrorAs(t, err, new(*core.ProfileImportIncompleteError))
	require.Len(t, result.Todos, 1)
	assert.Equal(t, domain.TodoModGone, result.Todos[0].Reason)
	assert.Equal(t, core.TodoActionSkip, result.Todos[0].Action)
	assert.NotContains(t, result.Todos[0].Remedy, "retry it")
}

// #660 review: an installer left to finish says so - not "run the same
// command again", which is a single install's advice.
func TestCollectionImport_AnInstallersFailureNamesTheFinish(t *testing.T) {
	f := newTodoFixture(t)
	result, err := f.svc.ApplyCollectionImport(context.Background(), f.game, f.plan(t, core.SourceCollectionImportRequest{WithOptional: true}), core.ProfileImportOptions{Install: true}, nil)
	require.ErrorAs(t, err, new(*core.ProfileImportIncompleteError))
	for _, failure := range result.Failures {
		if failure.ModID != "12" {
			continue
		}
		assert.NotContains(t, failure.Reason, "run the same command again")
		assert.NotContains(t, failure.Reason, "download failed")
		assert.Contains(t, failure.Reason, "left to finish: answer its installer")
	}
	assert.NotContains(t, err.Error(), "run the same command again")
}

// #660 review: a gone pin's to-do suggests what can resolve it - skipping
// it (or its gone file), or a retry once the file is back - never a current
// file there was none of.
func TestCollectionTodo_AGonePinsRemedyNeverSuggestsAMissingCurrentFile(t *testing.T) {
	f := newGonePinFixture(t)
	f.src.listErr = map[string]error{"10": domain.ErrModNotFound}
	result, _ := f.svc.ApplyCollectionImport(context.Background(), f.game, f.plan(t, core.SourceCollectionImportRequest{}), core.ProfileImportOptions{Install: true}, nil)
	require.Len(t, result.Todos, 1)
	assert.NotContains(t, result.Todos[0].Remedy, "current file")
	assert.Contains(t, result.Todos[0].Remedy, "skip it")
}
