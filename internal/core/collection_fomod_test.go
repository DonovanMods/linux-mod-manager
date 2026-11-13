package core_test

// #614 SF5 (#621's collection format): recorded FOMOD choices travel in a
// collection's mods entries, the export declares `requires: fomod`, and an
// import refuses - never rewrites - a stranger's choices lmm could not
// replay as written.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/require"
)

var collectionFomodChoices = domain.FomodChoices{
	ConfigSHA256: strings.Repeat("ab", 32),
	Steps: []fomod.StepChoice{{Name: "Main", Groups: []fomod.GroupChoice{
		{Name: "Textures", Choices: []fomod.OptionChoice{{Name: "4K", Idx: 1}}},
	}}},
	FileStates: map[string]string{"skse64_loader.exe": "Active"},
}

// TestExportCollection_FomodChoicesRequireFomod: a profile whose mod
// records FOMOD choices exports them in its mods entry and declares
// `requires: fomod`, and the document imports elsewhere with the same
// choices recorded.
func TestExportCollection_FomodChoicesRequireFomod(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "cp", Name: "Cyberpunk 2077", ModPath: t.TempDir(), SourceIDs: map[string]string{"steamworkshop": "1091500"}}
	require.NoError(t, svc.SaveGame(context.Background(), game))
	profile := &domain.Profile{
		Name: "fomod", GameID: game.ID,
		Mods: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "1", Version: "1.0", Fomod: collectionFomodChoices},
			{SourceID: "nexusmods", ModID: "2", Version: "2.0"},
		},
		Collection: &domain.CollectionInfo{ID: "2f8c7a3e-0000-4000-8000-000000000002", Version: 1},
	}
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), profile))

	export, err := svc.ExportCollection(context.Background(), game.ID, "fomod", core.CollectionExportOptions{})
	require.NoError(t, err)
	checkCollectionGolden(t, "export_v1_fomod", export.Document)

	recv, _ := recipient(t)
	plan, err := recv.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: export.Document, GameID: "cyber"})
	require.NoError(t, err)
	require.Empty(t, plan.Collection.Unsupported, "fomod is in the supported set")
	require.Contains(t, core.SupportedCollectionFeatures(), core.CollectionFeatureFomod)
	_, err = recv.ApplyCollectionImport(context.Background(), &domain.Game{ID: "cyber"}, plan, core.ProfileImportOptions{NoInstall: true}, nil)
	require.NoError(t, err)
	saved, err := config.LoadProfile(recv.ConfigDir(), "cyber", "fomod")
	require.NoError(t, err)
	ref := saved.FindRef("nexusmods", "1")
	require.NotNil(t, ref)
	require.True(t, collectionFomodChoices.Equal(ref.Fomod), "the choices are recorded as written: %+v", ref.Fomod)
}

// TestCollectionImport_UndeclaredFomodIsDerived: a hand-edited document
// that carries choices but drops `requires: fomod` is gated on the derived
// feature all the same (an importer that lacks it would refuse).
func TestCollectionImport_UndeclaredFomodIsDerived(t *testing.T) {
	svc, _ := recipient(t)
	doc := collectionFomodDoc("{config_sha256: " + strings.Repeat("ab", 32) + ", steps: [{name: Main, groups: [{name: T, choices: [{name: 4K, idx: 0}]}]}]}")
	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(doc), GameID: "cyber"})
	require.NoError(t, err)
	require.Contains(t, plan.Collection.Requires, core.CollectionFeatureFomod)
}

func collectionFomodDoc(fomodBlock string) string {
	return "lmm_collection: 1\nname: p\ngame:\n  steam_app_id: \"1091500\"\nrequires: [fomod]\n" +
		"mods:\n  - {source_id: s, mod_id: m, fomod: " + fomodBlock + "}\n"
}

// TestCollectionImport_RefusesFomodChoicesItCannotReplayAsWritten: every
// recorded name is matched against the installer's, so a control, escape
// or bidi character in one refuses the document rather than being cleaned
// into a different name; so do a malformed config hash or file state and
// counts over internal/fomod's MaxChoices.
func TestCollectionImport_RefusesFomodChoicesItCannotReplayAsWritten(t *testing.T) {
	svc, _ := recipient(t)
	hash := strings.Repeat("ab", 32)
	choice := func(step, group, option string) string {
		return fmt.Sprintf("{config_sha256: %s, steps: [{name: %q, groups: [{name: %q, choices: [{name: %q, idx: 0}]}]}]}", hash, step, group, option)
	}
	many := func(n int) string {
		opts := make([]string, n)
		for i := range opts {
			opts[i] = fmt.Sprintf("{name: o%d, idx: %d}", i, i)
		}
		return fmt.Sprintf("{config_sha256: %s, steps: [{name: S, groups: [{name: G, choices: [%s]}]}]}", hash, strings.Join(opts, ", "))
	}
	manyStates := func(n int) string {
		states := make([]string, n)
		for i := range states {
			states[i] = fmt.Sprintf("f%d.esp: Active", i)
		}
		return fmt.Sprintf("{config_sha256: %s, steps: [], file_states: {%s}}", hash, strings.Join(states, ", "))
	}
	for name, block := range map[string]string{
		"bidi in an option":      choice("Main", "T", "4K\u202e"),
		"escape in a step":       choice("Ma\x1b[31min", "T", "4K"),
		"zero-width in a group":  choice("Main", "T\u200b", "4K"),
		"newline in an option":   choice("Main", "T", "4K\nUltra"),
		"long name":              choice("Main", "T", strings.Repeat("x", 2000)),
		"upper-case config hash": "{config_sha256: " + strings.ToUpper(hash) + ", steps: []}",
		"short config hash":      "{config_sha256: abc, steps: []}",
		"unknown file state":     "{config_sha256: " + hash + ", steps: [], file_states: {a.esp: Present}}",
		"bidi in a file state":   "{config_sha256: " + hash + ", steps: [], file_states: {\"a\u202e.esp\": Active}}",
		"negative index":         "{config_sha256: " + hash + ", steps: [{name: S, index: -1, groups: []}]}",
		"too many entries":       many(fomod.MaxChoices),
		"too many file states":   manyStates(fomod.MaxChoices + 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(collectionFomodDoc(block)), GameID: "cyber"})
			require.ErrorIs(t, err, core.ErrInvalidCollection)
			require.Contains(t, err.Error(), "s:m's fomod choices")
		})
	}

	// At the caps, and with names that are merely unusual, it imports.
	for name, block := range map[string]string{
		"at the entry cap": many(fomod.MaxChoices - 2),
		"at the file cap":  manyStates(fomod.MaxChoices),
		"unicode names":    choice("Étape 1", "Texturen", "4K — Ultra"),
		"no config hash":   "{steps: [{name: S, groups: [{name: G, choices: []}]}]}",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(collectionFomodDoc(block)), GameID: "cyber"})
			require.NoError(t, err)
		})
	}
}

// TestCollectionImport_PlanShowsTheRecordedInstallerChoices: a collection
// plan names `requires: fomod` and each item's recorded installer choices
// as lines a frontend shows (#615).
func TestCollectionImport_PlanShowsTheRecordedInstallerChoices(t *testing.T) {
	svc, _ := recipient(t)
	doc := collectionFomodDoc("{config_sha256: " + strings.Repeat("ab", 32) + ", steps: [{name: Main, groups: [{name: Textures, choices: [{name: 4K, idx: 1}]}, {name: Extras, choices: []}]}]}")
	plan, err := svc.PlanCollectionImport(context.Background(), core.CollectionImportRequest{Data: []byte(doc), GameID: "cyber"})
	require.NoError(t, err)
	require.Contains(t, plan.Collection.Requires, core.CollectionFeatureFomod)
	require.Len(t, plan.Collection.Items, 1)
	require.Equal(t, []string{"Main > Textures: 4K", "Main > Extras: none"}, plan.Collection.Items[0].InstallerChoices)
}
