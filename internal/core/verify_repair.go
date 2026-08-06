package core

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// redownloadModFile re-downloads a single mod file and extracts it to the
// cache, then updates the checksum in the DB. persisted reports whether a
// checksum row was actually written: a download can succeed while yielding no
// checksum to store (e.g. a directory-source mod whose directory holds no
// regular files), and callers must not report "checksum populated" on the
// strength of a nil error alone (#164) - only when persisted is true.
//
// #224 Task 4: ported verbatim from cmd/lmm/verify.go's redownloadModFile
// (pre-refactor, doVerify's --fix repair helper), minus the cmd parameter -
// callers now pass ctx directly, and game/profile come from the run itself
// rather than being threaded in.
func (r *verifyRun) redownloadModFile(ctx context.Context, mod *domain.InstalledMod, fileID string) (persisted bool, err error) {
	files, err := r.svc.GetModFiles(ctx, mod.SourceID, SourceMappedMod(r.game, &mod.Mod))
	if err != nil {
		return false, fmt.Errorf("getting mod files: %w", err)
	}
	var downloadFile *domain.DownloadableFile
	for i := range files {
		if files[i].ID == fileID {
			downloadFile = &files[i]
			break
		}
	}
	if downloadFile == nil {
		return false, fmt.Errorf("file %s not found in mod", fileID)
	}
	result, err := r.svc.DownloadMod(ctx, mod.SourceID, r.game, &mod.Mod, downloadFile, nil)
	if err != nil {
		return false, err
	}
	if result.Checksum == "" {
		return false, nil
	}
	if err := r.svc.SaveFileChecksum(mod.SourceID, mod.ID, r.game.ID, r.profile, fileID, result.Checksum); err != nil {
		return false, fmt.Errorf("saving checksum: %w", err)
	}
	return true, nil
}
