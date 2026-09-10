package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// verifyMemoEntry is one memoised verify answer: the result, plus the
// fingerprint of the state it was computed from.
type verifyMemoEntry struct {
	fingerprint string
	result      *VerifyResult
}

// verifyMemoKey identifies an answer: the same question asked of a
// different game, profile or tier is a different question. The tier is in
// the key because VerifyFull asks the SOURCE things VerifyLocal never
// does, so the cheap answer can never stand in for the expensive one.
func verifyMemoKey(gameID, profile string, tier VerifyTier) string {
	return gameID + "\x00" + profile + "\x00" + tier.String()
}

// verifyMemoLookup returns the memoised result for this key when the
// fingerprint still matches, or nil.
func (s *Service) verifyMemoLookup(key, fingerprint string) *VerifyResult {
	s.verifyMemoMu.Lock()
	defer s.verifyMemoMu.Unlock()
	entry, ok := s.verifyMemo[key]
	if !ok || entry.fingerprint != fingerprint {
		return nil
	}
	return entry.result
}

// verifyMemoStore records result under key for this fingerprint.
func (s *Service) verifyMemoStore(key, fingerprint string, result *VerifyResult) {
	s.verifyMemoMu.Lock()
	defer s.verifyMemoMu.Unlock()
	if s.verifyMemo == nil {
		s.verifyMemo = map[string]verifyMemoEntry{}
	}
	s.verifyMemo[key] = verifyMemoEntry{fingerprint: fingerprint, result: result}
}

// dropVerifyMemo forgets every memoised verify answer. beginOp calls it, so
// EVERY mutation invalidates them - by the gate rather than by a per-flow
// guess about what a given flow could have touched, which is the only
// version of this that cannot be got wrong by a flow added later.
func (s *Service) dropVerifyMemo() {
	s.verifyMemoMu.Lock()
	defer s.verifyMemoMu.Unlock()
	clear(s.verifyMemo)
}

// verifyFingerprint is a cheap summary of everything a verify run reads
// LOCALLY: the profile's mod refs (and their locks), the installed rows
// (version, file ids, policy, deployed/enabled state), a stat of each
// EXTERNAL row's ExternalPath (#269), the deployed_files checksum rows the
// run walks, and a stat-only walk of the game's deployed tree - each
// entry's path, size and modification time.
//
// KNOWN LIMIT, and the reason VerifyOptions.Force exists: size+mtime is not
// content. A file rewritten with the same length and its timestamp restored
// (a restore from backup, a build tool that preserves mtimes, a
// deliberately crafted swap) fingerprints identically, and the memo will
// keep answering with the previous verdict until something else moves or a
// caller passes Force. `lmm verify` always forces for exactly this reason:
// a user who types the command is asking for a real look at the disk.
//
// It also does NOT cover state the FULL tier reads over the network - a
// source that changes what it reports for a recorded file id is invisible
// here - nor the cache tree that PakNeedsReingest stats. Both are bounded
// the same way: the next mutation drops the memo, and Force bypasses it.
//
// An unreadable directory or a failed DB read returns an error rather than
// a partial fingerprint, and the caller then simply runs the verify: a
// fingerprint that cannot be computed must never be mistaken for one that
// matches.
func (s *Service) verifyFingerprint(ctx context.Context, game *domain.Game, profile string) (string, error) {
	h := sha256.New()

	installed, err := s.GetInstalledMods(ctx, game.ID, profile)
	if err != nil {
		return "", fmt.Errorf("fingerprinting installed mods: %w", err)
	}
	rows := make([]string, 0, len(installed))
	for i := range installed {
		m := &installed[i]
		rows = append(rows, fmt.Sprintf("mod\x1f%s\x1f%s\x1f%s\x1f%t\x1f%t\x1f%s\x1f%s\x1f%t",
			m.SourceID, m.ID, m.Version, m.Enabled, m.Deployed,
			m.UpdatePolicy, strings.Join(m.FileIDs, ","), m.ManualDownload))
		if m.External {
			// #269 x #336: the ONE thing verify reads that nothing else in
			// this fingerprint can see. See externalStatToken.
			rows = append(rows, fmt.Sprintf("ext\x1f%s\x1f%s\x1f%s\x1f%s",
				m.SourceID, m.ID, m.ExternalPath, externalStatToken(m.ExternalPath)))
		}
	}
	sort.Strings(rows)
	for _, row := range rows {
		// A hash.Hash never fails a write (its own contract), so the error
		// is discarded rather than threaded through a fingerprint that has
		// no failure mode of its own.
		_, _ = fmt.Fprintln(h, row)
	}

	// The profile file, for the lock state the version pass reads. A
	// missing/unreadable profile fingerprints as "no refs", which is
	// exactly how verify itself treats it (config.LoadProfile's error is
	// ignored there too).
	if prof, err := config.LoadProfile(s.ConfigDir(), game.ID, profile); err == nil && prof != nil {
		refs := make([]string, 0, len(prof.Mods))
		for _, ref := range prof.Mods {
			refs = append(refs, fmt.Sprintf("ref\x1f%s\x1f%s\x1f%s\x1f%t",
				ref.SourceID, ref.ModID, ref.Version, ref.Locked))
		}
		sort.Strings(refs)
		for _, ref := range refs {
			_, _ = fmt.Fprintln(h, ref)
		}
	}

	// The deployed_files checksum rows - verify's own FIRST read
	// (GetFilesWithChecksums, verify.go) and the one input that can move
	// the verdict without moving anything on disk. `lmm verify --fix`'s
	// checksum backfill writes exactly these rows and touches neither the
	// deployed tree, the profile, nor installed_mods, so nothing else here
	// would notice it. In-process that is covered by the gate (the repair
	// takes beginOp, which drops the memo); out of process it is not, and
	// #317 makes "the CLI beside a running `lmm serve`" the sanctioned
	// workflow rather than a warned-against one.
	//
	// The digest is over the rows themselves - (source, mod, file id,
	// checksum), sorted, so DB order cannot change it - rather than over a
	// table revision, because installed_mod_files carries no monotonic
	// counter to read and adding one would be a schema migration for a
	// fact the rows already state. It is one query, on a path that already
	// makes two sibling DB reads, and it stays proportional to the profile
	// rather than to the disk.
	files, err := s.GetFilesWithChecksums(ctx, game.ID, profile)
	if err != nil {
		return "", fmt.Errorf("fingerprinting deployed files: %w", err)
	}
	frows := make([]string, 0, len(files))
	for _, f := range files {
		// "dbfile", not "file": the tree walk below already writes
		// "file\x1f..." lines, and two record kinds sharing a prefix in
		// one digest is how a crafted path becomes a collision.
		frows = append(frows, fmt.Sprintf("dbfile\x1f%s\x1f%s\x1f%s\x1f%s",
			f.SourceID, f.ModID, f.FileID, f.Checksum))
	}
	sort.Strings(frows)
	for _, row := range frows {
		_, _ = fmt.Fprintln(h, row)
	}

	if err := fingerprintTree(ctx, h, game.ModPath); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fingerprintTree writes a stat-only summary of root into h: every entry's
// path relative to root, its size and its modification time, in the lexical
// order WalkDir produces (so the same tree always hashes the same).
//
// A root that does not exist is not an error - a game whose mod directory
// has never been created is an ordinary state, and it fingerprints as an
// empty tree, which is what it is. Anything else (a permission error, an
// I/O failure) stops the walk: see verifyFingerprint's contract on partial
// fingerprints.
func fingerprintTree(ctx context.Context, w io.Writer, root string) error {
	if root == "" {
		return nil
	}
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		// Lstat, not Stat: a symlink IS what lmm deploys in the default
		// link method, and following it would fingerprint the cache entry
		// it points at instead of the link itself.
		info, serr := os.Lstat(path)
		if serr != nil {
			return serr
		}
		_, werr := fmt.Fprintf(w, "file\x1f%s\x1f%d\x1f%d\n", rel, info.Size(), info.ModTime().UnixNano())
		return werr
	})
}

// externalStatToken summarises what verify's external presence pass reads
// about ONE external mod: whether Steam still has content at its
// ExternalPath, and when that directory last changed
// (verify.go's externalPresencePass / externalContentPresent).
//
// It belongs in the fingerprint because that directory is the only input
// to a verify answer that lies entirely OUTSIDE everything else summarised
// here. It is not under game.ModPath, so fingerprintTree never reaches it;
// no lmm row records its contents, so the installed rows say nothing about
// it; and the agent that owns it is the STEAM CLIENT, which unsubscribes
// an item with lmm not running at all - so no beginOp ever drops the memo
// for it. Without this, unsubscribing a Workshop item left every later
// Mission Control hydrate answering "no issues" from a memo taken while
// the item was still there.
//
// A path that cannot be stat'ed is not an error: "absent" IS a state the
// pass reports, and it only has to fingerprint DIFFERENTLY from "present".
// The presence predicate is recorded beside the stat because the pass
// counts an EMPTY directory as absent (Steam leaves one behind after an
// unsubscribe), and that is a distinction a size and an mtime alone are
// not guaranteed to carry on every filesystem.
func externalStatToken(path string) string {
	if path == "" {
		return "unset"
	}
	present := externalContentPresent(path)
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Sprintf("absent\x1f%t", present)
	}
	return fmt.Sprintf("stat\x1f%t\x1f%d\x1f%d", present, info.Size(), info.ModTime().UnixNano())
}
