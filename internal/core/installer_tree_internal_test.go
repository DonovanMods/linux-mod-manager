package core

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// #644 nit: a plain mod whose file has a file list or patches (#610) has
// a variant - treeInputsVariant of the plain base - and that is no
// installer's tree. installerTree tells the two apart, and so do its
// callers: asRebuild rebuilds such a row raw, never re-resolving an
// installer it was not installed with.
func TestInstallerTree_TellsAPlainTreeWithTreeInputsFromAnInstallers(t *testing.T) {
	inputs := map[string]domain.TreeInputs{"200": {FileList: []domain.FileListEntry{{Path: "a.esp", MD5: "0123456789abcdef0123456789abcdef"}}}}
	plainWithList := treeInputsVariant("", inputs)
	installer := fomodVariant(domain.FomodChoices{ConfigSHA256: "abc"})
	ids := []string{"200"}
	for _, tc := range []struct {
		name    string
		variant string
		want    bool
	}{
		{"plain", "", false},
		{"plain, file-set variant", fileSetVariant("", ids), false},
		{"plain with a file list", plainWithList, false},
		{"plain with a file list, file-set variant", fileSetVariant(plainWithList, ids), false},
		{"plain archive", archiveVariant("", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), false},
		{"installer", installer, true},
		{"installer, file-set variant", fileSetVariant(installer, ids), true},
		{"installer archive", archiveVariant(installer, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"), true},
		{"installer with a file list", treeInputsVariant(installer, inputs), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := &domain.InstalledMod{Mod: domain.Mod{Variant: tc.variant}, FileIDs: ids}
			if got := installerTree(row, inputs); got != tc.want {
				t.Errorf("installerTree(%q) = %v, want %v", tc.variant, got, tc.want)
			}
			in := asRebuild(fomodInput{fileLists: domain.FileLists{"200": inputs["200"].FileList}}, row)
			if in.raw == tc.want {
				t.Errorf("asRebuild: raw = %v for an installer tree %v", in.raw, tc.want)
			}
		})
	}
}

// Reconfigure and mod show ask the same question: a plain mod with a file
// list has no installer choices to change.
func TestInstallerTree_ReconfigureAndModShowOfAPlainModWithAFileList(t *testing.T) {
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	ctx := context.Background()
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), LinkMethod: domain.LinkSymlink}
	if err := svc.SaveGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	pm := svc.NewProfileManager()
	if _, err := pm.Create(ctx, "g1", "default"); err != nil {
		t.Fatal(err)
	}
	if err := pm.SetDefault(ctx, "g1", "default"); err != nil {
		t.Fatal(err)
	}
	list := domain.FileLists{"200": {{Path: "a.esp", MD5: "0123456789abcdef0123456789abcdef"}}}
	ref := domain.ModReference{SourceID: "msrc", ModID: "42", Version: "2.0", FileIDs: []string{"200"}, FileList: list}
	if err := pm.UpsertMod(ctx, "g1", "default", ref); err != nil {
		t.Fatal(err)
	}
	variant := refVariant(&ref)
	if variant == "" {
		t.Fatal("precondition: a file list names a variant")
	}
	if err := svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:         domain.Mod{ID: "42", SourceID: "msrc", Name: "M", Version: "2.0", GameID: "g1", Variant: variant},
		ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, LinkMethod: domain.LinkSymlink, FileIDs: []string{"200"},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.PlanFomodReconfigure(ctx, game, "default", "msrc", "42"); !errors.Is(err, ErrNotFomodInstalled) {
		t.Errorf("reconfigure: err = %v, want ErrNotFomodInstalled", err)
	}
	detail, err := svc.ModDetail(ctx, game, "default", "msrc", "42")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Installed == nil {
		t.Fatal("precondition: the mod is installed")
	}
	if detail.Installed.Fomod != nil {
		t.Errorf("mod show reports installer choices for a plain mod: %+v", detail.Installed.Fomod)
	}
}
