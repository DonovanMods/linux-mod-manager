// Package core: this file holds the archive-import flow - PlanImportArchive/
// ApplyImportArchive, the ImportArchive convenience that runs both, and the
// types they own - lifted out of cmd/lmm/import.go's doImport tail by v2
// Phase 2 Unit K (#291) and given its Plan/Apply pair by #314. It is `lmm
// import <archive>`: take ONE local mod file, cache it, optionally enrich it
// from its source (--id), deploy it, and register it in a profile.
//
// Shape note (how the plan became possible). Unit K recorded that this flow
// could not be previewed: the only decision point is the file-conflict
// overwrite question, and conflicts were computed from the mod's CACHE
// ENTRY, which does not exist until the archive has been extracted into it.
// #314 removes that premise rather than living with it. The plan LISTS the
// archive instead of ingesting it (archive_listing.go), normalises the
// members through the very functions the ingest uses, and asks the DB for
// conflicts on that path list - so a preview costs one archive read and
// leaves nothing on disk.
//
// What survives is the mid-Apply gate. An ingest can still turn up a
// conflict set the plan did not predict (the archive or the profile moved
// under it), and v2 Phase 3 Ruling 1 keeps that decision a typed error
// (*ConflictError) the frontend answers with opts.AcceptConflicts, so core
// never calls back into it. The difference Ruling 18 makes is that the
// frontend now answers it from the PLAN, before Apply runs at all: there is
// one ingest, one identity, and one import readout per user-level import.
package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
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
	// ApplyImportArchive return *ConflictError carrying it, before the
	// hooks, the deploy, the DB row and the profile ref - and the cache
	// entry the call created is removed again (discardImportedCacheEntry),
	// so a refusal leaves managed state (DB, profile, game tree) untouched.
	// That removal only undoes what this call created: when an entry already
	// existed at a reproducible identity (--source/--id, or a NexusMods
	// filename), the import has already overwritten it before the conflict
	// gate runs, and a refusal does not restore it (#310).
	//
	// Since #314 a frontend answers the question from the PLAN
	// (ImportArchivePlan.Conflicts, computed with no ingest at all) and
	// calls ApplyImportArchive ONCE with this set - the Ruling 7 accept
	// re-run, and the second import readout it printed, are gone (Ruling
	// 18). Apply still recomputes the conflicts from what it actually
	// ingested: a set that no longer matches the plan's is ErrStalePlan, and
	// a non-empty one this flag has not answered is *ConflictError.
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
//     install.before_all/before_each hook failure. The first three are
//     raised at PLAN time since #314 (that is where the enrichment runs);
//     ApplyImportArchive copies ImportArchivePlan.Warnings in ahead of its
//     own, so a Result-only consumer still sees every one of them.
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

// ImportArchivePlan is the pure, displayable plan for importing ONE local
// archive into a profile (#314): the identity the import would record, the
// files it would contribute to the game directory, the conflicts that would
// have to be accepted, and the consequences (hooks, merged artifact) the
// file list cannot express. Computing it reads the archive's table of
// contents, the source's metadata and the DB, and writes NOTHING - a caller
// may compute it speculatively, render it, and discard it.
type ImportArchivePlan struct {
	// Archive is the archive path as given, echoed so a rendered plan names
	// the file it describes.
	Archive string `json:"archive"`

	// Mod is the RESOLVED identity the import would record: source, ID,
	// name and version, post-enrichment. For an unlinked archive the local
	// uuid is minted HERE, once, and carried into the apply - which is what
	// makes the ID a rendered plan prints the ID that gets persisted
	// (Ruling 18).
	Mod domain.Mod `json:"mod"`
	// LinkedSource is the source the row would be linked to ("local" for an
	// unlinked import), mirroring ImportArchiveResult.LinkedSource.
	LinkedSource string `json:"linked_source"`
	// AutoDetected reports that the identity was parsed from the archive's
	// filename rather than given, mirroring ImportArchiveResult.AutoDetected.
	AutoDetected bool `json:"auto_detected"`

	// Files is the sorted, game-dir-relative list of files the import would
	// contribute - empty for a DeployCompile native merge source, which is
	// validate+retain only and reaches the game through the merged artifact.
	// It is derived from the archive's LISTING through the same
	// normalisation the ingest applies (importDeployablePaths), so it is
	// exactly what the cache entry will hold.
	Files []string `json:"files"`
	// Conflicts is every file in Files another mod already owns in this
	// profile - the question opts.AcceptConflicts answers. Computed even
	// under opts.Force (a preview states what WOULD be overwritten); it is
	// the GATE, not the computation, that Force skips.
	Conflicts []Conflict `json:"conflicts"`

	// MergedArtifact is what the import would do to the profile's merged
	// artifact, or nil for no merged-artifact consequence - the same Ruling
	// 8 modelling `uninstall --dry-run` and `purge --dry-run` use.
	MergedArtifact *MergedArtifactEffect `json:"merged_artifact,omitzero"`
	// Hooks lists the install.* hooks the import would run, in run order.
	Hooks []string `json:"hooks"`

	// EntryPreExists reports that a cache entry already lived at this
	// archive's identity before the import - so a conflict refusal will
	// leave it alone rather than removing it (#310, and see
	// discardImportedCacheEntry).
	EntryPreExists bool `json:"entry_pre_exists"`

	// Warnings holds the enrichment diagnostics raised while COMPUTING this
	// plan - an unmapped source, a failed metadata fetch, a failed
	// source-file resolution - each with no prefix baked in (print them as
	// `Warning: %s`). Never nil. ApplyImportArchive copies them into
	// ImportArchiveResult.Warnings ahead of its own, so a Result-only
	// consumer still sees them.
	Warnings []string `json:"warnings"`

	// ident is the identity the INGEST keys its cache write by, before any
	// enrichment rename moves it onto Mod's. Carried so the apply writes
	// where the plan said it would - and so an unlinked import's uuid is
	// minted exactly once.
	ident importIdentity `json:"-"`
	// resolvedFile is the source file this archive resolved to (#139), or
	// nil. Resolved here because the enrichment that finds it also finalises
	// Mod's version; the apply only stamps its marker.
	resolvedFile *domain.DownloadableFile `json:"-"`
	// fingerprint is the archive's own size+mtime at plan time: the apply
	// refuses a plan whose archive changed underneath it.
	fingerprint archiveFingerprint `json:"-"`
	// snapshot is the installed-mod staleness precondition every Apply
	// re-checks (checkPlanFresh).
	snapshot installedSnapshot `json:"-"`
}

// archiveFingerprint is the cheap identity of the FILE a plan was computed
// from - its size and modification time. It is not a checksum: hashing a
// multi-gigabyte archive to guard a plan a user will act on within seconds
// costs far more than it buys, and any edit that leaves both size and mtime
// untouched is indistinguishable from no edit to every other tool too.
type archiveFingerprint struct {
	size    int64
	modTime time.Time
}

// fingerprintArchive stats path. A missing or unreadable archive fails with
// the same "archive not found" wording Importer.Import uses, so planning and
// ingesting an absent file report it identically.
func fingerprintArchive(path string) (archiveFingerprint, error) {
	info, err := os.Stat(path)
	if err != nil {
		return archiveFingerprint{}, fmt.Errorf("archive not found: %w", err)
	}
	return archiveFingerprint{size: info.Size(), modTime: info.ModTime()}, nil
}

// PlanImportArchive computes what importing archivePath into profileName
// would do, WITHOUT touching managed state: no cache entry, no DB row, no
// profile write, and nothing left in the staging root (R-B2). The archive is
// listed, not extracted - archive/zip natively, `7z l -slt` for .7z/.rar,
// with the extractor's own "install p7zip-full" error when 7z is missing.
//
// The metadata enrichment (opts.ModID with a configured opts.SourceID) runs
// HERE, because it is a read and because it is what finalises the mod's
// identity and version: a plan whose printed ID were provisional would defeat
// the point (Ruling 18). Its diagnostics land in plan.Warnings.
func (s *Service) PlanImportArchive(ctx context.Context, game *domain.Game, profileName, archivePath string, opts ImportArchiveOptions) (*ImportArchivePlan, error) {
	fingerprint, err := fingerprintArchive(archivePath)
	if err != nil {
		return nil, err
	}

	filename := filepath.Base(archivePath)
	importOpts := ImportOptions{SourceID: opts.SourceID, ModID: opts.ModID, ProfileName: profileName}
	ident := resolveImportIdentity(filename, importOpts)

	// A MINTED identity is a fresh uuid: it cannot name a pre-existing
	// entry, and the apply uses this very uuid, so the question is only
	// meaningful for a reproducible identity (unit P review, Important 3).
	gameCache := s.GetGameCache(game)
	entryPreExists := !ident.minted &&
		gameCache.Exists(game.ID, ident.sourceID, ident.modID, ident.version)

	// The compile source answers the format questions for a DeployCompile
	// game (#256), and a game whose compiler cannot be resolved fails HERE
	// for the same reason Import fails: without it core cannot tell a native
	// merge archive from anything else.
	var mc source.MergeCompiler
	if game.DeployMode == domain.DeployCompile {
		if mc, err = s.mergeCompilerSourceForGame(game.ID); err != nil {
			return nil, err
		}
	}
	kind := classifyImportArchive(game, mc, filename)

	var members []archiveMember
	switch kind {
	case importKindMergeSource, importKindConvertPak:
		// The ingest validates before it retains; a plan that skipped this
		// would promise an import that cannot happen.
		if err := mc.ValidateSource(archivePath); err != nil {
			return nil, fmt.Errorf("validating %s: %w", filename, err)
		}
	case importKindExtract:
		if !NewExtractor().CanExtract(archivePath) {
			return nil, fmt.Errorf("unsupported archive format: %s", filepath.Ext(archivePath))
		}
		if members, err = listArchiveMembers(ctx, NewExtractor(), archivePath); err != nil {
			return nil, err
		}
	}

	files, err := importDeployablePaths(kind, filename, members)
	if err != nil {
		return nil, err
	}

	plan := &ImportArchivePlan{
		Archive:        archivePath,
		LinkedSource:   ident.sourceID,
		AutoDetected:   ident.autoDetected,
		Files:          files,
		Conflicts:      []Conflict{},
		EntryPreExists: entryPreExists,
		Warnings:       []string{},
		ident:          ident,
		fingerprint:    fingerprint,
	}
	plan.Mod = domain.Mod{
		ID:       ident.modID,
		SourceID: ident.sourceID,
		Name:     importedModName(kind, filename, ident.version, members),
		Version:  ident.version,
		GameID:   game.ID,
	}

	warn := func(format string, args ...any) {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(format, args...))
	}
	plan.resolvedFile = s.enrichImportedMod(ctx, game, archivePath, &plan.Mod, opts, warn)

	// Conflicts come from the plan's own path list, not from a cache entry
	// that does not exist yet - the Installer.GetConflicts twin that takes
	// paths (#314). The linker is irrelevant to a conflict question, so the
	// game's default installer answers it without reading the profile.
	conflicts, err := s.getInstaller(game).conflictsForPaths(ctx, game, &plan.Mod, profileName, files)
	if err != nil {
		return nil, err
	}
	if len(conflicts) > 0 {
		plan.Conflicts = conflicts
	}

	plan.Hooks = installHookNames(s.resolvedHooksForPlan(ctx, game, profileName), opts.SkipHooks)
	plan.MergedArtifact = s.mergedArtifactEffectForImport(ctx, game, profileName,
		kind == importKindMergeSource || kind == importKindConvertPak)

	if plan.snapshot, err = s.currentInstalledSnapshot(ctx, game.ID, profileName); err != nil {
		return nil, err
	}
	return plan, nil
}

// ImportArchive imports one local archive into profileName: PlanImportArchive
// then ApplyImportArchive, with the plan's import readout emitted in between
// so this convenience produces the same event stream it always has. sink may
// be nil.
//
// It is the documented Ruling 10 convenience for a caller that has no
// decision to make (nothing to preview, no conflict to prompt about): a
// frontend that renders a preview, or that must answer the conflict question,
// calls the two halves itself - which is exactly what `lmm import <archive>`
// does since #314.
func (s *Service) ImportArchive(ctx context.Context, game *domain.Game, profileName, archivePath string, opts ImportArchiveOptions, sink EventSink) (*ImportArchiveResult, error) {
	plan, err := s.PlanImportArchive(ctx, game, profileName, archivePath, opts)
	if err != nil {
		return &ImportArchiveResult{}, err
	}
	if sink != nil && ImportEnrichmentRuns(game, opts) {
		// The progress line for work PlanImportArchive already did. A
		// frontend that renders a preview prints it AHEAD of the plan
		// instead, where a progress line belongs.
		sink(StepEvent{
			Scope: Scope{Op: OpImport}, Phase: ImportArchiveFetching,
			Detail: "Fetching metadata from " + opts.SourceID + "...",
		})
	}
	EmitImportArchiveReadout(plan, sink)
	return s.ApplyImportArchive(ctx, game, profileName, plan, opts, sink)
}

// EmitImportArchiveReadout emits the import readout a frontend renders from a
// finished plan: the plan's enrichment warnings, then the
// Mod/Source/ID/Version/Author/URL/Files block.
//
// It is the ONE place that readout is produced (Ruling 18: it prints once per
// user-level import, and the ID it prints is the ID the apply persists), so a
// frontend never restates the vocabulary. The metadata-fetch progress line is
// deliberately NOT here: it announces work the plan does, so a caller emits it
// before planning (see ImportEnrichmentRuns). sink may be nil.
func EmitImportArchiveReadout(plan *ImportArchivePlan, sink EventSink) {
	if sink == nil {
		return
	}
	scope := Scope{
		Op:      OpImport,
		Mod:     &domain.ModReference{SourceID: plan.Mod.SourceID, ModID: plan.Mod.ID},
		ModName: plan.Mod.Name,
	}
	step := func(phase DeployPhase, msg string) {
		sink(StepEvent{Scope: scope, Phase: phase, Detail: msg})
	}

	for _, w := range plan.Warnings {
		step(ImportArchiveWarning, w)
	}

	step(ImportArchiveDetected, "Mod: "+plan.Mod.Name)
	step(ImportArchiveDetail, "Source: "+plan.LinkedSource)
	step(ImportArchiveDetail, "ID: "+plan.Mod.ID)
	if plan.Mod.Version != "unknown" {
		step(ImportArchiveDetail, "Version: "+plan.Mod.Version)
	}
	if plan.Mod.Author != "" {
		step(ImportArchiveDetail, "Author: "+plan.Mod.Author)
	}
	if plan.Mod.SourceURL != "" {
		step(ImportArchiveDetail, "URL: "+plan.Mod.SourceURL)
	}
	if plan.AutoDetected {
		step(ImportArchiveDetail, "(auto-detected from filename)")
	}
	step(ImportArchiveDetail, fmt.Sprintf("Files: %d", len(plan.Files)))
}

// ImportEnrichmentRuns reports whether PlanImportArchive would fetch source
// metadata for this import - a real, non-local source that is mapped for the
// game, plus a mod ID. It is what gates the "Fetching metadata from <source>..."
// progress line, which a frontend prints immediately BEFORE calling
// PlanImportArchive (that is the work it announces); exported so cmd and the
// ImportArchive convenience cannot disagree about when it applies.
func ImportEnrichmentRuns(game *domain.Game, opts ImportArchiveOptions) bool {
	if opts.ModID == "" || opts.SourceID == "" || opts.SourceID == domain.SourceLocal {
		return false
	}
	_, mapped := game.SourceIDs[opts.SourceID]
	return mapped
}

// ApplyImportArchive performs plan: it caches the archive under the plan's
// identity, stamps the resolved file's completion marker, deploys it and
// records it on the DB row and the profile ref, running the install.* hook
// quartet around the deploy exactly as the pre-lift CLI did. sink may be nil.
//
// It is ONE mutation and runs under beginOp, behind two preconditions: the
// profile's installed-mod set must still match the plan's (checkPlanFresh)
// and the archive must still be the file the plan was computed from
// (fingerprint) - either mismatch is ErrStalePlan and the frontend re-plans.
// The conflict set is recomputed from what was actually ingested and must
// still match plan.Conflicts; see opts.AcceptConflicts for the gate itself.
func (s *Service) ApplyImportArchive(ctx context.Context, game *domain.Game, profileName string, plan *ImportArchivePlan, opts ImportArchiveOptions, sink EventSink) (*ImportArchiveResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &ImportArchiveResult{}, err
	}
	defer release()

	if err := s.checkPlanFresh(ctx, game.ID, profileName, plan.snapshot); err != nil {
		return &ImportArchiveResult{}, err
	}
	current, err := fingerprintArchive(plan.Archive)
	if err != nil {
		return &ImportArchiveResult{}, err
	}
	if current != plan.fingerprint {
		return &ImportArchiveResult{}, fmt.Errorf("%w: %s changed since the plan was computed", ErrStalePlan, plan.Archive)
	}

	return s.applyImportArchive(ctx, game, profileName, plan, opts, sink)
}

func (s *Service) applyImportArchive(ctx context.Context, game *domain.Game, profileName string, plan *ImportArchivePlan, opts ImportArchiveOptions, sink EventSink) (*ImportArchiveResult, error) {
	result := &ImportArchiveResult{}
	archivePath := plan.Archive

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

	// The plan's enrichment diagnostics are this result's first warnings, so
	// a Result-only consumer sees every warning the flow produced, in order
	// (R-B1, as ruled). They are NOT re-emitted: the readout that carried
	// them was already rendered from the plan (Ruling 18).
	result.Warnings = append(result.Warnings, plan.Warnings...)

	importOpts := ImportOptions{
		SourceID:    opts.SourceID,
		ModID:       opts.ModID,
		ProfileName: profileName,
	}

	gameCache := s.GetGameCache(game)
	// The identity the plan resolved - one uuid per user-level import, so an
	// accepted conflict no longer mints a second one (Ruling 18).
	ident := plan.ident
	// Whether the entry the ingest is about to (re)create was already in the
	// cache is the PLAN's answer, taken before anything was written (unit P
	// review, Important 3).
	entryPreExisted := plan.EntryPreExists

	imported, err := s.newImporter(game).importWithIdentity(ctx, archivePath, game, importOpts, ident)
	if err != nil {
		return result, fmt.Errorf("import failed: %w", err)
	}
	// The mod as the PLAN resolved it - post-enrichment, and the identity the
	// readout already printed - rather than the ingest's own pre-enrichment
	// value, so the ID printed is the ID persisted.
	planned := plan.Mod
	result.Mod = &planned
	result.LinkedSource = plan.LinkedSource
	result.AutoDetected = plan.AutoDetected
	result.Deployed = imported.FilesExtracted

	// The identity the ingest recorded, before the enrichment rename moves it
	// - the cache entry currently lives under exactly this ID and version.
	preEnrichID, preEnrichVersion := ident.modID, ident.version

	resolvedFile := plan.resolvedFile

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

	// The import readout is NOT emitted here: Ruling 18 makes it the plan's
	// (EmitImportArchiveReadout), so it prints once per user-level import
	// and names the identity this apply is about to persist.

	// The installer is built from the already-resolved method so both stay
	// consistent (and the profile file is only read once).
	linkMethod, err := s.GetEffectiveLinkMethod(ctx, game, profileName)
	if err != nil {
		return result, err
	}
	installer := s.newInstallerWithLinker(game, s.getLinker(linkMethod))

	// Force skips the conflict CHECK entirely, not merely the refusal - the
	// contract this flow has always had, and the reason a forced import
	// raises no "could not check conflicts" note when the entry is missing.
	if !opts.Force {
		conflicts, cerr := installer.GetConflicts(ctx, game, result.Mod, profileName)
		switch {
		case cerr != nil:
			note(ImportArchiveNote, "Warning: could not check conflicts: %v", cerr)
		case !sameConflicts(conflicts, plan.Conflicts):
			// What was actually ingested conflicts differently from what the
			// plan promised, so the decision the caller made (or is about to
			// make) was made about a different set. Re-plan (#314, R-B3).
			s.discardImportedCacheEntry(game, result, preEnrichID, preEnrichVersion, entryPreExisted)
			return result, fmt.Errorf("%w: file conflicts changed since the plan was computed", ErrStalePlan)
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

	// v2 Phase 3 Ruling 16 (B): the same lazy profile creation the install
	// flows do, through the same helper - which compares with errors.Is
	// (this site used ==, so a wrapped ErrProfileNotFound would have fallen
	// through silently) and reports a read it could not answer instead of
	// treating it as "the profile is fine".
	pm := s.NewProfileManager()
	if err := ensureProfileExists(ctx, pm, game.ID, profileName); err != nil {
		// NEW-6 (v2 Phase 3 Ruling 16 (B) review): a cancellation here means
		// the profile was never created, so the completeProfileWrite below
		// is doomed to fail (UpsertMod's LoadProfile finds nothing) - report
		// it now, ahead of that doomed write, rather than a Note.
		if cerr := ctx.Err(); cerr != nil {
			return result, cerr
		}
		note(ImportArchiveProfileNote, "Warning: could not create profile: %v", err)
	}
	modRef := domain.ModReference{
		SourceID: result.Mod.SourceID,
		ModID:    result.Mod.ID,
		Version:  result.Mod.Version,
		FileIDs:  result.FileIDs,
	}
	// Ruling 16 (A): the DB row is already saved, so the profile ref that
	// completes it is written even under a cancelled ctx; the cancellation
	// itself is then fatal rather than a Note.
	if err := completeProfileWrite(ctx, func(ctx context.Context) error {
		return pm.UpsertMod(ctx, game.ID, profileName, modRef)
	}); err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return result, cerr
		}
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

// enrichImportedMod folds the source's metadata into imported when the
// caller pinned a real source and mod ID (--id/--source), and returns the
// source file this archive corresponds to (#139), or nil. Every failure
// here is a warning and never fatal: an unconfigured source, an offline
// metadata fetch and a failed file listing each leave the import to proceed
// under the archive's own detected identity.
//
// It runs at PLAN time (#314): it is a pure read, and it is what finalises
// the identity and version the plan's readout prints and the apply persists.
// Its "Fetching metadata from..." progress line is therefore NOT emitted
// from here - a plan emits nothing - but rendered from the plan alongside
// the readout (EmitImportArchiveReadout / importEnrichmentRuns).
func (s *Service) enrichImportedMod(ctx context.Context, game *domain.Game, archivePath string, imported *domain.Mod, opts ImportArchiveOptions, warn func(string, ...any)) *domain.DownloadableFile {
	if opts.ModID == "" || opts.SourceID == "" || opts.SourceID == domain.SourceLocal {
		return nil
	}

	sourceGameID, ok := game.SourceIDs[opts.SourceID]
	if !ok {
		warn("source %s is not configured for this game; skipping metadata fetch", opts.SourceID)
		return nil
	}

	mod, err := s.GetMod(ctx, opts.SourceID, sourceGameID, opts.ModID)
	if err != nil {
		warn("could not fetch metadata: %v", err)
		return nil
	}

	// Apply metadata from source, keeping local file info.
	imported.Name = mod.Name
	imported.Author = mod.Author
	imported.Summary = mod.Summary
	imported.SourceURL = mod.SourceURL
	imported.PictureURL = mod.PictureURL
	if mod.Version != "" && imported.Version == "unknown" {
		imported.Version = mod.Version
	}

	// #139: resolve which of the source's files this archive is (exact
	// filename first, else the sole file at the imported version - the user
	// asserted the mod identity via --id), so the cache entry can be
	// marker-stamped and the row records real FileIDs. Non-fatal: an
	// offline/failed listing keeps today's marker-less import.
	file, ferr := s.resolveImportedFile(ctx, opts.SourceID, mod, filepath.Base(archivePath), imported.Version, true)
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
	if file.Version != "" && file.Version != imported.Version {
		imported.Version = file.Version
	}
	return file
}

// sameConflicts reports whether two conflict lists describe the same set,
// order-independent: the plan derives its list from the archive's sorted
// member paths and the apply's from the cache entry's own walk order, so
// comparing them positionally would flag a difference that is not one.
func sameConflicts(got, want []Conflict) bool {
	if len(got) != len(want) {
		return false
	}
	key := func(c Conflict) string {
		return c.RelativePath + "\x00" + c.CurrentSourceID + "\x00" + c.CurrentModID
	}
	seen := make(map[string]int, len(want))
	for _, c := range want {
		seen[key(c)]++
	}
	for _, c := range got {
		k := key(c)
		if seen[k] == 0 {
			return false
		}
		seen[k]--
	}
	return true
}
