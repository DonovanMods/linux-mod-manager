package main

// #610 review B1: `lmm profile import <older export>` lists the document's
// file lists and patches, and a patch the machine keeps for another file
// refuses the import without --allow-any-overrides.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoProfileImport_OlderExportPatchesAreListedAndGated(t *testing.T) {
	svc, game, _ := setupDoProfileImportTest(t)
	setFlag(t, &profileImportYes, true)
	setFlag(t, &jsonOutput, false)
	patch := append([]byte("BSDIFF40"), make([]byte, 24)...)
	sum := sha256.Sum256(patch)
	sha := hex.EncodeToString(sum[:])
	store := filepath.Join(svc.GlobalCacheDir(), game.ID, "collections", "patches")
	if game.CachePath != "" {
		store = filepath.Join(game.CachePath, "collections", "patches")
	}
	require.NoError(t, os.MkdirAll(store, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(store, sha+".diff"), patch, 0o644))
	data := buildImportProfileData(t, game.ID, "patched", []domain.ModReference{{
		SourceID: "test-src", ModID: "m", Version: "1.0", FileIDs: []string{"f"},
		FileList: domain.FileLists{"f": {{Path: "data/a.json", MD5: "0cc175b9c0f1b6a831c399e269772661"}}},
		Patches:  domain.FilePatches{"f": {{Path: "data/config.json", CRC32: "CABDDFC4", SHA256: sha}}},
	}})

	stdout, _, err := captureStdoutAndStderr(t, func() error {
		return doProfileImport(context.Background(), svc, game, data)
	})
	require.ErrorIs(t, err, core.ErrCodePatch)
	assert.Contains(t, stdout, "Exact file lists and patches")
	assert.Contains(t, stdout, "installs exactly 1 listed file(s)")
	assert.Contains(t, stdout, "patches data/config.json (baseline CRC32 CABDDFC4) - a patch lmm keeps for another file")
	assert.Contains(t, stdout, "1 patch(es) need --allow-any-overrides: the import is refused without it")
	_, perr := svc.NewProfileManager().Get(context.Background(), game.ID, "patched")
	assert.Error(t, perr, "nothing written")
}
