package config_test

// #660: a collection item the import could not install keeps its place in
// the profile with a to-do record (`todo:`): why, and what resolves it.
// This machine's record, like substituted_for: saved in place, never
// exported, never in a collection document.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

func TestSaveProfile_TodoIsEditedInPlaceAndNeverExported(t *testing.T) {
	dir := t.TempDir()
	path := writeProfileFile(t, dir, "tg", "default", handEdited)
	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	ref := profile.FindRef("local", "beta")
	ref.Todo = &domain.CollectionTodo{Reason: domain.TodoInstallerQuestions, Detail: "the recorded choices no longer fit", FomodReason: "stale"}
	require.NoError(t, config.SaveProfile(dir, profile))
	block := "    todo:\n      reason: installer_questions\n      detail: the recorded choices no longer fit\n      fomod_reason: stale\n"
	text := readFile(t, path)
	require.Contains(t, text, block)
	require.Equal(t, handEdited, strings.Replace(text, block, "", 1), "only the new block is added")

	reloaded, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.Equal(t, profile.Mods, reloaded.Mods)

	exported, err := config.ExportProfile(reloaded)
	require.NoError(t, err)
	require.NotContains(t, string(exported), "todo", "an export never carries the record")
	doc := &domain.CollectionDocument{FormatVersion: 1, Name: "c",
		Items: []domain.CollectionItem{{Ref: *reloaded.FindRef("local", "beta"), Name: "Beta"}}}
	data, err := config.MarshalCollection(doc)
	require.NoError(t, err)
	require.NotContains(t, string(data), "todo", "a collection document never carries the record")

	reloaded.FindRef("local", "beta").Todo = nil
	require.NoError(t, config.SaveProfile(dir, reloaded))
	require.Equal(t, handEdited, readFile(t, path), "clearing it restores the file")
}

func TestSaveProfile_TodoReasonChangesInPlace(t *testing.T) {
	dir := t.TempDir()
	writeProfileFile(t, dir, "tg", "default", handEdited)
	profile, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	profile.FindRef("local", "beta").Todo = &domain.CollectionTodo{Reason: domain.TodoDownloadFailed, Detail: "HTTP 503"}
	require.NoError(t, config.SaveProfile(dir, profile))
	profile.FindRef("local", "beta").Todo = &domain.CollectionTodo{Reason: domain.TodoGonePin, FileIDs: []string{"9"}}
	require.NoError(t, config.SaveProfile(dir, profile))
	reloaded, err := config.LoadProfile(dir, "tg", "default")
	require.NoError(t, err)
	require.Equal(t, &domain.CollectionTodo{Reason: domain.TodoGonePin, FileIDs: []string{"9"}}, reloaded.FindRef("local", "beta").Todo)
}

// TestExportProfileValue_CarriesNoMachineRecords: `profile export --json`
// emits ExportProfileValue, which - like the YAML export - carries neither
// a to-do nor the pin a substitute stands in for (#660 review).
func TestExportProfileValue_CarriesNoMachineRecords(t *testing.T) {
	profile := &domain.Profile{Name: "p", GameID: "g", Mods: []domain.ModReference{{
		SourceID: "nexusmods", ModID: "266", Version: "4.3.9",
		Todo:           &domain.CollectionTodo{Reason: domain.TodoDownloadFailed},
		SubstitutedFor: &domain.CollectionPin{Version: "4.3.8a", FileIDs: []string{"733846"}},
	}}}
	exported := config.ExportProfileValue(profile)
	data, err := json.Marshal(exported)
	require.NoError(t, err)
	require.NotContains(t, string(data), "todo")
	require.NotContains(t, string(data), "substituted_for")
	require.NotNil(t, profile.Mods[0].Todo, "the profile itself keeps its records")
}
