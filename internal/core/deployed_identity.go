// Package core: deployed_identity.go answers "is the file at a deployed
// path still the one lmm put there?" for the copy and hardlink link methods
// (#466), and "was it deployed under the game's current mod_path?" (#451).
//
// Under the symlink method the answer is on disk: lmm's file is a link into
// its cache. Under copy and hardlink the deployed path is a regular file, so
// a file the user replaced looked exactly like lmm's own and every removal
// deleted it. A deploy therefore records the content it wrote - checksum,
// size and mtime (db.FileFingerprint) - and every removal and every
// overwrite compares before it acts.
//
// Every comparison fails closed. A file that cannot be read, a record that
// cannot be read, and a content mismatch all leave the file where it is. A
// record with no fingerprint (a row written before schema v18) is the one
// case removed without proof, as it always was, and reported as unverified.
package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// deployedVerdict is judgeDeployed's answer about one deployed path.
type deployedVerdict int

const (
	// deployedGone: nothing is at the path.
	deployedGone deployedVerdict = iota
	// deployedOurs: the file is lmm's - its content matches a recorded
	// fingerprint, or it is not a regular file and no record fingerprints
	// the path (a symlink deployment, whose link is its identity).
	deployedOurs
	// deployedUnverified: a regular file that records name but none
	// fingerprints - deployed before checksums were recorded. Removed as
	// before, and reported.
	deployedUnverified
	// deployedUsers: the file is not provably lmm's; it stays. With
	// recorded false it is a regular file lmm has no record of under the
	// game's current mod_path at all - foreign content, which a deploy may
	// still preserve and replace (captureOriginal).
	deployedUsers
)

// deployedJudgement is judgeDeployed's full answer.
type deployedJudgement struct {
	verdict deployedVerdict
	// reason says why a deployedUsers file is kept, as a clause: "its
	// content changed after lmm deployed it".
	reason string
	// recorded reports whether any record of the path under the game's
	// current mod_path exists.
	recorded bool
	// unchecked marks a deployedUsers answer that is only a failure to
	// look - the file, its record or its content could not be read - not a
	// finding that the file is the user's. The file stays either way; a
	// purge keeps its record too.
	unchecked bool
	// kept is the recorded state a deployedUsers file was judged against:
	// the record an overwrite that leaves the file keeps.
	kept *db.DeployedFileState
}

// fingerprintFile fingerprints the regular file at path.
func fingerprintFile(path string) (*db.FileFingerprint, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	sum, size, err := hashFile(path)
	if err != nil {
		return nil, err
	}
	return &db.FileFingerprint{Checksum: sum, Size: size, MTime: info.ModTime().UnixNano(), CTime: inodeChangeTime(info)}, nil
}

// recordRoot is the mod_path a deploy under game records (#451).
func recordRoot(game *domain.Game) string {
	if game.ModPath == "" {
		return ""
	}
	return filepath.Clean(game.ModPath)
}

// underCurrentRoot reports whether a record with the given recorded
// mod_path describes a path under game's current mod_path: one that
// recorded none is taken to, as every row did before #451.
func underCurrentRoot(game *domain.Game, recorded string) bool {
	return recorded == "" || samePath(recorded, game.ModPath)
}

// currentStates is database's records of rel in game, keeping only those
// deployed under its current mod_path. A record stranded under another one
// says nothing about what is at rel here.
func currentStates(ctx context.Context, database *db.DB, game *domain.Game, rel string) ([]db.DeployedFileState, error) {
	states, err := database.DeployedFileStates(ctx, game.ID, filepath.ToSlash(rel))
	if err != nil {
		return nil, err
	}
	kept := states[:0]
	for _, st := range states {
		if underCurrentRoot(game, st.ModPath) {
			kept = append(kept, st)
		}
	}
	return kept, nil
}

// judgeDeployed decides whether dst, the deployed path rel of game, holds
// lmm's own file. A nil database cannot tell and answers deployedOurs, the
// behaviour every db-less Installer has always had.
func judgeDeployed(ctx context.Context, database *db.DB, game *domain.Game, rel, dst string) deployedJudgement {
	if database == nil {
		return deployedJudgement{verdict: deployedOurs, recorded: true}
	}
	info, err := os.Lstat(dst)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		// ENOTDIR: a parent is not a directory, so nothing is at dst.
		return deployedJudgement{verdict: deployedGone}
	default:
		return deployedJudgement{verdict: deployedUsers, recorded: true, unchecked: true, reason: fmt.Sprintf("it could not be checked (%v)", err)}
	}
	states, err := currentStates(ctx, database, game, rel)
	if err != nil {
		return deployedJudgement{verdict: deployedUsers, recorded: true, unchecked: true, reason: fmt.Sprintf("its record could not be read (%v)", err)}
	}
	var printed []db.DeployedFileState
	for _, st := range states {
		if st.Fingerprint != nil {
			printed = append(printed, st)
		}
	}
	j := deployedJudgement{recorded: len(states) > 0}
	switch {
	case len(printed) > 0:
	case !info.Mode().IsRegular():
		j.verdict = deployedOurs
		return j
	case j.recorded:
		j.verdict = deployedUnverified
		return j
	default:
		j.verdict, j.reason = deployedUsers, "lmm has no record of deploying it under "+recordRoot(game)
		return j
	}
	j.kept = &printed[0]
	if !info.Mode().IsRegular() {
		j.verdict, j.reason = deployedUsers, "lmm deployed a file there and it is now "+describeMode(info.Mode())
		return j
	}
	for _, st := range printed {
		if unchangedSince(info, st.Fingerprint) {
			j.verdict = deployedOurs
			return j
		}
	}
	sum, _, err := hashFile(dst)
	if err != nil {
		j.verdict, j.unchecked = deployedUsers, true
		j.reason = fmt.Sprintf("it could not be read to compare with what lmm deployed (%v)", err)
		return j
	}
	for i, st := range printed {
		if st.Fingerprint.Checksum == sum {
			j.verdict, j.kept = deployedOurs, &printed[i]
			return j
		}
	}
	j.verdict, j.reason = deployedUsers, "its content changed after lmm deployed it"
	return j
}

// unchangedSince is the pre-check that spares a hash: info matches fp's
// size, mtime and ctime exactly. A size and an mtime can be copied onto
// another file; a ctime cannot be set at all, and any write, replacement or
// permission change moves it - so a file that passes is the one the record
// describes. An unknown ctime never passes.
func unchangedSince(info fs.FileInfo, fp *db.FileFingerprint) bool {
	ctime := inodeChangeTime(info)
	return ctime != 0 && fp.CTime == ctime &&
		fp.Size == info.Size() && fp.MTime == info.ModTime().UnixNano()
}

// inodeChangeTime is info's ctime in Unix nanoseconds, or 0 when the
// platform does not say.
func inodeChangeTime(info fs.FileInfo) int64 {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return st.Ctim.Nano()
}

// describeMode names a non-regular file for a keep reason.
func describeMode(mode fs.FileMode) string {
	switch {
	case mode&fs.ModeSymlink != 0:
		return "a link"
	case mode.IsDir():
		return "a directory"
	default:
		return "not a regular file"
	}
}

// userFileNote is what a removal that kept rel reports.
func userFileNote(rel, reason string) string {
	return fmt.Sprintf("%s was left in place: %s, so lmm treats it as yours", filepath.ToSlash(rel), reason)
}

// userFileOverwriteNote is what a deploy that did not replace rel reports.
func userFileOverwriteNote(rel, reason string) string {
	return fmt.Sprintf("%s was not replaced: %s, so lmm left your file there - delete it, then deploy again, to take the mod's version",
		filepath.ToSlash(rel), reason)
}

// unpreservedOverwriteNote is what a deploy reports for a file it did not
// replace because the originals store already holds another original of
// the path, so this one could not be kept.
func unpreservedOverwriteNote(rel string) string {
	return fmt.Sprintf("%s was not replaced: lmm did not deploy the file there and already keeps an earlier original of that path, so it could not keep this one too - move it aside, then deploy again, to take the mod's version",
		filepath.ToSlash(rel))
}

// unverifiedNote is the one report a flow makes for the files it removed
// without a content check.
func unverifiedNote(paths []string) string {
	const shown = 5
	list := slices.Clone(paths)
	slices.Sort(list)
	more := ""
	if len(list) > shown {
		more = fmt.Sprintf(" and %d more", len(list)-shown)
		list = list[:shown]
	}
	return fmt.Sprintf("%d copied or hard-linked file(s) were removed unverified (deployed before checksums were recorded): %s%s",
		len(paths), strings.Join(list, ", "), more)
}

// strandedRoot is a mod_path, other than the game's current one, that
// deployed files are recorded under (#451).
type strandedRoot struct {
	ModPath  string
	Files    int
	Profiles []string
}

// strandedRoots reads the mod_paths other than game's current one that
// database records deployed files under, sorted by path.
func strandedRoots(ctx context.Context, database *db.DB, game *domain.Game) ([]strandedRoot, error) {
	if database == nil {
		return nil, nil
	}
	roots, err := database.DeployedFileRoots(ctx, game.ID)
	if err != nil {
		return nil, fmt.Errorf("reading where %s's files were deployed: %w", game.ID, err)
	}
	byPath := map[string]*strandedRoot{}
	for _, r := range roots {
		if underCurrentRoot(game, r.ModPath) {
			continue
		}
		sr, ok := byPath[r.ModPath]
		if !ok {
			sr = &strandedRoot{ModPath: r.ModPath}
			byPath[r.ModPath] = sr
		}
		sr.Files += r.Files
		if !slices.Contains(sr.Profiles, r.Profile) {
			sr.Profiles = append(sr.Profiles, r.Profile)
		}
	}
	out := make([]strandedRoot, 0, len(byPath))
	for _, sr := range byPath {
		slices.Sort(sr.Profiles)
		out = append(out, *sr)
	}
	slices.SortFunc(out, func(a, b strandedRoot) int { return strings.Compare(a.ModPath, b.ModPath) })
	return out, nil
}
