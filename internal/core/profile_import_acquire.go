// Package core: this file is ApplyImport's per-item halves on the
// acquire/decide/commit engine (acquire_engine.go, #602): acquireImportItem
// resolves and downloads one mod and writes nothing; commitImportItem writes
// it - cache, deploy, DB row, profile ref - exactly as the single loop did
// before the split.
package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

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
	// fomod is the mod's FOMOD resolve outcome (#614), recorded on its
	// ref; nil when it is not an installer.
	fomod *fomodResolved
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
func (s *Service) acquireImportItem(ctx context.Context, game *domain.Game, plan *ImportPlan, ref domain.ModReference, scope Scope, answers map[string]domain.FomodChoices, result *ProfileImportResult, emit func(Event), failed map[string]string) (*importStaged, *acquireFailure) {
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

	// #608: a profile fill installs from the user's own archives, matched
	// by the collection's checksums - so a source that cannot describe the
	// mod or list its files right now costs the row its metadata, not the
	// install.
	supplied := plan.supplied[st.key]
	note := func(msg string) {
		result.Warnings = append(result.Warnings, msg)
		emit(StepEvent{Scope: st.scope, Phase: ImportNote, Detail: msg})
	}
	mod, err := s.GetMod(ctx, ref.SourceID, game.ID, ref.ModID)
	if err != nil {
		if supplied == nil {
			return nil, fail(fmt.Sprintf("failed to fetch mod: %v", err), err)
		}
		mod = &domain.Mod{ID: ref.ModID, SourceID: ref.SourceID, GameID: game.ID, Version: ref.Version,
			Name: cmp.Or(plan.fillNames[st.key], domain.ModKey(ref.SourceID, ref.ModID))}
		note(fmt.Sprintf("%s did not describe %s (%v), so it is installed under the collection's name for it", s.sourceDisplayName(ref.SourceID), mod.Name, err))
	}
	st.scope.ModName = mod.Name

	files, err := s.GetModFiles(ctx, game, ref.SourceID, mod)
	if supplied != nil {
		files, err = suppliedFiles(files, err, ref, supplied)
	}
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
	// #607: the imported reference's stated type is the row's, and every
	// deploy of it routes by it.
	mod.ModType = ref.ModType
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
	//
	// #614: a FOMOD mod's entry is the variant its choices - this run's
	// answers, else the imported reference's - name (fomodVariant).
	fx := s.fomodInputFor(ctx, game, plan.Profile.Name, &ref, ref.SourceID, ref.ModID, answers)
	gameCache := s.GetGameCache(game)
	warmKey := modCacheKey(game.ID, *mod)
	warmKey.Variant = fomodVariant(fx.choices)
	if gameCache.HasFileIDs(warmKey, st.fileIDs) {
		if warmKey.Variant != "" {
			mod.Variant = warmKey.Variant
			st.fomod = &fomodResolved{variant: warmKey.Variant, choices: fx.choices}
		}
		return st, nil
	}
	// The mod's files are not in the run's item set until this returns, so
	// a panic in a later file's acquire releases the earlier ones here
	// (#628).
	defer releaseOnPanic(func() { s.releaseAcquired(st.files, false) })
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
		var a *acquiredFile
		reason := "download failed: %v"
		if sup, ok := supplied[file.ID]; ok {
			reason = "installing from " + sup.display + " failed: %v"
			a, err = s.acquireSupplied(ctx, gameCache, ref.SourceID, game, mod, file, sup, fx, progressFn)
		} else {
			a, err = s.acquireFile(ctx, gameCache, ref.SourceID, game, mod, file, fx, progressFn)
		}
		if err != nil {
			f := fail(fmt.Sprintf(reason, err), err)
			// The mod's earlier files go with the failure, so a refusal
			// that blocks retains them too: the re-run fetches none of
			// them again.
			f.release = s.releaseFilesFunc(st.files)
			emit(StepEvent{Scope: st.scope, Phase: ImportDownloadDone})
			return nil, f
		}
		st.files = append(st.files, a)
	}
	emit(StepEvent{Scope: st.scope, Phase: ImportDownloadDone})
	resolved, err := unifyFomod(mod, st.files)
	if err != nil {
		f := fail(err.Error(), err)
		f.release = s.releaseFilesFunc(st.files)
		return nil, f
	}
	st.fomod = resolved
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
		// #607: the row is another profile's; where the mod deploys is
		// this profile's reference's to say (its stated type, or none).
		row := *st.cachedRow
		row.ModType = st.ref.ModType
		modRef, msgs, err := s.importCachedMod(ctx, game, profileName, installer, row, plan.priorVersions[st.key], deploy, plan.updatePolicyFor(st.key))
		if err != nil {
			fail(err.Error())
			return nil
		}
		for _, msg := range msgs {
			result.Warnings = append(result.Warnings, msg)
			emit(StepEvent{Scope: scope, Phase: ImportNote, Detail: msg})
		}
		s.writeImportPolicy(ctx, game, plan, profileName, st.key, row.SourceID, row.ID, scope, result, emit)
		if cerr := s.recordImportedRef(ctx, pm, game.ID, profileName, modRef, fomodRecord{op: fomodKeep}, scope, result, emit); cerr != nil {
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
		UpdatePolicy: plan.updatePolicyFor(st.key),
		Enabled:      true,
		FileIDs:      st.fileIDs,
		Deployed:     deploy, // installer.Install above just succeeded
	}
	installedMod.GameID = game.ID
	if err := s.saveInstalledMod(ctx, installedMod); err != nil {
		fail(fmt.Sprintf("save failed: %v", err))
		return nil
	}
	s.writeImportPolicy(ctx, game, plan, profileName, st.key, mod.SourceID, mod.ID, scope, result, emit)

	// #372: the row exists now, so what was downloaded above finally has
	// somewhere to record its checksum.
	for _, msg := range s.recordFileChecksums(ctx, mod.SourceID, mod.ID, game.ID, profileName, checksums) {
		result.Warnings = append(result.Warnings, msg)
		emit(StepEvent{Scope: scope, Phase: ImportNote, Detail: msg})
	}

	modRef := domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: st.fileIDs}
	fomodRec := fomodRecordOf(st.fomod)
	if cerr := s.recordImportedRef(ctx, pm, game.ID, profileName, modRef, fomodRec, scope, result, emit); cerr != nil {
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
	listed := make(map[string]bool, len(toInstall))
	for _, ref := range toInstall {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		if listed[key] {
			continue // a duplicate the engine skipped (acquireRun.key)
		}
		listed[key] = true
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
	var fomodErr *FomodChoicesRequiredError
	if errors.As(refused, &fomodErr) {
		result.FomodChoicesRequired = fomodErr
	}
	return &ProfileImportIncompleteError{Profile: plan.Profile.Name, Result: result, refusal: refused}
}

// updatePolicyFor is the update policy an import writes key's row with:
// auto when the plan names it (ImportPlan.autoUpdate), else notify.
func (p *ImportPlan) updatePolicyFor(key string) domain.UpdatePolicy {
	if p.autoUpdate[key] {
		return domain.UpdateAuto
	}
	return domain.UpdateNotify
}

// writeImportPolicy sets key's row to the auto update policy when the plan
// names it (ImportPlan.autoUpdate), inside the import's commit. The row
// write above already carries it for a new row; an EXISTING row (a
// re-import over a profile, --force) keeps its policy through the upsert,
// which never moves one, so this says it explicitly. A failure is a
// warning: the mod is installed either way.
func (s *Service) writeImportPolicy(ctx context.Context, game *domain.Game, plan *ImportPlan, profileName, key, sourceID, modID string, scope Scope, result *ProfileImportResult, emit func(Event)) {
	if !plan.autoUpdate[key] {
		return
	}
	if err := s.setModUpdatePolicy(ctx, sourceID, modID, game.ID, profileName, domain.UpdateAuto); err != nil {
		msg := fmt.Sprintf("could not set %s to update automatically: %v", key, err)
		result.Warnings = append(result.Warnings, msg)
		emit(StepEvent{Scope: scope, Phase: ImportNote, Detail: msg})
	}
}

// suppliedFiles is a profile fill's file list for ref (#608): the source's
// own listing, with every pinned file it does not list - or every one, when
// the listing failed - described from the user's archive instead, since
// the archive was matched to the pin by the collection's checksum.
func suppliedFiles(files []domain.DownloadableFile, listErr error, ref domain.ModReference, supplied map[string]suppliedArchive) ([]domain.DownloadableFile, error) {
	if listErr != nil {
		files = nil
	}
	for _, id := range ref.FileIDs {
		if slices.ContainsFunc(files, func(f domain.DownloadableFile) bool { return f.ID == id }) {
			continue
		}
		sup, ok := supplied[id]
		if !ok {
			if listErr != nil {
				return nil, listErr
			}
			continue
		}
		files = append(files, domain.DownloadableFile{ID: id, Name: sup.display, FileName: sup.name, Version: ref.Version, Size: sup.size})
	}
	return files, nil
}
