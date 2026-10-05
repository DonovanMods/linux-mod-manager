// Package core: this file holds the update-from-archive flow (#530) -
// PlanUpdateFromArchive/ApplyUpdateFromArchive - `lmm update <mod>
// --from-file <archive>`, and the web UI's "Update from file...".
//
// The case it exists for: a source that will not serve a file through its API
// (source.ErrManualDownload - a CurseForge author's third-party opt-out). The
// user downloads the file by hand from the mod's page (#513's link), and
// before this flow had to re-enter the source and mod ID to `lmm import` it,
// which installed it as a fresh mod rather than updating the installed one:
// no previous_version, so no rollback.
//
// It is an UPDATE, not an import. The identity (source, mod ID, game,
// profile) is the installed row's; the lock, the update policy and the
// enabled state are kept; previous_version/previous_file_ids are recorded so
// `lmm update rollback` works; and the update hooks run around a Replace of
// the old deployment. Everything from "the new files are cached" on is
// ApplyUpdate's own tail (commitUpdate) - only the way the files reach the
// cache differs: the archive-import ingest (importWithIdentity) under the
// installed identity, instead of a download.
//
// Which version and file ID the archive is decides whether future update
// checks - file-ID based on CurseForge (#504) - stay correct, so the plan
// works it out from the source and the archive's name (see
// UpdateFromArchivePlan.Match), and a name that is not the file the update
// check advertised is a decision the user makes: Apply refuses it with
// *ArchiveMismatchError unless AcceptMismatch answers it (v2 Phase 3
// Ruling 1 - the conflict gate's shape).
package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// UpdateFromArchiveOptions configures PlanUpdateFromArchive and
// ApplyUpdateFromArchive.
type UpdateFromArchiveOptions struct {
	// Version is `--version`: the version to record, overriding whatever
	// the plan worked out (never the file ID). Required only when nothing
	// else names one - see ArchiveVersionRequiredError.
	Version string
	// AcceptMismatch answers the filename gate: update from an archive that
	// is not the file the update check advertised (ArchiveMatchMismatch).
	// Apply-time only.
	AcceptMismatch bool
	// Force continues past a failing uninstall.before_each/
	// install.before_each hook, as UpdateOptions.Force. Apply-time only.
	Force bool
	// SkipHooks runs no hooks (`--no-hooks`).
	SkipHooks bool
}

// ArchiveMatch classifies how an archive's name relates to the files the
// mod's source lists - see UpdateFromArchivePlan.Match, and
// ImportArchivePlan.Match for an install from a file (#535), where the
// "advertised" file is the one the install would download.
type ArchiveMatch string

// The ArchiveMatch values.
const (
	// ArchiveMatchAdvertised: the update check advertised a file, and the
	// archive is it. Its version and file ID are adopted.
	ArchiveMatchAdvertised ArchiveMatch = "advertised"
	// ArchiveMatchMismatch: the update check advertised a file, and the
	// archive is some other file - another flavor or loader's build, or a
	// name the source does not list. Apply refuses it unless
	// AcceptMismatch.
	ArchiveMatchMismatch ArchiveMatch = "mismatch"
	// ArchiveMatchListed: nothing is advertised, but the archive is a file
	// the source lists. Its version and file ID are adopted.
	ArchiveMatchListed ArchiveMatch = "listed"
	// ArchiveMatchNone: nothing is advertised and the source lists no file
	// of this name (or there is no source to ask). The version comes from
	// --version or the archive's name, and no file ID is recorded.
	ArchiveMatchNone ArchiveMatch = "none"
)

// ArchiveFileRef names one file on the mod's source.
type ArchiveFileRef struct {
	ID       string `json:"id"`
	FileName string `json:"file_name"`
	Version  string `json:"version,omitempty"`
}

// UpdateFromArchivePlan is the pure, displayable plan for updating one
// installed mod from a local archive. Computing it reads the archive's
// listing, the source (one update check, one file listing) and the DB, and
// writes nothing.
type UpdateFromArchivePlan struct {
	// Archive is the archive path as given; ArchiveName its base name.
	Archive     string `json:"archive"`
	ArchiveName string `json:"archive_name"`
	// Mod is the installed mod being updated, as it stands now.
	Mod domain.InstalledMod `json:"mod"`
	// FromVersion is Mod.Version; ToVersion the version the update records.
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
	// FileIDs is the file identity the update records on the row and the
	// profile ref - the matched file's ID, or empty when the archive is no
	// file the source lists. Never nil.
	FileIDs []string `json:"file_ids"`

	// Match is how the archive's name relates to the source's files. Names
	// are compared case-insensitively, and the suffix a browser adds to a
	// repeated download (" (1)", "-1", ...) is ignored when the name without
	// it is a listed file and the name with it is not (MatchNormalized).
	Match ArchiveMatch `json:"match"`
	// MatchedFile is the listed file the archive's name matched, or nil.
	MatchedFile *ArchiveFileRef `json:"matched_file,omitempty"`
	// MatchNormalized reports that MatchedFile matched only once the
	// download-duplicate suffix was ignored.
	MatchNormalized bool `json:"match_normalized,omitzero"`
	// Advertised is the file the update check advertised for this mod, or
	// nil when it advertised none (no update, a pinned or local mod, or a
	// check that failed - that failure is in Warnings).
	Advertised *ArchiveFileRef `json:"advertised,omitempty"`

	// Locked/LockedVersion are the profile ref's lock state; Refusal is the
	// lock refusal's sentence when Locked (UpdatePlan.Refusal's wording) -
	// Apply refuses a locked mod.
	Locked        bool   `json:"locked"`
	LockedVersion string `json:"locked_version,omitempty"`
	Refusal       string `json:"refusal,omitempty"`

	// Files is the sorted, game-dir-relative list of files the new version
	// would deploy, read off the archive's listing (ImportArchivePlan.Files).
	Files []string `json:"files"`
	// Hooks lists the update hooks Apply would run, in run order.
	Hooks []string `json:"hooks"`
	// Warnings holds the plan's diagnostics, unprefixed: a failed update
	// check, no file ID to record, the adapter's layout notes. A mismatch is
	// not among them - Match states it, and a frontend words it beside its
	// own way to proceed (ArchiveMismatchError.Sentence). Never nil.
	Warnings []string `json:"warnings"`

	// matchedFileID is the file whose completion marker Apply stamps.
	matchedFileID string `json:"-"`
	// entryPreExists: a cache entry already lived at ToVersion, so a failed
	// apply leaves it alone (discardImportedCacheEntry's rule).
	entryPreExists bool               `json:"-"`
	fingerprint    archiveFingerprint `json:"-"`
	snapshot       installedSnapshot  `json:"-"`
}

// ArchiveMismatchError is the refusal of an archive that is not the file
// lmm expected it to be, when AcceptMismatch did not answer it: for
// ApplyUpdateFromArchive the file the update check advertised, and for an
// install from a file (ApplyImportArchive with InstallFromFile, #535) the
// file the install would download. Nothing has changed when it is returned.
type ArchiveMismatchError struct {
	ArchiveName string
	// Advertised is the file expected: the advertised update, or the file
	// the install would download.
	Advertised ArchiveFileRef
	// Matched is the listed file the archive is, or nil for a name the
	// source does not list.
	Matched *ArchiveFileRef
	// Install reports an install from a file rather than an update.
	Install bool
}

// Error names both files and the way to proceed anyway.
func (e *ArchiveMismatchError) Error() string {
	if e.Install {
		return e.Sentence() + "; install from it anyway with --accept-mismatch"
	}
	return e.Sentence() + "; update from it anyway with --accept-mismatch"
}

// Sentence is the refusal without its remedy, for a frontend that words its
// own way to proceed beside it - the CLI's plan warning.
func (e *ArchiveMismatchError) Sentence() string {
	is := "a file the source does not list"
	if e.Matched != nil {
		is = fmt.Sprintf("the source's file %s", e.Matched.FileName)
	}
	expected := "the update the source advertised"
	if e.Install {
		expected = "the file lmm would install"
	}
	return fmt.Sprintf("%s is %s, not %s (%s) - it may be another flavor or loader's build",
		e.ArchiveName, is, expected, e.Advertised.FileName)
}

// Details implements the --json error envelope's extension point.
func (e *ArchiveMismatchError) Details() any {
	return archiveMismatchDetails{ArchiveName: e.ArchiveName, Advertised: e.Advertised, Matched: e.Matched, Install: e.Install}
}

type archiveMismatchDetails struct {
	ArchiveName string          `json:"archive_name"`
	Advertised  ArchiveFileRef  `json:"advertised"`
	Matched     *ArchiveFileRef `json:"matched,omitempty"`
	Install     bool            `json:"install,omitzero"`
}

// ErrArchiveIsInstalledVersion is PlanUpdateFromArchive's refusal of an
// archive that resolves to the version already installed: the ingest keys
// the cache by version, so it would overwrite the live entry the deployment
// and a rollback both read. Callers branch with errors.Is.
var ErrArchiveIsInstalledVersion = errors.New("the archive is the installed version")

// ArchiveVersionRequiredError is PlanUpdateFromArchive's refusal when
// nothing says which version an archive is: no advertised or listed file
// matches its name, its name carries no version, and no --version was given.
type ArchiveVersionRequiredError struct {
	ArchiveName string
	SourceID    string
	ModID       string
}

// Error names the archive and the remedy.
func (e *ArchiveVersionRequiredError) Error() string {
	return fmt.Sprintf("cannot tell which version %s is: it is no file the source lists and its name carries no version - give it with --version", e.ArchiveName)
}

// Details implements the --json error envelope's extension point; the web
// UI reads version_required to ask for the version.
func (e *ArchiveVersionRequiredError) Details() any {
	return archiveVersionRequiredDetails{ArchiveName: e.ArchiveName, SourceID: e.SourceID, ModID: e.ModID, VersionRequired: true}
}

type archiveVersionRequiredDetails struct {
	ArchiveName     string `json:"archive_name"`
	SourceID        string `json:"source_id"`
	ModID           string `json:"mod_id"`
	VersionRequired bool   `json:"version_required"`
}

// PlanUpdateFromArchive computes what updating (sourceID, modID) in
// profileName from archivePath would do. Network reads (one update check
// for this mod, one file listing) are expected; nothing is written.
//
// It refuses outright - an error, no plan - what can never be applied: an
// archive that cannot be ingested, a Workshop item (Steam updates those), an
// archive whose version nothing names (*ArchiveVersionRequiredError), and
// one that resolves to the installed version (the ingest keys the cache by
// version, so it would overwrite the live entry the deployment and a
// rollback both read). A locked mod gets a plan carrying Refusal, as
// PlanUpdate's does; Apply refuses it.
func (s *Service) PlanUpdateFromArchive(ctx context.Context, game *domain.Game, profileName, sourceID, modID, archivePath string, opts UpdateFromArchiveOptions) (*UpdateFromArchivePlan, error) {
	// #462: an update writes into the game directory, which holds the active
	// profile's mods, so it acts for that profile alone.
	if err := s.requireActiveProfile(ctx, game.ID, profileName, VerbUpdate); err != nil {
		return nil, err
	}
	mod, err := s.GetInstalledMod(ctx, sourceID, modID, game.ID, profileName)
	if err != nil {
		return nil, err
	}
	if err := refuseExternal("update", mod, ReasonExternalNoUpdate); err != nil {
		return nil, err
	}
	fingerprint, err := fingerprintArchive(archivePath)
	if err != nil {
		return nil, err
	}

	plan := &UpdateFromArchivePlan{
		Archive:     archivePath,
		ArchiveName: filepath.Base(archivePath),
		Mod:         *mod,
		FromVersion: mod.Version,
		FileIDs:     []string{},
		Warnings:    []string{},
		fingerprint: fingerprint,
	}
	warn := func(format string, args ...any) {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(format, args...))
	}

	s.identifyUpdateArchive(ctx, game, plan, warn)
	if err := resolveUpdateArchiveVersion(plan, opts.Version); err != nil {
		return nil, err
	}
	if plan.ToVersion == mod.Version {
		return nil, fmt.Errorf("%w: %s is already at v%s, the version %s holds - nothing to update", ErrArchiveIsInstalledVersion, mod.Name, mod.Version, plan.ArchiveName)
	}
	// A mismatch is not repeated here: Match/Advertised/MatchedFile state
	// it, and each frontend words it beside its own way to proceed.
	if plan.Match != ArchiveMatchMismatch && len(plan.FileIDs) == 0 && mod.SourceID != domain.SourceLocal {
		warn("%s", noFileIDWarning(plan.ArchiveName))
	}

	contents, err := s.planArchiveContents(ctx, game, archivePath, plan.ToVersion)
	if err != nil {
		return nil, err
	}
	plan.Files = contents.files
	plan.Warnings = append(plan.Warnings, contents.warnings...)
	if plan.Files == nil {
		plan.Files = []string{}
	}

	locked, lockedVersion, err := s.lockState(ctx, game.ID, profileName, mod.SourceID, mod.ID)
	if err != nil {
		return nil, err
	}
	if locked {
		plan.Locked, plan.LockedVersion = true, lockedVersion
		plan.Refusal = lockedRefUnlockOnlyMessage(mod.Mod, profileName, &domain.ModReference{Version: lockedVersion})
	}

	plan.Hooks = updateHookNames(s.resolvedHooksForPlan(ctx, game, profileName), opts.SkipHooks)
	plan.entryPreExists = s.GetGameCache(game).Exists(game.ID, mod.SourceID, mod.ID, plan.ToVersion)
	if plan.snapshot, err = s.currentInstalledSnapshot(ctx, game.ID, profileName); err != nil {
		return nil, err
	}
	return plan, nil
}

// identifyUpdateArchive fills plan's Advertised, MatchedFile and Match from
// the source - one update check for this mod and one listing of its files. Every source failure is a warning: the plan then
// knows less, and says so, rather than refusing an archive the user has in
// hand. A local mod has no source to ask.
func (s *Service) identifyUpdateArchive(ctx context.Context, game *domain.Game, plan *UpdateFromArchivePlan, warn func(string, ...any)) {
	plan.Match = ArchiveMatchNone
	mod := &plan.Mod
	if mod.SourceID == domain.SourceLocal {
		return
	}

	var upd *domain.Update
	if UpdateCheckable(*mod) {
		updates, err := s.NewUpdater().CheckUpdates(ctx, game, []domain.InstalledMod{*mod}, nil, UpdateCheckOptions{})
		if err != nil {
			warn("could not check %s for an update: %v", mod.SourceID, err)
		}
		for i := range updates {
			u := &updates[i]
			if u.InstalledMod.SourceID == mod.SourceID && u.InstalledMod.ID == mod.ID && !u.RecompileNeeded {
				upd = u
				break
			}
		}
	}

	srcMod, err := s.GetMod(ctx, mod.SourceID, game.ID, mod.ID)
	if err != nil {
		warn("could not fetch %s's files from %s: %v", mod.Name, mod.SourceID, err)
		return
	}
	files, err := s.GetModFiles(ctx, mod.SourceID, srcMod)
	if err != nil {
		warn("could not fetch %s's files from %s: %v", mod.Name, mod.SourceID, err)
		return
	}

	if upd != nil {
		if adv := advertisedFile(files, *upd); adv != nil {
			plan.Advertised = fileRef(adv)
			if plan.Advertised.Version == "" {
				// A file with no label of its own is the version the check
				// advertised it as.
				plan.Advertised.Version = upd.NewVersion
			}
		}
	}
	m := classifyArchive(files, plan.ArchiveName, plan.Advertised)
	plan.Match, plan.MatchedFile, plan.MatchNormalized = m.match, m.ref, m.normalized
}

// archiveClassification is how an archive's name relates to a mod's listed
// files and the one file expected of it - see classifyArchive.
type archiveClassification struct {
	match ArchiveMatch
	// ref is the listed file the name matched, nil for none; file is the
	// listing's own entry for it.
	ref        *ArchiveFileRef
	file       *domain.DownloadableFile
	normalized bool
}

// classifyArchive matches archiveName against files (matchArchiveName) and
// says how that relates to expected - the advertised update (#530) or the
// file an install would download (#535), nil when nothing is expected:
// expected itself is ArchiveMatchAdvertised (the matched file then carries
// expected's version, which may be one the listing does not label), any
// other name is ArchiveMatchMismatch, and with nothing expected a listed
// file is ArchiveMatchListed and anything else ArchiveMatchNone.
func classifyArchive(files []domain.DownloadableFile, archiveName string, expected *ArchiveFileRef) archiveClassification {
	c := archiveClassification{match: ArchiveMatchNone}
	matched, normalized := matchArchiveName(files, archiveName)
	if matched != nil {
		c.ref, c.file, c.normalized = fileRef(matched), matched, normalized
	}
	switch {
	case expected != nil && matched != nil && matched.ID == expected.ID:
		c.match = ArchiveMatchAdvertised
		c.ref.Version = expected.Version
	case expected != nil:
		c.match = ArchiveMatchMismatch
	case matched != nil:
		c.match = ArchiveMatchListed
	}
	return c
}

// advertisedFile is the listed file upd advertises: the file its
// FileIDReplacements names (#504/#505 - the check stated that exact file),
// else the sole file listed at upd.NewVersion. Several candidates, or none
// listed, advertise nothing a name can be compared with.
func advertisedFile(files []domain.DownloadableFile, upd domain.Update) *domain.DownloadableFile {
	var named []string
	for _, id := range upd.FileIDReplacements {
		if !slices.Contains(named, id) {
			named = append(named, id)
		}
	}
	pick := func(match func(f *domain.DownloadableFile) bool) *domain.DownloadableFile {
		var found *domain.DownloadableFile
		for i := range files {
			if !match(&files[i]) {
				continue
			}
			if found != nil {
				return nil
			}
			found = &files[i]
		}
		return found
	}
	if len(named) > 0 {
		if len(named) != 1 {
			return nil
		}
		return pick(func(f *domain.DownloadableFile) bool { return f.ID == named[0] })
	}
	if upd.NewVersion == "" {
		return nil
	}
	return pick(func(f *domain.DownloadableFile) bool { return f.Version == upd.NewVersion })
}

// resolveUpdateArchiveVersion sets plan's ToVersion and FileIDs from its
// Match (#530) - see resolveArchiveVersion.
func resolveUpdateArchiveVersion(plan *UpdateFromArchivePlan, override string) error {
	version, fileID, err := resolveArchiveVersion(plan.MatchedFile, plan.ArchiveName, override, plan.Mod.SourceID, plan.Mod.ID)
	if err != nil {
		return err
	}
	if fileID != "" {
		plan.FileIDs = []string{fileID}
		plan.matchedFileID = fileID
	}
	plan.ToVersion = version
	return nil
}

// resolveArchiveVersion is the version and file ID an archive records, from
// the listed file its name matched (#530, and #535's install from a file):
//
//   - a matched file: its version and ID (an advertised or expected file
//     without a label of its own carries the version it was offered as);
//     a matched file without a label takes the archive name's version.
//   - no matched file: the archive name's version, no file ID.
//
// A mismatch records the matched file's identity too - what Apply records if
// the user accepts it. override (--version) replaces the version in every
// case, never the ID. A version nothing names is
// *ArchiveVersionRequiredError.
func resolveArchiveVersion(matched *ArchiveFileRef, archiveName, override, sourceID, modID string) (version, fileID string, err error) {
	if matched != nil && cache.VerifiableFileID(matched.ID) {
		fileID = matched.ID
	}
	if matched != nil {
		version = matched.Version
	}
	if version == "" {
		version = domain.ExtractVersionFromName(strings.TrimSuffix(archiveName, filepath.Ext(archiveName)))
	}
	if override != "" {
		version = override
	}
	if version == "" {
		return "", "", &ArchiveVersionRequiredError{ArchiveName: archiveName, SourceID: sourceID, ModID: modID}
	}
	return version, fileID, nil
}

// noFileIDWarning is the plan warning for an archive that records no file
// ID, shared by the update and the install from a file.
func noFileIDWarning(archiveName string) string {
	return archiveName + " is no file the source lists, so no file ID is recorded: future update checks compare versions only"
}

// fileRef is f as an ArchiveFileRef.
func fileRef(f *domain.DownloadableFile) *ArchiveFileRef {
	return &ArchiveFileRef{ID: f.ID, FileName: f.FileName, Version: f.Version}
}

// downloadDuplicateSuffix is what a browser appends to a file name it has
// already saved once: " (1)" / "(1)" (Chrome, Firefox) or "-1" / "_1".
var downloadDuplicateSuffix = regexp.MustCompile(`(?: ?\(\d+\)|[-_]\d+)$`)

// matchArchiveName finds the listed file an archive's name names (#530).
// In order, the first rule that names exactly one file wins:
//
//  1. the name exactly;
//  2. the name, case-insensitively;
//  3. the name without a download-duplicate suffix before its extension,
//     case-insensitively (normalized reports this rule).
//
// Because the raw name is tried first, normalisation can never move a match
// from one listed file to another: "Mod-1.2-1.zip" stays itself when it is
// listed, and is read as "Mod-1.2.zip" only when it is not. Several files
// sharing a name match nothing - a guessed file ID is worse than none.
func matchArchiveName(files []domain.DownloadableFile, name string) (file *domain.DownloadableFile, normalized bool) {
	unique := func(eq func(string) bool) *domain.DownloadableFile {
		var found *domain.DownloadableFile
		for i := range files {
			if files[i].FileName == "" || !eq(files[i].FileName) {
				continue
			}
			if found != nil && found.ID != files[i].ID {
				return nil
			}
			found = &files[i]
		}
		return found
	}
	if f := unique(func(n string) bool { return n == name }); f != nil {
		return f, false
	}
	if f := unique(func(n string) bool { return strings.EqualFold(n, name) }); f != nil {
		return f, false
	}
	candidates := undupedNames(name)
	if len(candidates) == 0 {
		return nil, false
	}
	f := unique(func(n string) bool {
		return slices.ContainsFunc(candidates, func(c string) bool { return strings.EqualFold(n, c) })
	})
	return f, f != nil
}

// undupedNames is name with its download-duplicate suffix removed, once per
// way of reading its extension (".zip", and ".tar.gz" for a name ending in
// it); empty when no reading has such a suffix.
func undupedNames(name string) []string {
	ext := filepath.Ext(name)
	exts := []string{ext}
	if inner := filepath.Ext(strings.TrimSuffix(name, ext)); strings.EqualFold(inner, ".tar") {
		exts = append(exts, inner+ext)
	}
	var out []string
	for _, e := range exts {
		stem := strings.TrimSuffix(name, e)
		if loc := downloadDuplicateSuffix.FindStringIndex(stem); loc != nil && loc[0] > 0 {
			out = append(out, stem[:loc[0]]+e)
		}
	}
	return out
}

// updateHookNames lists the hooks an update runs, in run order, skipping
// unconfigured ones: uninstall.before_each and install.before_each, the
// Replace, then uninstall.after_each and install.after_each.
func updateHookNames(hooks *ResolvedHooks, skipHooks bool) []string {
	names := []string{}
	if skipHooks || hooks == nil {
		return names
	}
	for _, h := range []struct{ name, cmd string }{
		{"uninstall.before_each", hooks.GetUninstallBeforeEach()},
		{"install.before_each", hooks.GetInstallBeforeEach()},
		{"uninstall.after_each", hooks.GetUninstallAfterEach()},
		{"install.after_each", hooks.GetInstallAfterEach()},
	} {
		if h.cmd != "" {
			names = append(names, h.name)
		}
	}
	return names
}

// ApplyUpdateFromArchive performs plan: it ingests the archive into the cache
// under the installed mod's identity at plan.ToVersion, stamps the matched
// file's completion marker, and hands over to ApplyUpdate's own tail
// (commitUpdate) - hooks, Replace, the DB swap that records
// previous_version/previous_file_ids, the archive's checksum (#514), the
// profile ref, the merged-pak sync. sink may be nil.
//
// Refused before anything is written: a stale plan (the installed set or
// the archive changed - ErrStalePlan), a Workshop item, a locked mod
// (ErrModLocked, #325), and a mismatched archive opts.AcceptMismatch has not
// answered (*ArchiveMismatchError). A failure after the ingest removes the
// cache entry this call created - unless the row already records it, which
// only a failed compensation can leave behind.
func (s *Service) ApplyUpdateFromArchive(ctx context.Context, game *domain.Game, plan *UpdateFromArchivePlan, opts UpdateFromArchiveOptions, sink EventSink) (*UpdateApplyResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return &UpdateApplyResult{}, err
	}
	defer release()
	return s.applyUpdateFromArchive(ctx, game, plan, opts, sink)
}

func (s *Service) applyUpdateFromArchive(ctx context.Context, game *domain.Game, plan *UpdateFromArchivePlan, opts UpdateFromArchiveOptions, sink EventSink) (*UpdateApplyResult, error) {
	result := &UpdateApplyResult{}
	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}
	mod := plan.Mod
	profileName := mod.ProfileName

	if err := s.requireActiveProfile(ctx, game.ID, profileName, VerbUpdate); err != nil {
		return result, err
	}
	if err := s.checkPlanFresh(ctx, mod.GameID, profileName, plan.snapshot); err != nil {
		return result, err
	}
	current, err := fingerprintArchive(plan.Archive)
	if err != nil {
		return result, err
	}
	if current != plan.fingerprint {
		return result, fmt.Errorf("%w: %s changed since the plan was computed", ErrStalePlan, plan.Archive)
	}
	if err := refuseExternal("update", &mod, ReasonExternalNoUpdate); err != nil {
		return result, err
	}
	if err := s.refuseLockedUpdate(ctx, game, profileName, &mod); err != nil {
		return result, err
	}
	if plan.Match == ArchiveMatchMismatch && !opts.AcceptMismatch {
		return result, &ArchiveMismatchError{ArchiveName: plan.ArchiveName, Advertised: *plan.Advertised, Matched: plan.MatchedFile}
	}

	autoName, autoWarn := s.autoSnapshot(ctx, game, profileName, OpUpdate)
	result.Warnings = prependWarning(result.Warnings, autoWarn)
	result.Notes = prependSnapshotNote(result.Notes, autoName)

	hooks, err := s.resolvedHooks(ctx, game, profileName)
	if err != nil {
		return result, err
	}
	runner, err := s.hookRunner(ctx)
	if err != nil {
		return result, err
	}

	scope := Scope{Op: OpUpdate, ModName: mod.Name, Mod: &domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID}}
	warn := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		result.Warnings = append(result.Warnings, msg)
		emit(WarningEvent{Scope: scope, Phase: UpdateWarning, Message: msg})
	}

	ident := importIdentity{sourceID: mod.SourceID, modID: mod.ID, version: plan.ToVersion}
	importOpts := ImportOptions{SourceID: mod.SourceID, ModID: mod.ID, ProfileName: profileName}
	imported, err := s.newImporter(game).importWithIdentity(ctx, plan.Archive, game, importOpts, ident)
	if err != nil {
		return result, fmt.Errorf("import failed: %w", err)
	}

	newMod := mod.Mod
	newMod.Version = plan.ToVersion
	fileIDs := slices.Clone(plan.FileIDs)
	// #197 C1: a DeployCompile merge source is retained under its own
	// filename, and a row that does not record it is invisible to every
	// future merge - the archive import's own rule.
	if imported.RetainedFileID != "" && !slices.Contains(fileIDs, imported.RetainedFileID) {
		fileIDs = append(fileIDs, imported.RetainedFileID)
	}
	if plan.matchedFileID != "" {
		if err := s.markImportedFileComplete(ctx, game, &newMod, plan.matchedFileID); err != nil {
			warn("could not mark cache entry complete: %v", err)
		}
	}
	checksums := archiveChecksums(plan.Archive, fileIDs, warn)

	err = s.commitUpdate(ctx, game, profileName, updateCommit{
		mod:       mod,
		newMod:    &newMod,
		fileIDs:   fileIDs,
		checksums: checksums,
		scope:     scope,
		hooks:     hooks,
		runner:    runner,
		hookCtx:   hookContextFor(game),
	}, UpdateOptions{Force: opts.Force, SkipHooks: opts.SkipHooks}, result, emit)
	if err != nil {
		s.discardUpdateArchiveEntry(game, plan, warn)
	}
	return result, err
}

// discardUpdateArchiveEntry removes the cache entry a failed
// ApplyUpdateFromArchive created, so a refusal or failure leaves the cache as
// it found it. It leaves an entry that pre-existed the plan, and one the row
// records after all (a compensation that itself failed): deleting that would
// strand the deployment it backs.
func (s *Service) discardUpdateArchiveEntry(game *domain.Game, plan *UpdateFromArchivePlan, warn func(string, ...any)) {
	if plan.entryPreExists {
		return
	}
	ctx := context.Background()
	mod := plan.Mod
	if row, err := s.GetInstalledMod(ctx, mod.SourceID, mod.ID, game.ID, mod.ProfileName); err != nil || row.Version == plan.ToVersion {
		return
	}
	if err := s.GetGameCache(game).Delete(game.ID, mod.SourceID, mod.ID, plan.ToVersion); err != nil && !errors.Is(err, context.Canceled) {
		s.logger().Warn("removing failed update's cache entry", "mod", mod.ID, "version", plan.ToVersion, "err", err)
		warn("could not remove the failed update's cache entry %s@%s: %v", mod.ID, plan.ToVersion, err)
	}
}

// refuseLockedUpdate is the lock gate both update applies share (#97, #325):
// a locked ref refuses an update entirely. A profile that cannot be read
// cannot hold a lock and falls through - unless the read was cancelled
// (Ruling 16 (C)), which is a profile never asked.
func (s *Service) refuseLockedUpdate(ctx context.Context, game *domain.Game, profileName string, mod *domain.InstalledMod) error {
	prof, err := s.NewProfileManager().Get(ctx, game.ID, profileName)
	if err != nil {
		return ctx.Err()
	}
	if ref := prof.FindRef(mod.SourceID, mod.ID); ref != nil && ref.Locked {
		return LockedRefUnlockOnlyRefusalError(mod.Mod, profileName, ref)
	}
	return nil
}
