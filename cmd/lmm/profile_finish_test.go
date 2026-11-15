package main

// #660: `lmm profile finish` lists a collection profile's items left to do,
// each with the command that resolves it, and resolves them; a collection
// import's summary says how many need the user and ends with the command to
// run next.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setFinishFlags sets `profile finish`'s --retry, and clears the rest.
func setFinishFlags(t *testing.T, retry []string) {
	t.Helper()
	setFinishActions(t, retry, nil, nil, nil)
}

// setFinishActions sets `profile finish`'s action flags.
func setFinishActions(t *testing.T, retry, useCurrent, skipFile, skip []string) {
	t.Helper()
	old := [][]string{profileFinishRetry, profileFinishUseCurrent, profileFinishSkipFile, profileFinishSkip}
	oldFrom, oldDry := profileFinishFrom, profileFinishDryRun
	profileFinishRetry, profileFinishUseCurrent, profileFinishSkipFile, profileFinishSkip = retry, useCurrent, skipFile, skip
	profileFinishFrom, profileFinishDryRun = "", false
	t.Cleanup(func() {
		profileFinishRetry, profileFinishUseCurrent, profileFinishSkipFile, profileFinishSkip = old[0], old[1], old[2], old[3]
		profileFinishFrom, profileFinishDryRun = oldFrom, oldDry
	})
}

// importLeavingTodos imports twoFileTenRecipe on the free account: Ten's
// optional file is gone with nothing in its place (a gone_pin to-do that
// can leave the file out), and nothing is served downloads.
func importLeavingTodos(t *testing.T) (*core.Service, *domain.Game, string, string) {
	t.Helper()
	svc, game, configDir := nexusCollectionServiceAs(t, twoFileTenRecipe, "skyrimspecialedition", func(d *nexusCollectionDouble) source.ModSource {
		return twoFileTenDouble{d}
	})
	setNexusFlags(t, "abcdef", false)
	out, _, err := captureStdoutStderrErr(t, func() error { return doNexusCollectionImport(context.Background(), svc, game) })
	require.NoError(t, err, out)
	return svc, game, configDir, out
}

func TestDoNexusCollectionImport_SummaryEndsWithTheNextCommand(t *testing.T) {
	_, _, _, out := importLeavingTodos(t)
	assert.Contains(t, out, "Imported 0 of 1 mod; 1 need you:")
	assert.Contains(t, out, "! Ten (nexusmods:10): the source no longer offers a file the collection pins for Ten: leave that file out and install the rest, or skip it")
	assert.Contains(t, out, `lmm profile finish "Three Mods" --skip-file 10 (or --skip 10)`)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Equal(t, `lmm profile finish "Three Mods"`, lines[len(lines)-1], "the last line is the next command")
}

func TestDoProfileFinish_ListsTheItems(t *testing.T) {
	svc, game, _, _ := importLeavingTodos(t)
	setFinishActions(t, nil, nil, nil, nil)
	out, err := captureStdoutErr(t, func() error { return doProfileFinish(context.Background(), svc, game, "Three Mods") })
	require.NoError(t, err)
	assert.Contains(t, out, "Finish collection Three Mods: 1 of 1 need you")
	assert.Contains(t, out, `lmm profile finish "Three Mods" --skip-file 10 (or --skip 10)`)

	withJSONOutput(t)
	out, err = captureStdoutErr(t, func() error { return doProfileFinish(context.Background(), svc, game, "Three Mods") })
	require.NoError(t, err)
	var report core.CollectionTodoReport
	require.NoError(t, json.Unmarshal([]byte(out), &report), out)
	require.Len(t, report.Todos, 1)
	assert.Equal(t, domain.TodoGonePin, report.Todos[0].Reason)
	assert.Equal(t, core.TodoActionChooseFile, report.Todos[0].Action)
	assert.True(t, report.Todos[0].CanSkipFile)
}

func TestDoProfileFinish_SkipResolvesAndRecordsIt(t *testing.T) {
	svc, game, configDir, _ := importLeavingTodos(t)
	setFinishActions(t, nil, nil, nil, []string{"10"})
	out, err := captureStdoutErr(t, func() error { return doProfileFinish(context.Background(), svc, game, "Three Mods") })
	require.NoError(t, err, out)
	assert.Contains(t, out, "- Ten (nexusmods:10): skip")
	assert.Contains(t, out, "Collection Three Mods is finished")
	saved, err := config.LoadProfile(configDir, game.ID, "Three Mods")
	require.NoError(t, err)
	assert.Nil(t, saved.FindRef("nexusmods", "10"))
	require.NotNil(t, saved.CollectionBaseline)
	assert.Len(t, saved.CollectionBaseline.Skipped, 1)
}

func TestDoProfileFinish_SkipFileJSON(t *testing.T) {
	svc, game, configDir, _ := importLeavingTodos(t)
	setFinishActions(t, nil, nil, []string{"nexusmods:10"}, nil)
	withJSONOutput(t)
	out, err := captureStdoutErr(t, func() error { return doProfileFinish(context.Background(), svc, game, "Three Mods") })
	// The double serves no download for the rest of the mod: the item
	// fails again, for that reason now, and the document says so.
	require.Error(t, err)
	var finished *core.CollectionFinishIncompleteError
	require.ErrorAs(t, err, &finished)
	require.Len(t, finished.Result.Todos, 1)
	assert.NotEqual(t, domain.TodoGonePin, finished.Result.Todos[0].Reason, "the gone file is left out; what is left is the rest's download")
	assert.Empty(t, out, "the error envelope is the document")
	saved, err := config.LoadProfile(configDir, game.ID, "Three Mods")
	require.NoError(t, err)
	assert.Equal(t, []string{"100"}, saved.FindRef("nexusmods", "10").FileIDs)
}

func TestDoProfileFinish_RefusesAModNotOnTheList(t *testing.T) {
	svc, game, _, _ := importLeavingTodos(t)
	setFinishActions(t, []string{"12"}, nil, nil, nil)
	_, err := captureStdoutErr(t, func() error { return doProfileFinish(context.Background(), svc, game, "Three Mods") })
	require.Error(t, err)
	assert.True(t, core.IsBadFinishChoice(err), "got %v", err)
	assert.Contains(t, err.Error(), "--retry 12")
}

func TestPrintIncomplete_WarnsOnStderr(t *testing.T) {
	_, stderr, err := captureStdoutStderrErr(t, func() error {
		printIncomplete(&core.CollectionIncomplete{Warning: "collection incomplete: 1 item(s) unresolved (FSMP); the game may not behave as the curator intended"})
		printIncomplete(nil)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, "Warning: collection incomplete: 1 item(s) unresolved (FSMP); the game may not behave as the curator intended\n", stderr)
}

// TestReportError_JSON_CollectionFinishIncompleteError pins the --json
// envelope of a finish whose item failed again: the details are the result,
// the list left included.
func TestReportError_JSON_CollectionFinishIncompleteError(t *testing.T) {
	withJSONOutput(t)
	err := &core.CollectionFinishIncompleteError{Profile: "Skyrim Plus", Result: &core.CollectionFinishResult{
		Profile: "Skyrim Plus", Failed: 1,
		Failures: []core.ItemFailure{{SourceID: "nexusmods", ModID: "4044", Name: "FSMP", Reason: "download failed: HTTP 503"}},
		Todos: []core.CollectionTodoItem{{SourceID: "nexusmods", ModID: "4044", Name: "FSMP", Reason: domain.TodoDownloadFailed,
			Detail: "download failed: HTTP 503", Action: core.TodoActionRetry, Remedy: "the download of FSMP failed; retry it"}},
		Total: 2,
	}}
	out := captureStdout(t, func() error { reportError(err); return nil })
	assert.Equal(t, "{\n"+
		"  \"error\": \"collection Skyrim Plus is not finished - 1 item(s) failed again: FSMP: download failed: HTTP 503\",\n"+
		"  \"details\": {\n"+
		"    \"profile\": \"Skyrim Plus\",\n"+
		"    \"installed\": 0,\n"+
		"    \"failed\": 1,\n"+
		"    \"failures\": [\n"+
		"      {\n"+
		"        \"source_id\": \"nexusmods\",\n"+
		"        \"mod_id\": \"4044\",\n"+
		"        \"name\": \"FSMP\",\n"+
		"        \"reason\": \"download failed: HTTP 503\"\n"+
		"      }\n"+
		"    ],\n"+
		"    \"todos\": [\n"+
		"      {\n"+
		"        \"source_id\": \"nexusmods\",\n"+
		"        \"mod_id\": \"4044\",\n"+
		"        \"name\": \"FSMP\",\n"+
		"        \"reason\": \"download_failed\",\n"+
		"        \"detail\": \"download failed: HTTP 503\",\n"+
		"        \"action\": \"retry\",\n"+
		"        \"remedy\": \"the download of FSMP failed; retry it\"\n"+
		"      }\n"+
		"    ],\n"+
		"    \"total\": 2\n"+
		"  }\n"+
		"}\n", out)
}
