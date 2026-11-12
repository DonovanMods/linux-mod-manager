// Package core: this file is ApplyImport's per-item halves on the
// acquire/decide/commit engine (acquire_engine.go, #602): acquireImportItem
// resolves and downloads one mod and writes nothing; commitImportItem writes
// it - cache, deploy, DB row, profile ref - exactly as the single loop did
// before the split.
package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// importStaged is one import item between its acquire and its commit.
type importStaged struct {
	ref   domain.ModReference
	key   string
	scope Scope
	// cachedRow is set when another profile already has the mod (#371):
	// nothing was acquired, the commit copies that row and its cache entry.
	cachedRow *domain.InstalledMod
	// mod, fileIDs and files are the downloaded path: the mod as the
	// source describes it (its version stamped, #94), the file ids it will
	// record, and the acquired files - none when the cache already holds
	// every file (#138's cache-first convergence).
	mod     *domain.Mod
	fileIDs []string
	files   []*acquiredFile
}

// recordImportFailure counts one failed import item, as the single loop
// counted it (#131/#308).
func recordImportFailure(result *ProfileImportResult, ref domain.ModReference, name, reason string) {
	result.Failed++
	result.Warnings = append(result.Warnings, fmt.Sprintf("%s:%s: %s", ref.SourceID, ref.ModID, reason))
	result.Failures = append(result.Failures, ItemFailure{
		SourceID: ref.SourceID, ModID: ref.ModID, Name: name, Reason: reason,
	})
}

// acquireImportItem is acquire for one import item: resolve the mod and its
// files and download what the cache lacks, writing nothing. A failure is
// reported LIVE (ImportModFailed, where it happened, under the item's own
// Installing line) and COUNTED only when it settles, so an import the
// engine refuses counts nothing. failed receives the reason of every
// failure, for the refused document.
func (s *Service) acquireImportItem(ctx context.Context, game *domain.Game, plan *ImportPlan, ref domain.ModReference, scope Scope, result *ProfileImportResult, emit func(Event), failed map[string]string) (*importStaged, *acquireFailure) {
	scope.Mod = &domain.ModReference{SourceID: ref.SourceID, ModID: ref.ModID}
	emit(ModEvent{Scope: scope, Phase: ImportModInstalling})

	st := &importStaged{ref: ref, key: domain.ModKey(ref.SourceID, ref.ModID), scope: scope}
	fail := func(reason string, err error) *acquireFailure {
		failed[st.key] = reason
		name := st.scope.ModName
		emit(ModEvent{Scope: st.scope, Phase: ImportModFailed, Detail: reason})
		return &acquireFailure{
			refusal: AcquireRefusal{SourceID: ref.SourceID, ModID: ref.ModID, Name: name, Err: err},
			settle:  func() { recordImportFailure(result, ref, name, reason) },
		}
	}

	// #371: a mod another profile already has needs this profile's own
	// row, built from that row and its cache entry. Deliberately ahead of
	// every source call below: an already-downloaded mod must not be
	// re-fetched, and an EXTERNAL one (Steam's own item) has no source call
	// that would even succeed.
	if row, ok := plan.cachedRows[st.key]; ok {
		st.scope.ModName = row.Name
		st.cachedRow = &row
		return st, nil
	}

	mod, err := s.GetMod(ctx, ref.SourceID, game.ID, ref.ModID)
	if err != nil {
		return nil, fail(fmt.Sprintf("failed to fetch mod: %v", err), err)
	}
	st.scope.ModName = mod.Name

	files, err := s.GetModFiles(ctx, game, ref.SourceID, mod)
	if err != nil {
		return nil, fail(fmt.Sprintf("failed to get files: %v", err), err)
	}
	if len(files) == 0 {
		return nil, fail("no downloadable files", errors.New("no downloadable files"))
	}

	// Select files to download - use the DB-stored FileIDs for a
	// redownload, or the imported profile's own FileIDs for a fresh install
	// (see ImportPlan.storedFileIDs' doc comment for why this can't just be
	// ref.FileIDs uniformly).
	var fileIDsToUse []string
	if stored, ok := plan.storedFileIDs[st.key]; ok {
		fileIDsToUse = stored
	} else if len(ref.FileIDs) > 0 {
		fileIDsToUse = ref.FileIDs
	}
	filesToDownload, err := selectFilesForVersion(files, fileIDsToUse, ref.Version)
	if err != nil {
		return nil, fail(err.Error(), err)
	}

	mod.Version = domain.EffectiveInstalledVersion(mod.Version, filesToDownload) // #94
	st.mod = mod
	for _, f := range filesToDownload {
		st.fileIDs = append(st.fileIDs, f.ID)
	}

	// #138: cache-first, by per-file completion marker - the same guard
	// (and the same two review findings) as ApplyProfileSwitch's install
	// loop: HasFileIDs, not bare Exists (a version directory can exist yet
	// be only PARTIALLY populated by a broken-off download run), and by
	// FILE ID, never FileName (an extracted archive's cache entry holds
	// member names that match no DownloadableFile). Deploying from cache
	// matters most for exactly this flow's drift convergence: a
	// downgrade's archived file may have vanished upstream.
	gameCache := s.GetGameCache(game)
	if gameCache.HasFileIDs(game.ID, mod.SourceID, mod.ID, mod.Version, st.fileIDs) {
		return st, nil
	}
	for _, file := range filesToDownload {
		if err := ctx.Err(); err != nil {
			s.releaseAcquired(st.files, false)
			return nil, &acquireFailure{abort: err}
		}
		progressFn := func(e Event) {
			if forwardFetchStep(e, st.scope, emit) {
				return
			}
			d, ok := e.(DownloadEvent)
			if !ok || d.TotalBytes <= 0 {
				return
			}
			emit(DownloadEvent{Scope: st.scope, Phase: ImportDownloading, Percent: d.Percent})
		}
		a, err := s.acquireFile(ctx, gameCache, ref.SourceID, game, mod, file, progressFn)
		if err != nil {
			s.releaseAcquired(st.files, false)
			f := fail(fmt.Sprintf("download failed: %v", err), err)
			emit(StepEvent{Scope: st.scope, Phase: ImportDownloadDone})
			return nil, f
		}
		st.files = append(st.files, a)
	}
	emit(StepEvent{Scope: st.scope, Phase: ImportDownloadDone})
	return st, nil
}

// commitImportItem is commit for one acquired import item: the cache, the
// deployment, the DB row, its checksums and the profile ref, in the order
// and under the Ruling 16 pairing the single loop had. A non-nil error is
// fatal to the whole import (a cancellation, or a profile write that
// recordImportedRef cannot complete).
func (s *Service) commitImportItem(ctx context.Context, game *domain.Game, plan *ImportPlan, profileName string, pm *ProfileManager, installer *Installer, st *importStaged, deploy bool, result *ProfileImportResult, emit func(Event)) error {
	scope := st.scope
	fail := func(reason string) {
		recordImportFailure(result, st.ref, scope.ModName, reason)
		emit(ModEvent{Scope: scope, Phase: ImportModFailed, Detail: reason})
	}

	if st.cachedRow != nil {
		modRef, msgs, err := s.importCachedMod(ctx, game, profileName, installer, *st.cachedRow, plan.priorVersions[st.key], deploy)
		if err != nil {
			fail(err.Error())
			return nil
		}
		for _, msg := range msgs {
			result.Warnings = append(result.Warnings, msg)
			emit(StepEvent{Scope: scope, Phase: ImportNote, Detail: msg})
		}
		if cerr := s.recordImportedRef(ctx, pm, game.ID, profileName, modRef, scope, result, emit); cerr != nil {
			return cerr
		}
		result.countImported(deploy)
		emit(ModEvent{Scope: scope, Phase: ImportModInstalled})
		return nil
	}

	mod := st.mod
	var checksums []fileChecksum // #372 - saved after the DB row below
	for _, a := range st.files {
		res, err := s.commitAcquired(ctx, a)
		if err != nil {
			fail(fmt.Sprintf("download failed: %v", err))
			return nil
		}
		checksums = appendChecksum(checksums, a.file.ID, res)
	}

	if deploy {
		if err := s.deployImportedMod(ctx, game, installer, plan.priorVersions[st.key], mod, profileName); err != nil {
			fail(err.Error())
			return nil
		}
	}

	// Save to DB. Normalize GameID to the lmm game (see the comment on
	// ApplyProfileSwitch's own identical save site for why).
	installedMod := &domain.InstalledMod{
		Mod:          *mod,
		ProfileName:  profileName,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      st.fileIDs,
		Deployed:     deploy, // installer.Install above just succeeded
	}
	installedMod.GameID = game.ID
	if err := s.saveInstalledMod(ctx, installedMod); err != nil {
		fail(fmt.Sprintf("save failed: %v", err))
		return nil
	}

	// #372: the row exists now, so what was downloaded above finally has
	// somewhere to record its checksum.
	for _, msg := range s.recordFileChecksums(ctx, mod.SourceID, mod.ID, game.ID, profileName, checksums) {
		result.Warnings = append(result.Warnings, msg)
		emit(StepEvent{Scope: scope, Phase: ImportNote, Detail: msg})
	}

	modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: st.fileIDs}
	if cerr := s.recordImportedRef(ctx, pm, game.ID, profileName, modRef, scope, result, emit); cerr != nil {
		return cerr
	}
	result.countImported(deploy)
	emit(ModEvent{Scope: scope, Phase: ImportModInstalled})
	return nil
}

// refusedImport shapes an import the engine refused (#602) into the
// document an incomplete import already had: a *ProfileImportIncompleteError
// whose ProfileImportResult lists EVERY item - each refused one under its
// refusal code, each one that failed on its own with its reason, and each
// one held back with RefusalHeldBack - and is marked Refused, with the
// loader refusal (when that is what it was) under LoaderRequired. Nothing
// was written, the profile included.
func refusedImport(plan *ImportPlan, toInstall []domain.ModReference, refused *AcquireRefusedError, names, failed map[string]string, result *ProfileImportResult) error {
	byKey := make(map[string]AcquireRefusal, len(refused.Refusals))
	for _, r := range refused.Refusals {
		byKey[domain.ModKey(r.SourceID, r.ModID)] = r
	}
	result.ProfileName = plan.Profile.Name
	result.Refused = true
	result.Failed, result.Failures = 0, nil
	for _, ref := range toInstall {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		failure := ItemFailure{SourceID: ref.SourceID, ModID: ref.ModID, Name: names[key]}
		switch r, isRefused := byKey[key]; {
		case isRefused:
			failure.Reason, failure.Refusal = failed[key], refusalCode(r.Err)
		case failed[key] != "":
			failure.Reason = failed[key]
		default:
			failure.Reason, failure.Refusal = heldBackReason, RefusalHeldBack
		}
		result.Failed++
		result.Failures = append(result.Failures, failure)
	}
	var loaderErr *LoaderRequiredError
	if errors.As(refused, &loaderErr) {
		result.LoaderRequired = loaderErr
	}
	return &ProfileImportIncompleteError{Profile: plan.Profile.Name, Result: result, refusal: refused}
}
