// Package core: this file splits a download's ingest into its two halves
// (#602, design docs/plans/2026-11-10-collections-design.md §1): ACQUIRE -
// fetch (or reuse a retained archive), verify, extract and lay out into a
// pristine tree beside the cache entry - and COMMIT, which merges that tree
// into the entry and stamps its completion marker.
//
// Acquire writes nothing anything else reads: not the cache entry, not the
// DB, not a profile. Its one durable side effect is the archive a refusal
// keeps for the retry (retained_download.go), which exists precisely so a
// refused acquire costs no second download. That is what lets an
// install-shaped Apply acquire EVERY item before it commits any of them
// (acquire_engine.go), and refuse with nothing written when one of them
// turns out to need an answer.
//
// downloadMod is the two halves back to back, for the flows that fetch one
// file and use it at once.
package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// acquiredFile is one file's acquire-phase outcome: its bytes verified and
// its cache members built in tree, a pristine directory beside the cache
// entry, ready for commitAcquired to merge in. Until then the entry itself is
// untouched.
//
// The holder must call release exactly once, whether or not it commits:
// release removes the scratch directories (the download's own staging dir,
// tree) and never the retained archive.
type acquiredFile struct {
	gameCache *cache.Cache
	game      *domain.Game
	// mod is a copy: the cache entry it names is fixed at acquire time, so
	// a caller that later restamps its own mod cannot move the commit.
	mod      domain.Mod
	file     *domain.DownloadableFile
	sourceID string

	// tree holds exactly what this file contributes to the entry; members
	// lists its regular files (the marker's manifest).
	tree    string
	members []string
	// unseeded is a directory ingest's #166 rule: the commit replaces the
	// entry instead of layering onto it.
	unseeded bool
	// listed: DownloadModResult.FilesExtracted counts the committed entry's
	// files (cache.ListFiles) rather than this file's members.
	listed bool

	checksum string
	// archivePath and download are set for a network download only: the
	// archive is what a blocked Apply retains for its re-run.
	archivePath string
	download    *DownloadResult

	owned []string
	log   *slog.Logger

	// fomod is the FOMOD resolve step's outcome when this file was an
	// installer (#614): mod.Variant is then its variant. Nil for any other
	// file.
	fomod *fomodResolved
}

// release removes the acquisition's scratch directories. Safe on nil and
// safe to call twice.
//
// A directory it cannot remove is LOGGED, not returned: release runs after
// the commit (or the refusal) has already decided the outcome, and a
// committed install must not be reported as failed because scratch space
// outlived it - nor a refusal replaced by a cleanup error (#602 review N4).
func (a *acquiredFile) release() {
	if a == nil {
		return
	}
	for _, dir := range a.owned {
		if err := os.RemoveAll(dir); err != nil && a.log != nil {
			a.log.Warn("removing an acquire directory failed", "path", dir, "err", err)
		}
	}
	a.owned = nil
}

// releaseOnPanic is deferred by whatever holds acquire scratch directories
// that nothing else owns yet: if the function panics - an extractor, an
// adapter's layout - release runs and the panic carries on (#628). A
// deferred cleanup that keys on the returned error never sees a panic, since
// err is still nil while one unwinds; `lmm serve` recovers a job's panic and
// keeps running, so what the panic skipped would stay on disk.
//
// recover only stops a panic when the deferred function itself calls it, so
// this must be deferred directly (`defer releaseOnPanic(f)`), never from
// inside another deferred closure.
func releaseOnPanic(release func()) {
	if p := recover(); p != nil {
		release()
		panic(p)
	}
}

// acquireTreePath names the pristine tree for one file of one cache entry.
// Beside the entry - so the commit's merge is a rename on one filesystem,
// whatever the game's cache_path - and per FILE, so a mod's several files
// can all be acquired before any of them commits.
func acquireTreePath(cachePath, fileID string) string {
	sum := sha256.Sum256([]byte(fileID))
	return cachePath + ".acquire-" + hex.EncodeToString(sum[:6])
}

// acquireFile is acquire for one file: it ends with the file's members in
// a pristine tree and nothing committed. A failure is a *DownloadError (or
// a typed refusal kept bare, see asDownloadError), and a refusal the user
// answers without re-fetching has kept its archive (retained_download.go).
//
// fx is what a FOMOD installer in the file is installed with (#614,
// fomod_resolve.go) - the MOD's input, of which the file's own is taken
// here (fomodInput.forFile, #634); the zero value records no choices.
func (s *Service) acquireFile(ctx context.Context, gameCache *cache.Cache, sourceID string, game *domain.Game, mod *domain.Mod, file *domain.DownloadableFile, fx fomodInput, sink EventSink) (*acquiredFile, error) {
	a, err := s.acquireFileRaw(ctx, gameCache, sourceID, game, mod, file, fx.forFile(file.ID), sink)
	if err != nil {
		if game != nil {
			s.noteDownloadOutcome(ctx, game.ID, sourceID, mod, err) // #543
		}
		return nil, asDownloadError(err, sourceID, mod, file)
	}
	return a, nil
}

// commitAcquired is commit for one acquired file: seed the entry's staging
// directory, merge the tree in, and publish both with the file's marker in
// one swap. The caller still releases a.
func (s *Service) commitAcquired(ctx context.Context, a *acquiredFile) (*DownloadModResult, error) {
	result, err := s.commitAcquiredRaw(ctx, a)
	if err != nil {
		return nil, asDownloadError(err, a.sourceID, &a.mod, a.file)
	}
	if a.game != nil {
		s.noteDownloadOutcome(ctx, a.game.ID, a.sourceID, &a.mod, nil) // #543
	}
	return result, nil
}

// newAcquired starts an acquisition for (mod, file) and creates nothing.
func (s *Service) newAcquired(gameCache *cache.Cache, sourceID string, game *domain.Game, mod *domain.Mod, file *domain.DownloadableFile) *acquiredFile {
	a := &acquiredFile{gameCache: gameCache, game: game, mod: *mod, file: file, sourceID: sourceID, log: s.logger()}
	a.tree = acquireTreePath(gameCache.ModPath(cache.Key{GameID: game.ID, SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, Variant: mod.Variant}), file.ID)
	return a
}

// claimTree clears a stale tree a crashed run left and takes ownership of
// the path.
func (a *acquiredFile) claimTree() error {
	if err := os.RemoveAll(a.tree); err != nil {
		return fmt.Errorf("clearing acquire directory: %w", err)
	}
	// A FOMOD resolve that crashed mid-build leaves its half-built tree
	// beside the acquire tree (executeFomodOps): clear that too.
	if err := os.RemoveAll(a.tree + ".fomod"); err != nil {
		return fmt.Errorf("clearing acquire directory: %w", err)
	}
	a.owned = append(a.owned, a.tree)
	return nil
}

func (s *Service) acquireFileRaw(ctx context.Context, gameCache *cache.Cache, sourceID string, game *domain.Game, mod *domain.Mod, file *domain.DownloadableFile, fx fomodInput, sink EventSink) (a *acquiredFile, err error) {
	// Note: We intentionally do NOT check if cache exists here.
	// A mod can have multiple downloadable files (e.g., main mod + optional patches),
	// and each file should be downloaded and extracted to the cache.
	// The cache directory may already exist from a previous file download.

	// Get the source so we can gate the local-ingest branch below to sources
	// that are actually allowed to serve local files.
	src, err := s.sourceByID(sourceID) // #623: lmm-collection included
	if err != nil {
		return nil, fmt.Errorf("getting source: %w", err)
	}

	url, err := src.GetDownloadURL(ctx, mod, file.ID)
	if err != nil {
		// A source that cannot serve a URL at all may still be able to
		// FETCH the file itself (#269 Tier 3: a steamcmd shell-out is a
		// subprocess, not a URL). The fallback is gated on the source
		// saying exactly that - ErrNotSupported - so a real failure from a
		// source that also implements Fetcher (a 500, a bad key) still
		// fails here instead of quietly taking a different route.
		fetcher, isFetcher := src.(source.Fetcher)
		if !isFetcher || !errors.Is(err, source.ErrNotSupported) {
			return nil, fmt.Errorf("getting download URL: %w", err)
		}
		a, err := s.acquireFetched(ctx, gameCache, fetcher, sourceID, game, mod, file, fx, sink)
		if a != nil {
			a.sourceID = sourceID
		}
		return a, err
	}

	if localPath, ok := strings.CutPrefix(url, "file://"); ok {
		// Only a source.LocalFileServer is allowed to serve local files. A
		// remote source (NexusMods, CurseForge, a compromised custom
		// API/manifest source, ...) returning file:// must never be trusted
		// to read arbitrary paths off disk into the cache.
		lfs, servesLocal := src.(source.LocalFileServer)
		if !servesLocal || !lfs.ServesLocalFiles() {
			return nil, fmt.Errorf("source %q returned a local file:// URL but is not a directory source", sourceID)
		}
		a, err := s.acquireLocal(ctx, gameCache, game, mod, file, localPath, fx, sink)
		if a != nil {
			a.sourceID = sourceID
		}
		return a, err
	}

	// Stage the download under the data dir, not $TMPDIR — see newStagingDir.
	tempDir, err := newStagingDir(s.stagingRoot(), "lmm-download-*")
	if err != nil {
		return nil, err
	}
	a = s.newAcquired(gameCache, sourceID, game, mod, file)
	a.owned = append(a.owned, tempDir)
	// pending, not a: a `return nil, err` has already set the named result
	// to nil by the time this runs.
	defer func(pending *acquiredFile) {
		if err != nil {
			pending.release()
		}
	}(a)
	defer releaseOnPanic(a.release)

	// Download the file. safeFileName sanitizes file.FileName - a
	// SOURCE-CONTROLLED value (NexusMods/CurseForge/Icarus/a custom source's
	// own declared filename) - before it is ever used as a path component:
	// an entry like "../../evil" would otherwise let a malicious or buggy
	// source escape tempDir/the acquire tree (#196 review). Used for every
	// path-construction use of the filename below; file.FileName itself is
	// left untouched for display purposes (the SHA256 mismatch message).
	safeFileName := filepath.Base(file.FileName)
	archivePath := filepath.Join(tempDir, safeFileName)

	// An earlier ingest of this exact file may have been refused for a
	// reason the user answers without re-fetching (#359's loader
	// precondition, or #602's blocked Apply), in which case the bytes were
	// kept and there is nothing to fetch (retained_download.go). Everything
	// below runs unchanged either way - but note WHAT the checks below then
	// check: a reuse rebuilds DownloadResult from the retention's sidecar,
	// so the SHA256 and exact-size comparisons are against the values the
	// original download RECORDED, not a re-hash of the retained file. That
	// is deliberate rather than an oversight: the sidecar is written last
	// (storeRetainedDownload), so a retention interrupted mid-move has no
	// sidecar at all and reuseRetainedDownload rejects it outright instead
	// of trusting a truncated archive. The retained copy is dropped by the
	// COMMIT (commitAcquiredRaw), whichever kind of member set it built.
	retainedPath, downloadResult, reused := s.reuseRetainedDownload(sourceID, mod.ID, file.ID, identityOf(mod, file))
	if !reused {
		var headers map[string]string
		if hp, ok := src.(source.DownloadHeaderProvider); ok {
			headers = hp.DownloadHeaders(url)
		}
		downloadResult, err = s.downloader.DownloadWithHeaders(ctx, url, archivePath, headers, sink)
		if err != nil {
			return nil, fmt.Errorf("downloading mod: %w", err)
		}
	} else {
		archivePath = retainedPath
	}

	if file.SHA256 != "" && !strings.EqualFold(downloadResult.SHA256, file.SHA256) {
		return nil, fmt.Errorf("verifying download of %s: sha256 mismatch: source declares %s, downloaded file is %s",
			file.FileName, file.SHA256, downloadResult.SHA256)
	}

	// A source.ExactFileSizer declares its sizes to the byte, so a body
	// that does not match one is a truncated or wrong file - the only
	// integrity check available on a CDN that publishes no checksum (#269
	// Tier 3's legacy file_url items). The partial is removed here rather
	// than left for the deferred staging teardown, so the failure itself
	// leaves nothing behind.
	if es, ok := src.(source.ExactFileSizer); ok && es.ExactFileSizes() && file.Size > 0 && downloadResult.Size != file.Size {
		_ = os.Remove(downloadResult.Path)
		return nil, fmt.Errorf("verifying download of %s: size mismatch: source declares %d bytes, downloaded file is %d",
			file.FileName, file.Size, downloadResult.Size)
	}

	if err := a.claimTree(); err != nil {
		return nil, err
	}
	a.archivePath, a.download, a.checksum = archivePath, downloadResult, downloadResult.Checksum

	// convertEligiblePak requires BOTH the game's own eligibility (deploy
	// mode + ConvertPaks) AND a compile-capable ADAPTER (#221 I1 fix, as
	// U2/#412 rephrases it): the game flags alone don't decide, so a raw
	// pak for a game whose adapter cannot compile must fall through to the
	// legacy extract/copy path below - exactly as it did before #221 -
	// rather than hard-erroring the whole download. Unlike a .exmodz file,
	// which has no other valid interpretation and so still hard-errors
	// (see the !ok check below).
	//
	// The question used to be asked of the SOURCE, which is what made an
	// Icarus .pak from NexusMods un-compilable while the identical file
	// from Project Daedalus compiled. One game, one adapter, one answer.
	mc, isMergeCompiler := s.optionalCompiler(game)
	convertEligiblePak := isMergeCompiler && isConvertEligibleArtifact(game, mc, safeFileName)
	if game.DeployMode == domain.DeployCompile && (s.isNativeMergeFile(game, mc, safeFileName) || convertEligiblePak) {
		if !isMergeCompiler {
			return nil, fmt.Errorf("game %q requires DeployCompile but its adapter %q cannot compile (file %q from source %q)", game.ID, s.AdapterName(game), safeFileName, src.ID())
		}
		if err := mc.ValidateSource(archivePath); err != nil {
			return nil, fmt.Errorf("validating %s: %w", safeFileName, err)
		}
		if err := os.MkdirAll(a.tree, 0755); err != nil {
			return nil, fmt.Errorf("preparing staging: %w", err)
		}
		if err := copyFileStreaming(archivePath, filepath.Join(a.tree, cache.RetainedSourceName(file.ID))); err != nil {
			return nil, fmt.Errorf("retaining %s: %w", safeFileName, err)
		}
		// exmodz: members nil (#197) - the merged pak is the only artifact.
		// pak (#221): ALSO keep a deployable copy as the sole member, so the
		// default state is raw-deploy (today's behavior); the first
		// successful merge flips the manifest to nil (syncMergedPak's
		// reconcile) and the merged pak takes over.
		if convertEligiblePak {
			if err := copyFileStreaming(archivePath, filepath.Join(a.tree, safeFileName)); err != nil {
				return nil, fmt.Errorf("staging deployable pak %s: %w", safeFileName, err)
			}
			a.members = []string{safeFileName}
		}
		return a, nil
	}

	if game.DeployMode == domain.DeployCopy || !s.extractor.CanExtract(archivePath) {
		// Copy mode: game wants files as-is (e.g., Hytale .zip mods)
		// Or not an archive - just copy to cache. copyFileStreaming mkdirs
		// its destination itself (importer.go).
		if err := copyFileStreaming(archivePath, filepath.Join(a.tree, safeFileName)); err != nil {
			return nil, fmt.Errorf("copying to cache: %w", err)
		}
		a.members = []string{safeFileName}
		return a, nil
	}

	members, resolved, err := s.extractPristine(ctx, game, mod, archivePath, a.tree, fx, sink)
	if err != nil {
		s.retainRefusedDownload(err, sourceID, mod.ID, file.ID, archivePath, downloadResult, identityOf(mod, file))
		return nil, fmt.Errorf("extracting mod: %w", err)
	}
	a.members, a.listed = members, true
	a.setFomod(resolved)
	return a, nil
}

// acquireLocal is acquire for a local mod (directory or archive), through
// the same tree and commit as a download. Local ingests have no HTTP
// download checksum, so the checksum is computed from the SOURCE instead
// (#164): the MD5 of the local file for file/archive ingests (the same
// fingerprint the download path records for a fetched archive), or a
// deterministic digest over the member set for directory ingests
// (digestDirectoryMembers). Both are pure functions of the source content,
// so a later re-ingest of an unchanged source reproduces the stored value
// and install/verify --fix converge instead of looping on NO CHECKSUM. A
// directory with no regular files yields an empty checksum - nothing to
// fingerprint - and callers must report that honestly.
//
// sink receives the archive's download-time warnings (#425) - the same
// DownloadWarning events a fetched archive raises - and may be nil.
func (s *Service) acquireLocal(ctx context.Context, gameCache *cache.Cache, game *domain.Game, mod *domain.Mod, file *domain.DownloadableFile, localPath string, fx fomodInput, sink EventSink) (a *acquiredFile, err error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return nil, fmt.Errorf("local mod path: %w", err)
	}
	a = s.newAcquired(gameCache, mod.SourceID, game, mod, file)
	if err := a.claimTree(); err != nil {
		return nil, err
	}
	// pending, not a: a `return nil, err` has already set the named result
	// to nil by the time this runs.
	defer func(pending *acquiredFile) {
		if err != nil {
			pending.release()
		}
	}(a)
	defer releaseOnPanic(a.release)
	a.listed = true

	switch {
	case info.IsDir():
		// #166: a directory ingest REPLACES the cache entry instead of
		// overlaying it, so it commits UNSEEDED - seeding from the existing
		// entry would let members deleted from the source survive every
		// re-ingest (verify --fix, a re-download into a retained entry) and
		// stay deployed indefinitely. Safe because directory sources
		// declare exactly ONE synthetic file ID ("main" - see
		// custom.Directory.GetModFiles), so the seed can never carry sibling
		// files' members or markers worth preserving. File/archive ingests
		// keep the seed: their sources may serve multiple file IDs into one
		// entry.
		a.unseeded = true
		if err := copyDir(ctx, localPath, a.tree); err != nil {
			return nil, fmt.Errorf("copying mod directory: %w", err)
		}
		if err := os.MkdirAll(a.tree, 0755); err != nil {
			return nil, fmt.Errorf("copying mod directory: %w", err)
		}
		// Attribute the COPIES, not localPath's own listing: the tree holds
		// exactly what the commit publishes, and copyDir DEREFERENCES
		// in-root symlinks into regular files that a walk of the source
		// would skip - those files are cached, listed, and deployed
		// (cache.ListFiles), so the manifest and digest must cover them too
		// or the member-set views drift apart (#166).
		if a.members, err = relativeFileMembers(a.tree); err != nil {
			return nil, fmt.Errorf("listing staged mod directory: %w", err)
		}
		// Digest the COPIES, not localPath: the live source directory can
		// change mid-ingest - hashing it here could persist a checksum for
		// content that was never cached (review finding on #164).
		if a.checksum, err = digestDirectoryMembers(a.tree, a.members); err != nil {
			return nil, fmt.Errorf("fingerprinting mod directory: %w", err)
		}
	case game.DeployMode == domain.DeployCopy || !s.extractor.CanExtract(localPath):
		// file.FileName is the declared name for this mod file - use it so
		// the cached file matches what the source/caller advertised (#52
		// item 12); localPath's own basename is often just a temp file name
		// and falls back only when the caller left FileName unset.
		// filepath.Base sanitizes whichever name was chosen: file.FileName
		// is SOURCE-CONTROLLED (#196 review) and must not be trusted as a
		// path component verbatim (e.g. "../../evil").
		destName := file.FileName
		if destName == "" {
			destName = filepath.Base(localPath)
		}
		destName = filepath.Base(destName)
		if err := copyFileStreaming(localPath, filepath.Join(a.tree, destName)); err != nil {
			return nil, fmt.Errorf("copying to cache: %w", err)
		}
		a.members = []string{destName}
		if a.checksum, err = md5File(localPath); err != nil {
			return nil, fmt.Errorf("hashing local mod file: %w", err)
		}
	default:
		var resolved *fomodResolved
		if a.members, resolved, err = s.extractPristine(ctx, game, mod, localPath, a.tree, fx, sink); err != nil {
			return nil, fmt.Errorf("extracting mod: %w", err)
		}
		a.setFomod(resolved)
		if a.checksum, err = md5File(localPath); err != nil {
			return nil, fmt.Errorf("hashing local mod archive: %w", err)
		}
	}
	return a, nil
}

func (s *Service) commitAcquiredRaw(ctx context.Context, a *acquiredFile) (*DownloadModResult, error) {
	var cachePath, stagePath string
	var err error
	if a.unseeded {
		cachePath, stagePath, err = prepareUnseededStaging(a.gameCache, a.game, &a.mod)
	} else {
		cachePath, stagePath, err = prepareStaging(ctx, a.gameCache, a.game, &a.mod)
	}
	if err != nil {
		return nil, err
	}
	defer func() {
		if rmErr := os.RemoveAll(stagePath); rmErr != nil {
			s.logger().Debug("removing staging directory failed", "path", stagePath, "err", rmErr)
		}
	}()

	if err := mergeAcquiredTree(a.tree, stagePath); err != nil {
		return nil, fmt.Errorf("staging acquired members: %w", err)
	}
	if err := commitStagedCacheWithMarker(modCacheKey(a.game.ID, a.mod), cachePath, stagePath, a.file.ID, a.members); err != nil {
		return nil, err
	}
	// The retained copy is dead weight once the bytes are in the cache -
	// whichever member set (extracted, stored, compiled) they became.
	if a.download != nil {
		s.dropRetainedDownload(a.sourceID, a.mod.ID, a.file.ID)
	}

	result := &DownloadModResult{FilesExtracted: len(a.members), Checksum: a.checksum}
	if a.listed {
		files, err := a.gameCache.ListFiles(cache.Key{GameID: a.game.ID, SourceID: a.mod.SourceID, ModID: a.mod.ID, Version: a.mod.Version, Variant: a.mod.Variant})
		if err != nil {
			return nil, err
		}
		result.FilesExtracted = len(files)
	}
	return result, nil
}

// mergeAcquiredTree moves tree's contents into stagePath (new members
// overwrite same-named seeded ones), keeping its directories, empty ones
// included. Both sit beside the same cache entry, so every move is a
// rename.
func mergeAcquiredTree(tree, stagePath string) error {
	return filepath.WalkDir(tree, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(tree, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(stagePath, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0755)
		}
		return os.Rename(path, dest)
	})
}

// extractPristine extracts archivePath into tree, a PRISTINE directory,
// lets the game's adapter lay it out, and returns the regular files it
// holds - the exact member set this archive contributes.
//
// The pristine directory exists for attribution (#144 item 4): the entry's
// staging directory is seeded with the existing cache entry at commit, so
// extracting into it directly could not tell this archive's members apart
// from earlier files' - and a before/after diff would silently misattribute
// a member the archive OVERWRITES (one also shipped by an earlier file),
// breaking the shared-member-survives rule. The extractor's own
// untrusted-archive guards (zip-slip, reserved-namespace rejection) run
// against the pristine dir, where "preexisting reserved entries" is
// correctly empty.
//
// Returned members are tree-relative paths of regular files only, matching
// cache.ListFiles semantics (directories and symlinks are never listed,
// deployed, or undeployed).
//
// Between the extract and the adapter, the FOMOD resolve step (#614,
// fomod_resolve.go) turns an installer archive into the tree its choices
// install, so the adapter lays out what the mod IS rather than the
// installer's raw contents. Its outcome is returned for the caller to
// record; nil for any other archive.
func (s *Service) extractPristine(ctx context.Context, game *domain.Game, mod *domain.Mod, archivePath, tree string, fx fomodInput, sink EventSink) ([]string, *fomodResolved, error) {
	if err := s.extractor.Extract(ctx, archivePath, tree); err != nil {
		return nil, nil, err
	}
	if s.afterExtract != nil {
		s.afterExtract(filepath.Base(archivePath))
	}
	resolved, err := s.resolveFomod(mod, filepath.Base(archivePath), tree, fx)
	if err != nil {
		return nil, nil, err
	}
	if resolved != nil {
		for _, w := range resolved.warnings {
			s.logger().Warn(w, "mod", mod.Name, "game", game.ID)
			if sink != nil {
				sink(WarningEvent{
					Scope:   Scope{Op: OpInstall, ModName: mod.Name, Mod: &domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID}},
					Phase:   DownloadWarning,
					Message: w,
				})
			}
		}
	}

	// #353: the game's adapter gets its say on the archive's layout HERE,
	// against the PRISTINE tree, the only point at which "what did this
	// archive contribute" is still an answerable question. A mod downloaded
	// from NexusMods and one imported from a local archive therefore reach
	// the cache in the same layout, which is what lets a BepInEx plugin
	// deploy correctly with no source work at all (spike §5).
	//
	// The refusal it makes first (#359/#413) is one a downloaded archive
	// cannot answer any earlier: its shape is not knowable until it is
	// extracted, which is why PlanInstall cannot ask. It lands in ACQUIRE,
	// before anything is committed, so nothing is deployed and nothing is
	// recorded - and an install-shaped Apply collects it with every other
	// item's before writing anything at all (#602).
	layout, err := s.layoutStagedExtract(game, mod.Name, tree)
	if err != nil {
		return nil, nil, err
	}
	// A download has no plan to carry the adapter's warnings: its shape is
	// not knowable until it is extracted, which is this function. So they
	// ride the flow's own event sink as ordinary WarningEvents - the wire
	// type every warning in every flow already uses - under the one phase
	// every flow forwards (DownloadWarning, #425), and the log keeps the
	// record for a caller that passed no sink.
	for _, w := range layout.Warnings {
		s.logger().Warn(w, "mod", mod.Name, "game", game.ID)
		if sink != nil {
			sink(WarningEvent{
				Scope:   Scope{Op: OpInstall, ModName: mod.Name, Mod: &domain.ModReference{SourceID: mod.SourceID, ModID: mod.ID}},
				Phase:   DownloadWarning,
				Message: w,
			})
		}
	}

	var members []string
	err = filepath.WalkDir(tree, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(tree, path)
			if err != nil {
				return err
			}
			members = append(members, rel)
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("listing extracted members: %w", err)
	}
	return members, resolved, nil
}

// setFomod records the resolve step's outcome on the acquisition: the cache
// entry it commits to is the resolved tree's variant.
func (a *acquiredFile) setFomod(r *fomodResolved) {
	if r == nil {
		return
	}
	a.fomod = r
	a.mod.Variant = r.variant
}
