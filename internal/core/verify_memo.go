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
// (version, file ids, policy, deployed/enabled state), and a stat-only walk
// of the game's deployed tree - each entry's path, size and modification
// time.
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
