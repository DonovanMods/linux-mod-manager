// Package core: this file holds the archive-import flow - ImportArchive and
// the types it owns - lifted out of cmd/lmm/import.go's doImport tail by v2
// Phase 2 Unit K (#291). It is `lmm import <archive>`: take ONE local mod
// file, cache it, optionally enrich it from its source (--id), deploy it,
// and register it in a profile.
//
// Shape note (why there is no Plan here). Ruling 6 and the spec's
// "Plan/Apply is the shape of every mutation" both hold for flows whose
// decision can be previewed WITHOUT mutating. Archive import cannot: the
// only decision point is the file-conflict overwrite question, and conflicts
// are computed from the mod's CACHE ENTRY, which does not exist until the
// archive has been extracted into it - the same reason ApplyInstall computes
// its own conflicts mid-Apply rather than in PlanInstall (see
// InstallOptions.AcceptConflicts' doc comment and InstallPlan.Conflicts').
// So the cache write and the install are one mutation; v2 Phase 3 Ruling 1
// makes the decision point a typed error (*ConflictError) the frontend
// answers with opts.AcceptConflicts, so core never calls back into it.
package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// ImportArchiveOptions configures ImportArchive.
type ImportArchiveOptions struct {
	// SourceID and ModID are the CLI's --source/--id: setting BOTH (to a
	// real, non-local source) is what turns the import into a source-linked
	// one - the mod's metadata is fetched, its file resolved (#139), and the
	// cache entry renamed onto the resolved version. Either one alone
	// imports the archive under its own detected identity. The frontend
	// resolves an empty SourceID against the game's configured sources
	// before calling (that resolution can prompt, so it stays in cmd).
	SourceID string
	ModID    string

	// Force is `--force`: it skips the conflict CHECK entirely (not merely
	// the refusal - GetConflicts is never called), and it downgrades a
	// failed install.before_all/before_each hook from fatal to a warning.
	Force bool
	// SkipHooks runs no hooks at all (the CLI's --no-hooks).
	SkipHooks bool

	// AcceptConflicts answers the deploy-step conflict gate up front: the
	// caller has already decided that overwriting another mod's deployed
	// files is fine. Force implies it.
	//
	// Left false (the default), a non-empty conflict list makes
	// ImportArchive return *ConflictError carrying it, before the hooks,
	// the deploy, the DB row and the profile ref - and the cache entry the
	// call created is removed again (discardImportedCacheEntry), so a
	// refusal leaves managed state (DB, profile, game tree) untouched. That
	// removal only undoes what this call created: when an entry already
	// existed at a reproducible identity (--source/--id, or a NexusMods
	// filename), the import has already overwritten it before the conflict
	// gate runs, and a refusal does not restore it (#310). A frontend that
	// prompts re-runs ImportArchive with this set, and the re-run re-caches
	// the archive from disk; the conflict list cannot be computed before
	// the archive is cached, which is why it is a mid-Apply typed error
	// rather than a Plan field - see this file's package comment.
	AcceptConflicts bool
}

// ImportArchiveResult reports ImportArchive's outcome. As with every other
// core Result there is no verbosity concept: each diagnostic is recorded
// once here and emitted once as an event, and a streaming frontend renders
// one or the other, never both.
//
//   - Warnings holds the diagnostics the pre-lift CLI printed to stderr
//     unconditionally, each with no prefix baked in (print them as
//     `Warning: %s`): an unmapped source, a failed metadata fetch, a failed
//     source-file resolution, a failed marker stamp, a failed merged-pak
//     sync, one entry per warning FROM a successful sync, and a forced
//     install.before_all/before_each hook failure.
//   - Notes holds the diagnostics it printed only under --verbose, each
//     already carrying its historical "Warning: " prefix: the cache-rename
//     failure and the conflict-check failure (no indent), and the
//     profile-create and profile-upsert failures (2-space indent - the
//     event phase tells the two apart).
//   - HookWarnings holds the non-fatal install.after_each/after_all
//     failures, in call order. They are NOT events: the pre-lift CLI
//     accumulated them and printed them together at the very end, so the
//     frontend prints this slice as
//     `Warning: %s` after the flow returns. Nothing in the flow can fail
//     once they are populated.
//
// On error the result still carries everything accumulated before the
// failure, including Mod once the archive has been cached.
type ImportArchiveResult struct {
	// Mod is the imported mod as finally recorded - post-enrichment,
	// post-version-adoption. nil only when Import itself failed.
	Mod *domain.Mod `json:"mod,omitempty"`
	// LinkedSource is the source the row is linked to ("local" for an
	// unlinked import), mirroring ImportResult.LinkedSource; a "local" value
	// is what the CLI's closing "won't receive update notifications" note is
	// rendered from.
	LinkedSource string `json:"linked_source"`
	// AutoDetected reports that the mod's identity was parsed from the
	// archive's filename rather than given, mirroring
	// ImportResult.AutoDetected - the readout's "(auto-detected from
	// filename)" line.
	AutoDetected bool `json:"auto_detected"`
	// Renamed reports that enrichment moved the cache entry onto a new
	// ID/version and the rename SUCCEEDED. False when no rename was needed
	// and false when one was attempted and failed (that failure is in
	// Notes).
	Renamed bool `json:"renamed"`
	// FileID is the source file this archive resolved to (#139), or empty
	// when nothing resolved - the identity the completion marker was
	// stamped with.
	FileID string `json:"file_id,omitempty"`
	// FileIDs is what was actually recorded on the DB row and the profile
	// ref: FileID when one resolved, plus ImportResult.RetainedFileID folded
	// in for a DeployCompile import (#197 C1 - a row missing that value is
	// invisible to every future merge). Distinct from FileID, which is only
	// ever the resolved source file.
	FileIDs []string `json:"file_ids,omitempty"`
	// Deployed is the number of files the archive contributed to the game
	// directory (ImportResult.FilesExtracted). Zero is legitimate for a
	// DeployCompile ".exmodz" import, which is validate+retain only and
	// reaches the game through the merged pak instead.
	Deployed int `json:"deployed"`
	// MergedPakSynced reports that the end-of-import merged-pak sync
	// actually ran and produced no hard error (#197 I3/C1). syncMergedPak
	// no-ops with a nil error on a non-DeployCompile game (task-19 review
	// Minor 1), so this stays false there - a constant true would carry no
	// information for the only field that tells a frontend the sync
	// happened at all. Its non-fatal warnings, if any, are in Warnings.
	MergedPakSynced bool `json:"merged_pak_synced"`

	HookWarnings []string `json:"hook_warnings,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
	Notes        []string `json:"notes,omitempty"`
}

// ImportArchive imports one local archive into profileName: it caches the
// archive, optionally enriches it from its source (opts.ModID with a
// configured opts.SourceID), stamps the resolved file's completion marker,
// deploys it and records it on the DB row and the profile ref, running the
// install.* hook quartet around the deploy exactly as the pre-lift CLI did.
// sink may be nil.
//
// It is ONE mutation and runs under beginOp: the cache write and the install
// are the same operation, and the only decision point in between is the
// conflict gate opts.AcceptConflicts answers (see this file's package
// comment).
func (s *Service) ImportArchive(ctx context.Context, game *domain.Game, profileName, archivePath string, opts ImportArchiveOptions, sink EventSink) (*ImportArchiveResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &ImportArchiveResult{}, err
	}
	defer release()
	return s.importArchive(ctx, game, profileName, archivePath, opts, sink)
}

func (s *Service) importArchive(ctx context.Context, game *domain.Game, profileName, archivePath string, opts ImportArchiveOptions, sink EventSink) (*ImportArchiveResult, error) {
	result := &ImportArchiveResult{}

	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}
	scope := func() Scope {
		sc := Scope{Op: OpImport}
		if result.Mod != nil {
			sc.Mod = &domain.ModReference{SourceID: result.Mod.SourceID, ModID: result.Mod.ID}
			sc.ModName = result.Mod.Name
		}
		return sc
	}
	step := func(phase DeployPhase, msg string) {
		emit(StepEvent{Scope: scope(), Phase: phase, Detail: msg})
	}
	warn := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		result.Warnings = append(result.Warnings, msg)
		step(ImportArchiveWarning, msg)
	}
	note := func(phase DeployPhase, format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		result.Notes = append(result.Notes, msg)
		step(phase, msg)
	}

	importOpts := ImportOptions{
		SourceID:    opts.SourceID,
		ModID:       opts.ModID,
		ProfileName: profileName,
	}

	// Whether the entry Import is about to (re)create was already in the
	// cache before this call, so a conflict refusal below can remove what
	// this call created without touching what it found (unit P review,
	// Important 3). A MINTED identity is a fresh uuid: it cannot name a
	// pre-existing entry, and asking would be meaningless since the uuid
	// resolveImportIdentity hands back here is not the one Import will use.
	gameCache := s.GetGameCache(game)
	ident := resolveImportIdentity(filepath.Base(archivePath), importOpts)
	entryPreExisted := !ident.minted &&
		gameCache.Exists(game.ID, ident.sourceID, ident.modID, ident.version)

	imported, err := s.newImporter(game).Import(ctx, archivePath, game, importOpts)
	if err != nil {
		return result, fmt.Errorf("import failed: %w", err)
	}
	result.Mod = imported.Mod
	result.LinkedSource = imported.LinkedSource
	result.AutoDetected = imported.AutoDetected
	result.Deployed = imported.FilesExtracted

	// The identity Import recorded, before any enrichment moves it - the
	// cache entry currently lives under exactly this ID and version.
	preEnrichID, preEnrichVersion := result.Mod.ID, result.Mod.Version

	resolvedFile := s.enrichImportedMod(ctx, game, archivePath, result, opts, step, warn)

	if preEnrichID != result.Mod.ID || preEnrichVersion != result.Mod.Version {
		oldPath := gameCache.ModPath(game.ID, result.Mod.SourceID, preEnrichID, preEnrichVersion)
		newPath := gameCache.ModPath(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version)
		// A failed MkdirAll is silent, exactly as it was pre-lift: the
		// rename is not even attempted, and the cascade it causes (the mod
		// is not cached at its new version) surfaces at the deploy.
		if err := os.MkdirAll(filepath.Dir(newPath), 0755); err == nil {
			if err := os.Rename(oldPath, newPath); err != nil {
				note(ImportArchiveNote, "Warning: could not rename cache entry: %v", err)
			} else {
				result.Renamed = true
			}
		}
	}

	// #139: stamp the resolved file's completion marker onto the (final,
	// post-rename) cache entry. Non-fatal, and the FileIDs are recorded on
	// the row/ref below even if stamping fails - the row's file identity is
	// resolved either way; a missing marker only costs the one redundant
	// redownload every marker-less import always paid.
	result.FileIDs = []string{}
	if resolvedFile != nil {
		result.FileID = resolvedFile.ID
		result.FileIDs = []string{resolvedFile.ID}
		if err := s.markImportedFileComplete(ctx, game, result.Mod, resolvedFile.ID); err != nil {
			warn("could not mark cache entry complete: %v", err)
		}
	}
	// #197 C1 fix: a DeployCompile ".exmodz" import retains its source under
	// RetainedFileID (the archive's own filename - Import's only stable
	// identity), which is NEVER resolvedFile.ID (a real source file ID, or
	// nothing at all without --id). Without folding it into FileIDs too,
	// enabledMergeSources can never find this mod's retained source - it
	// silently never participates in any merge, forever, and is invisible to
	// update/verify since it's excluded from both sides of the staleness
	// fingerprint as well.
	if imported.RetainedFileID != "" && !slices.Contains(result.FileIDs, imported.RetainedFileID) {
		result.FileIDs = append(result.FileIDs, imported.RetainedFileID)
	}

	step(ImportArchiveDetected, "Mod: "+result.Mod.Name)
	step(ImportArchiveDetail, "Source: "+result.LinkedSource)
	step(ImportArchiveDetail, "ID: "+result.Mod.ID)
	if result.Mod.Version != "unknown" {
		step(ImportArchiveDetail, "Version: "+result.Mod.Version)
	}
	if result.Mod.Author != "" {
		step(ImportArchiveDetail, "Author: "+result.Mod.Author)
	}
	if result.Mod.SourceURL != "" {
		step(ImportArchiveDetail, "URL: "+result.Mod.SourceURL)
	}
	if result.AutoDetected {
		step(ImportArchiveDetail, "(auto-detected from filename)")
	}
	step(ImportArchiveDetail, fmt.Sprintf("Files: %d", result.Deployed))

	// The installer is built from the already-resolved method so both stay
	// consistent (and the profile file is only read once).
	linkMethod, err := s.GetEffectiveLinkMethod(ctx, game, profileName)
	if err != nil {
		return result, err
	}
	installer := s.newInstallerWithLinker(game, s.getLinker(linkMethod))

	if !opts.Force {
		conflicts, cerr := installer.GetConflicts(ctx, game, result.Mod, profileName)
		switch {
		case cerr != nil:
			note(ImportArchiveNote, "Warning: could not check conflicts: %v", cerr)
		case len(conflicts) > 0 && !opts.AcceptConflicts:
			s.discardImportedCacheEntry(game, result, preEnrichID, preEnrichVersion, entryPreExisted)
			return result, &ConflictError{Conflicts: conflicts}
		}
	}

	hooks, err := s.resolvedHooks(ctx, game, profileName)
	if err != nil {
		return result, err
	}
	runner, err := s.hookRunner(ctx)
	if err != nil {
		return result, err
	}
	hookCtx := hookContextFor(game)

	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.before_all", hooks.GetInstallBeforeAll()); err != nil {
		if !opts.Force {
			return result, fmt.Errorf("install.before_all hook failed: %w", err)
		}
		msg := fmt.Sprintf("install.before_all hook failed (forced): %v", err)
		result.Warnings = append(result.Warnings, msg)
		emit(HookEvent{Scope: scope(), Phase: InstallBeforeAllForced, Stage: "install.before_all", Detail: msg})
	}

	// The mod fields are set only AFTER install.before_all, which the
	// pre-lift tail ran with an empty mod scope, and cleared again before
	// install.after_all for the same reason.
	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = result.Mod.ID, result.Mod.Name, result.Mod.Version
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.before_each", hooks.GetInstallBeforeEach()); err != nil {
		if !opts.Force {
			return result, fmt.Errorf("install.before_each hook failed: %w", err)
		}
		msg := fmt.Sprintf("install.before_each hook failed (forced): %v", err)
		result.Warnings = append(result.Warnings, msg)
		emit(HookEvent{Scope: scope(), Phase: InstallBeforeEachForced, Stage: "install.before_each", Detail: msg})
	}

	step(ImportArchiveDeploying, "Deploying to game directory...")
	if err := installer.Install(ctx, game, result.Mod, profileName); err != nil {
		return result, fmt.Errorf("deployment failed: %w", err)
	}

	installedMod := &domain.InstalledMod{
		Mod:          *result.Mod,
		ProfileName:  profileName,
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		Deployed:     true,
		LinkMethod:   linkMethod,
		FileIDs:      result.FileIDs, // empty unless resolved against the source (#139)
	}
	if err := s.saveInstalledMod(ctx, installedMod); err != nil {
		return result, fmt.Errorf("failed to save mod: %w", err)
	}

	pm := s.NewProfileManager()
	if _, err := pm.Get(game.ID, profileName); err != nil {
		if err == domain.ErrProfileNotFound {
			if _, err := pm.Create(game.ID, profileName); err != nil {
				note(ImportArchiveProfileNote, "Warning: could not create profile: %v", err)
			}
		}
	}
	modRef := domain.ModReference{
		SourceID: result.Mod.SourceID,
		ModID:    result.Mod.ID,
		Version:  result.Mod.Version,
		FileIDs:  result.FileIDs,
	}
	if err := pm.UpsertMod(game.ID, profileName, modRef); err != nil {
		// Non-fatal (ruling 9: today this can only be a LOCKED ref, #143).
		note(ImportArchiveProfileNote, "Warning: could not update profile: %v", err)
	}

	// #197 I3/C1 fix: a DeployCompile ".exmodz" import deploys zero files of
	// its own (validate+retain only) - without this, the imported mod's
	// content never reaches the game directory until some OTHER flow happens
	// to sync the merged pak.
	//
	// Ruling 8: MergedPakSynced is set from the sync HAVING RUN, inside the
	// branch that ran it - syncMergedPak returns immediately for a
	// non-DeployCompile game, so the call is guarded by the same predicate
	// rather than re-derived from game.DeployMode next to the assignment,
	// where the two could drift apart.
	if game.DeployMode == domain.DeployCompile {
		if syncWarnings, syncErr := s.syncMergedPak(ctx, game, profileName); syncErr != nil {
			warn("could not sync merged pak: %v", syncErr)
		} else {
			result.MergedPakSynced = true
			for _, w := range syncWarnings {
				warn("%s", w)
			}
		}
	}

	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = result.Mod.ID, result.Mod.Name, result.Mod.Version
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.after_each", hooks.GetInstallAfterEach()); err != nil {
		result.HookWarnings = append(result.HookWarnings, fmt.Sprintf("install.after_each hook failed: %v", err))
	}
	hookCtx.ModID, hookCtx.ModName, hookCtx.ModVersion = "", "", ""
	if err := runHook(ctx, opts.SkipHooks, runner, &hookCtx, "install.after_all", hooks.GetInstallAfterAll()); err != nil {
		result.HookWarnings = append(result.HookWarnings, fmt.Sprintf("install.after_all hook failed: %v", err))
	}

	return result, nil
}

// discardImportedCacheEntry removes the cache entry THIS ImportArchive call
// created, leaving one it merely found alone (unit P review, Important 3).
//
// It runs on the conflict refusal, whose whole promise is that nothing
// happened: the decision may never come back, and for an unlinked archive
// (domain.SourceLocal, a freshly minted uuid per Import call) the accept
// re-run mints a DIFFERENT identity, so the refused pass's entry would be a
// full copy of the archive's contents referenced by nothing, forever, once
// per refusal. Removing it also restores the property the accept re-run
// wants for its own sake: the enrichment rename can no longer collide with
// a populated destination (task-8 review Minor 4's `Renamed: false`).
//
// The conflict list cannot be computed before the cache write - it comes
// from the entry's own deployable file listing, which only exists once the
// archive has been extracted, converted and marker-stamped (see this file's
// package comment) - so removing after the fact is the way the refusal
// stays free.
//
// preEnrichID/preEnrichVersion name where Import put the content; a
// successful enrichment rename moved it to result.Mod's identity and left
// nothing behind, and a FAILED one left it exactly where Import put it with
// the destination still holding whatever made the rename fail - which this
// call did not create and must not remove.
func (s *Service) discardImportedCacheEntry(game *domain.Game, result *ImportArchiveResult, preEnrichID, preEnrichVersion string, entryPreExisted bool) {
	if entryPreExisted || result.Mod == nil {
		return
	}
	gameCache := s.GetGameCache(game)
	if err := gameCache.Delete(game.ID, result.Mod.SourceID, preEnrichID, preEnrichVersion); err != nil {
		s.logger().Debug("removing refused import's cache entry", "mod", preEnrichID, "version", preEnrichVersion, "err", err)
	}
	if !result.Renamed {
		return
	}
	if err := gameCache.Delete(game.ID, result.Mod.SourceID, result.Mod.ID, result.Mod.Version); err != nil {
		s.logger().Debug("removing refused import's renamed cache entry", "mod", result.Mod.ID, "version", result.Mod.Version, "err", err)
	}
}

// enrichImportedMod folds the source's metadata into result.Mod when the
// caller pinned a real source and mod ID (--id/--source), and returns the
// source file this archive corresponds to (#139), or nil. Every failure
// here is a warning and never fatal: an unconfigured source, an offline
// metadata fetch and a failed file listing each leave the import to proceed
// under the archive's own detected identity.
func (s *Service) enrichImportedMod(ctx context.Context, game *domain.Game, archivePath string, result *ImportArchiveResult, opts ImportArchiveOptions, step func(DeployPhase, string), warn func(string, ...any)) *domain.DownloadableFile {
	if opts.ModID == "" || opts.SourceID == "" || opts.SourceID == domain.SourceLocal {
		return nil
	}

	sourceGameID, ok := game.SourceIDs[opts.SourceID]
	if !ok {
		warn("source %s is not configured for this game; skipping metadata fetch", opts.SourceID)
		return nil
	}

	step(ImportArchiveFetching, "Fetching metadata from "+opts.SourceID+"...")
	mod, err := s.GetMod(ctx, opts.SourceID, sourceGameID, opts.ModID)
	if err != nil {
		warn("could not fetch metadata: %v", err)
		return nil
	}

	// Apply metadata from source, keeping local file info.
	result.Mod.Name = mod.Name
	result.Mod.Author = mod.Author
	result.Mod.Summary = mod.Summary
	result.Mod.SourceURL = mod.SourceURL
	result.Mod.PictureURL = mod.PictureURL
	if mod.Version != "" && result.Mod.Version == "unknown" {
		result.Mod.Version = mod.Version
	}

	// #139: resolve which of the source's files this archive is (exact
	// filename first, else the sole file at the imported version - the user
	// asserted the mod identity via --id), so the cache entry can be
	// marker-stamped and the row records real FileIDs. Non-fatal: an
	// offline/failed listing keeps today's marker-less import.
	file, ferr := s.resolveImportedFile(ctx, opts.SourceID, mod, filepath.Base(archivePath), result.Mod.Version, true)
	if ferr != nil {
		warn("could not resolve source file for archive: %v", ferr)
		return nil
	}
	if file == nil {
		return nil
	}
	// The matched file's own version is authoritative - adopt it so the
	// cache entry, DB row, and marker all agree with what future source-side
	// resolutions will report.
	if file.Version != "" && file.Version != result.Mod.Version {
		result.Mod.Version = file.Version
	}
	return file
}
