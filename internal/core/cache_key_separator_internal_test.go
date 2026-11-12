package core

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// TestImportWithIdentity_VersionWithVariantSeparatorIsRefused (#603): an
// import's version can come from outside the archive's name (update
// --from-file's --version override, a matched source file's version), so
// the ingest refuses a version carrying cache.VariantSeparator, with the
// cache's typed error, before it writes anything.
func TestImportWithIdentity_VersionWithVariantSeparatorIsRefused(t *testing.T) {
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	for _, mode := range []domain.DeployMode{domain.DeployExtract, domain.DeployCopy} {
		t.Run(mode.String(), func(t *testing.T) {
			game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir(), DeployMode: mode}
			archive := filepath.Join(t.TempDir(), "mod.zip")
			writeSeparatorTestZip(t, archive)

			ident := importIdentity{sourceID: domain.SourceLocal, modID: "m1", version: "2.0@x"}
			_, err := svc.newImporter(game).importWithIdentity(context.Background(), archive, game, ImportOptions{}, ident)
			if !errors.Is(err, cache.ErrVersionSeparator) {
				t.Fatalf("importWithIdentity err = %v, want cache.ErrVersionSeparator", err)
			}
			entry := svc.GetGameCache(game).ModPath(cache.Key{GameID: game.ID, SourceID: ident.sourceID, ModID: ident.modID, Version: ident.version})
			if _, statErr := os.Stat(filepath.Dir(entry)); !os.IsNotExist(statErr) {
				t.Fatalf("the refused import created %s (stat err %v)", filepath.Dir(entry), statErr)
			}
		})
	}
}

func writeSeparatorTestZip(t *testing.T, path string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("plugin.dll")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
