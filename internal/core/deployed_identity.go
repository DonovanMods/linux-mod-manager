// Package core: deployed_identity.go answers "is the file at a deployed
// path still the one lmm put there?" for the copy and hardlink link methods
// (#466), and "was it deployed under the game's current mod_path?" (#451).
//
// Under the symlink method the answer combines disk and the installed row's
// recorded method: lmm's file is a link into its cache, while a non-link at a
// path recorded only by symlink deployments is the user's replacement (#483).
// Under copy and hardlink the deployed path is a regular file, so a file the
// user replaced looked exactly like lmm's own and every removal deleted it. A
// deploy therefore records the content it wrote - checksum, size and mtime
// (db.FileFingerprint) - and every removal and every overwrite compares before
// it acts.
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
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// deployedVerdict is deployedJudge's answer about one deployed path.
type deployedVerdict int

const (
	// deployedGone: nothing is at the path.
	deployedGone deployedVerdict = iota
	// deployedOurs: the file is lmm's - its content matches a recorded
	// fingerprint or the mod's cached copy, or it is a link lmm made into
	// the game's cache. A fingerprintless record is not enough by itself:
	// its installed row's method distinguishes a symlink deployment from a
	// legacy copy/hardlink (#483).
	deployedOurs
	// deployedUnverified: a regular file that records name but none
	// fingerprints, and the mod's cached copy is not there to compare
	// with. Removed as before, and reported.
	deployedUnverified
	// deployedUsers: the file is not provably lmm's; it stays. With
	// recorded false lmm has no record of the path under the game's
	// current mod_path at all - foreign content, which a deploy may still
	// preserve and replace (captureOriginal) when it is a regular file.
	deployedUsers
)

// deployedJudgement is deployedJudge's full answer.
type deployedJudgement struct {
	verdict deployedVerdict
	// reason says why a deployedUsers file is kept, as a clause: "its
	// content changed after lmm deployed it".
	reason string
	// recorded reports whether any record of the path under the game's
	// current mod_path exists.
	recorded bool
	// regular reports that the path holds a regular file - the only kind
	// the originals store can preserve.
	regular bool
	// unchecked marks a deployedUsers answer that is only a failure to
	// look - the file, its record or its content could not be read - not a
	// finding that the file is the user's. The file stays either way, and
	// so does every record of it.
	unchecked bool
	// legacy marks a deployedUnverified answer whose records all predate
	// schema v18 (they name no mod_path): deployed before checksums were
	// recorded, rather than recorded without one.
	legacy bool
	// cacheProof marks a deployedUsers answer that rests on the mod's
	// cached copy rather than on a fingerprint lmm recorded (#466 review
	// D10): a record from before checksums whose file differs from it.
	cacheProof bool
	// sharesCache marks a deployedUsers hard link that is still the mod's
	// cached copy: an edit in place changed that copy too, so a redeploy
	// would only link the edit again (#466 review D8).
	sharesCache bool
	// kept is the fingerprinted record a deployedUsers file was judged
	// against: the record an overwrite that leaves the file keeps. Nil
	// when the answer rests on no fingerprint.
	kept *db.DeployedFileState
}

// fingerprintFile fingerprints the regular file at path.
//
// The ctime is recorded only when it cannot be racy (git's "racy clean"
// rule, #466 review D1): a filesystem stamps changes with a coarse clock,
// so a write landing in the same tick as the one this fingerprint read
// would leave size, mtime and ctime all as recorded. When the fingerprint
// was finished within one tick of the ctime it read, the record carries no
// ctime, and every later check of it hashes.
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
	return fingerprintOf(info, sum, size, time.Now()), nil
}

// fingerprintOf is the fingerprint of a file whose Lstat was info and whose
// content hashed to sum and size, finished at done (fingerprintFile).
func fingerprintOf(info fs.FileInfo, sum string, size int64, done time.Time) *db.FileFingerprint {
	fp := &db.FileFingerprint{Checksum: sum, Size: size, MTime: info.ModTime().UnixNano()}
	if ctime := inodeChangeTime(info); ctime != 0 && !racyCTime(ctime, done) {
		fp.CTime = ctime
	}
	return fp
}

// racyCTime reports whether a change after done could still be stamped
// with ctime: done is less than one timestamp tick after it. A ctime with
// no sub-second part comes from a filesystem that keeps whole seconds
// (ext4 with small inodes, vfat's two), so its tick is taken as two
// seconds; any other is taken as a scheduler tick, generously.
func racyCTime(ctime int64, done time.Time) bool {
	tick := 20 * time.Millisecond
	if ctime%int64(time.Second) == 0 {
		tick = 2 * time.Second
	}
	return done.UnixNano()-ctime < int64(tick)
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

// deployedJudge decides whether the file at a deployed path of game is
// lmm's own (#466). A zero db cannot tell and answers deployedOurs, the
// behaviour every db-less Installer has always had.
type deployedJudge struct {
	db   *db.DB
	game *domain.Game
	// profile is the profile acting on the path. Its own records decide a
	// link (#466 review F1): another profile's fingerprinted copy of the
	// same path says nothing about the link this one deployed.
	profile string
	// cacheRoots is every directory lmm-owned content for game lives
	// under (Service.cacheRoots): a link into one is lmm's.
	cacheRoots []string
	// others is what the games whose mod directories overlap game's
	// record (#466 review D5): their fingerprints of the same file count.
	others *otherGameRecords
	// cached returns the mod's cached copy of rel for a record, or "" when
	// there is none to compare with (#466 review D10): the proof a record
	// with no fingerprint can still have.
	cached func(st db.DeployedFileState, rel string) string
}

// judge decides whether dst, the deployed path rel of the judge's game,
// holds lmm's own file.
func (jd deployedJudge) judge(ctx context.Context, rel, dst string) deployedJudgement {
	if jd.db == nil {
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
	states, err := currentStates(ctx, jd.db, jd.game, rel)
	if err != nil {
		return deployedJudgement{verdict: deployedUsers, recorded: true, unchecked: true, reason: fmt.Sprintf("its record could not be read (%v)", err)}
	}
	j := deployedJudgement{recorded: len(states) > 0, regular: info.Mode().IsRegular()}
	// #483: a symlink deployment cannot leave a regular file, directory or
	// other non-link object behind. If every installed row that records this
	// path says it used symlinks, what is there now is therefore the user's
	// replacement. This runs before fingerprint handling because symlink rows
	// deliberately have none; treating that absence as "unverified" let every
	// deploy remove the user's object. A copy/hardlink or unknown claimant
	// keeps the old conservative answer: another profile may own the regular
	// file, and a bare deployed_files row does not prove its method.
	if st := symlinkReplacementState(states, info.Mode()); st != nil {
		j.kept = st
		j.verdict = deployedUsers
		// Keep #469's established user-facing explanation: purge and verify
		// already expose it, and deploy now uses the same shared judgement.
		j.reason = replacedLinkNote
		return j
	}
	if !j.regular {
		return jd.judgeLink(j, info, states, dst)
	}
	var prints []db.DeployedFileState
	for _, st := range states {
		if st.Fingerprint != nil {
			prints = append(prints, st)
		}
	}
	theirs, err := jd.others.fingerprints(ctx, rel)
	if err != nil {
		j.verdict, j.unchecked, j.recorded = deployedUsers, true, true
		j.reason = fmt.Sprintf("another game's record of it could not be read (%v)", err)
		return j
	}
	if len(prints) > 0 {
		j.kept = &prints[0]
	}
	prints = append(prints, theirs...)
	if len(prints) == 0 {
		if !j.recorded {
			j.verdict, j.reason = deployedUsers, "lmm has no record of deploying it under "+recordRoot(jd.game)
			return j
		}
		return jd.judgeUnprinted(j, info, states, rel, dst)
	}
	if j.kept == nil {
		j.kept = &prints[0]
	}
	if fsKeepsChangeTime(dst) {
		for _, st := range prints {
			if unchangedSince(info, st.Fingerprint) {
				j.verdict = deployedOurs
				return j
			}
		}
	}
	sum, _, err := hashFile(dst)
	if err != nil {
		j.verdict, j.unchecked = deployedUsers, true
		j.reason = fmt.Sprintf("it could not be read to compare with what lmm deployed (%v)", err)
		return j
	}
	for i, st := range prints {
		if st.Fingerprint.Checksum == sum {
			j.verdict = deployedOurs
			if i < len(prints)-len(theirs) {
				j.kept = &prints[i]
			}
			return j
		}
	}
	j.verdict, j.reason = deployedUsers, "its content changed after lmm deployed it"
	j.sharesCache = jd.sharesCache(info, states, rel)
	return j
}

// symlinkReplacementState returns a representative record when mode is not
// a symlink and every installed row recording the path says it was deployed
// by symlink. A nil method (the installed row is gone) or any copy/hardlink
// claimant makes the provenance ambiguous and preserves the historical
// unverified judgement. This game-wide test is deliberate: the deployed tree
// is shared by its profiles, so a switch into a profile with no row of its own
// must still recognize a link another profile recorded and the user replaced.
func symlinkReplacementState(states []db.DeployedFileState, mode fs.FileMode) *db.DeployedFileState {
	if mode&fs.ModeSymlink != 0 || len(states) == 0 {
		return nil
	}
	for idx := range states {
		if states[idx].LinkMethod == nil || *states[idx].LinkMethod != domain.LinkSymlink {
			return nil
		}
	}
	return &states[0]
}

// judgeLink is judge for a path that holds something other than a regular
// file: a link, a directory, a device.
//
// A link into the game's cache is lmm's whatever the records say - it
// holds nothing of the user's. Anything else is lmm's only on a record
// under the game's current mod_path that fingerprints nothing - a link
// deployment's record - of the acting profile (#466 review F1, re-review
// R1): another profile's link deployment says nothing about what is at the
// path now, and a link lmm made is one into the cache. A judge acting for
// no profile counts every record. With no record at all it is the user's
// (review D2): a link lmm did not make may point anywhere, and a deploy
// that wrote through it would overwrite whatever it points at.
func (jd deployedJudge) judgeLink(j deployedJudgement, info fs.FileInfo, states []db.DeployedFileState, dst string) deployedJudgement {
	if info.Mode()&fs.ModeSymlink != 0 && jd.linksIntoCache(dst) {
		j.verdict = deployedOurs
		return j
	}
	if !j.recorded {
		j.verdict = deployedUsers
		j.reason = fmt.Sprintf("it is %s lmm has no record of deploying under %s", describeMode(info.Mode()), recordRoot(jd.game))
		return j
	}
	deciding := states
	if jd.profile != "" {
		deciding = statesOf(states, jd.profile)
	}
	if len(deciding) == 0 {
		// Recorded, so a deploy writes no record of its own, but by no
		// record of this profile's: kept, and nothing restored.
		j.verdict = deployedUsers
		j.reason = fmt.Sprintf("it is %s the %s profile has no record of deploying", describeMode(info.Mode()), jd.profile)
		return j
	}
	for _, st := range deciding {
		if st.Fingerprint == nil {
			j.verdict = deployedOurs
			return j
		}
	}
	j.kept = &deciding[0]
	j.verdict, j.reason = deployedUsers, "lmm deployed a file there and it is now "+describeMode(info.Mode())
	return j
}

// judgeUnprinted is judge for a regular file whose records under the
// game's current mod_path fingerprint nothing: the mod's cached copy is the
// proof when it is there (#466 review D10). Equal is lmm's. Different is
// the user's when that mod is the path's only claimant; when another mod,
// profile or game records the path too, the content may be theirs, and the
// file is unverified - as it is when there is nothing to compare with.
func (jd deployedJudge) judgeUnprinted(j deployedJudgement, info fs.FileInfo, states []db.DeployedFileState, rel, dst string) deployedJudgement {
	var sum string
	compared := false
	for _, st := range jd.comparable(states) {
		if jd.cached == nil {
			break
		}
		cached := jd.cached(st, rel)
		if cached == "" {
			continue
		}
		cachedSum, _, err := hashFile(cached)
		if err != nil {
			continue
		}
		if sum == "" {
			s, _, err := hashFile(dst)
			if err != nil {
				j.verdict, j.unchecked = deployedUsers, true
				j.reason = fmt.Sprintf("it could not be read to compare with the mod's cached copy (%v)", err)
				return j
			}
			sum = s
		}
		if sum == cachedSum {
			j.verdict = deployedOurs
			return j
		}
		compared = true
	}
	if compared && jd.onlyClaimant(states, rel) {
		j.verdict, j.cacheProof = deployedUsers, true
		j.reason = "it differs from the mod's cached copy, and lmm recorded no checksum when it deployed it"
		j.sharesCache = jd.sharesCache(info, states, rel)
		return j
	}
	j.verdict, j.legacy = deployedUnverified, true
	for _, st := range states {
		if st.ModPath != "" {
			j.legacy = false
		}
	}
	return j
}

// onlyClaimant reports whether every record of rel - the game's, under its
// current mod_path, and the overlapping games' - is the acting profile's
// record of one mod.
func (jd deployedJudge) onlyClaimant(states []db.DeployedFileState, rel string) bool {
	if len(jd.others.holding(rel)) > 0 {
		return false
	}
	for _, st := range states {
		if st.Profile != states[0].Profile || st.SourceID != states[0].SourceID || st.ModID != states[0].ModID {
			return false
		}
		if jd.profile != "" && st.Profile != jd.profile {
			return false
		}
	}
	return true
}

// linksIntoCache reports whether the link at dst points under one of the
// judge's cache roots.
func (jd deployedJudge) linksIntoCache(dst string) bool {
	if len(jd.cacheRoots) == 0 {
		return false
	}
	target, err := os.Readlink(dst)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(dst), target)
	}
	return underAnyCacheRoot(filepath.Clean(target), jd.cacheRoots)
}

// sharesCache reports whether the file info describes is the very file the
// mod's cache holds for rel - a hard link to it.
func (jd deployedJudge) sharesCache(info fs.FileInfo, states []db.DeployedFileState, rel string) bool {
	if jd.cached == nil {
		return false
	}
	for _, st := range jd.comparable(states) {
		cached := jd.cached(st, rel)
		if cached == "" {
			continue
		}
		if ci, err := os.Lstat(cached); err == nil && os.SameFile(info, ci) {
			return true
		}
	}
	return false
}

// comparable is the states whose mod's cached copy the judge can compare
// with: the acting profile's own, whose installed version is the one
// cached names - or every state, for a judge acting for no profile.
func (jd deployedJudge) comparable(states []db.DeployedFileState) []db.DeployedFileState {
	if jd.profile == "" {
		return states
	}
	return statesOf(states, jd.profile)
}

// statesOf is the states recorded by profile.
func statesOf(states []db.DeployedFileState, profile string) []db.DeployedFileState {
	var out []db.DeployedFileState
	for _, st := range states {
		if profile != "" && st.Profile == profile {
			out = append(out, st)
		}
	}
	return out
}

// judgeDeployed is a deployedJudge with only a database, a game and the
// acting profile.
func judgeDeployed(ctx context.Context, database *db.DB, game *domain.Game, profile, rel, dst string) deployedJudgement {
	return deployedJudge{db: database, game: game, profile: profile}.judge(ctx, rel, dst)
}

// unchangedSince is the pre-check that spares a hash: info matches fp's
// size, mtime and ctime exactly. A size and an mtime can be copied onto
// another file; a ctime cannot be set on a filesystem that keeps one
// (fsKeepsChangeTime, which the caller asks), and any write, replacement
// or permission change moves it - so a file that passes is the one the
// record describes. An unknown ctime, recorded or live, never passes.
func unchangedSince(info fs.FileInfo, fp *db.FileFingerprint) bool {
	ctime := inodeChangeTime(info)
	return ctime != 0 && fp.CTime == ctime &&
		fp.Size == info.Size() && fp.MTime == info.ModTime().UnixNano()
}

// Filesystem magic numbers (statfs(2)) of the filesystems that keep a real
// inode change time: one no user call can set, and that every write moves.
const (
	magicExt4     = 0xEF53 // ext2, ext3 and ext4 share it
	magicBtrfs    = 0x9123683E
	magicXFS      = 0x58465342
	magicTmpfs    = 0x01021994
	magicF2FS     = 0xF2F52010
	magicBcachefs = 0xCA451A4E
	magicZFS      = 0x2FC12FC1
)

// changeTimeFilesystems is the allow-list fsKeepsChangeTime trusts.
var changeTimeFilesystems = map[int64]bool{
	magicExt4: true, magicBtrfs: true, magicXFS: true, magicTmpfs: true,
	magicF2FS: true, magicBcachefs: true, magicZFS: true,
}

// statfsType is statfs(2)'s filesystem type for path; a seam for tests.
var statfsType = func(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Type), nil
}

// fsKeepsChangeTime reports whether the filesystem holding path keeps a
// change time the size/mtime/ctime pre-check can trust (#466 review D1).
// vfat and exFAT have none - their "ctime" is the mtime, which a user can
// set, at a granularity of up to two seconds - and NTFS, FUSE and anything
// unknown are not trusted either: a file there is always hashed.
func fsKeepsChangeTime(path string) bool {
	typ, err := statfsType(path)
	return err == nil && changeTimeFilesystems[typ]
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
func userFileOverwriteNote(rel, reason string, sharesCache bool) string {
	return fmt.Sprintf("%s was not replaced: %s, so lmm left your file there - %s",
		filepath.ToSlash(rel), reason, takeModVersionRemedy(sharesCache))
}

// takeModVersionRemedy is how the user takes the mod's version of a file
// they changed back. A hard link that is still the mod's cached copy was
// changed in the cache too (#466 review D8), so a redeploy would link the
// edit again: only a forced reinstall, which extracts the mod afresh, has
// the mod's version to give.
func takeModVersionRemedy(sharesCache bool) string {
	if sharesCache {
		return "it is a hard link to the mod's cached copy, so that copy changed too; run `lmm install --force` for the mod to extract its own version again"
	}
	return "delete it, then deploy again, to take the mod's version"
}

// unpreservedOverwriteNote is what a deploy reports for a file it did not
// replace because the originals store already holds another original of
// the path, so this one could not be kept.
func unpreservedOverwriteNote(rel string) string {
	return fmt.Sprintf("%s was not replaced: lmm did not deploy the file there and already keeps an earlier original of that path, so it could not keep this one too - move it aside, then deploy again, to take the mod's version",
		filepath.ToSlash(rel))
}

// uncapturableOverwriteNote is what a deploy reports for a path it did not
// replace because what is there could not be preserved first (#466 review
// D2, D4): a link or directory lmm did not make, or a file the originals
// store failed to copy.
func uncapturableOverwriteNote(rel, why string) string {
	return fmt.Sprintf("%s was not replaced: lmm did not deploy what is there, and it could not be preserved (%s), so lmm left it - move it aside, then deploy again, to take the mod's version",
		filepath.ToSlash(rel), why)
}

// unverifiedPath is a path a flow removed with no content check, and
// whether its records all predate schema v18.
type unverifiedPath struct {
	rel    string
	legacy bool
}

// unverifiedNotes are the reports a flow makes for the files it removed
// without a content check: one line for the files deployed before
// checksums were recorded, one for the files recorded without one (#466
// review D3).
func unverifiedNotes(paths []unverifiedPath) []string {
	var legacy, bare []string
	for _, p := range paths {
		if p.legacy {
			legacy = append(legacy, p.rel)
		} else {
			bare = append(bare, p.rel)
		}
	}
	var out []string
	if len(legacy) > 0 {
		out = append(out, unverifiedNote(legacy, "deployed before checksums were recorded"))
	}
	if len(bare) > 0 {
		out = append(out, unverifiedNote(bare, "recorded without a checksum"))
	}
	return out
}

// unverifiedNote is one unverifiedNotes line.
func unverifiedNote(paths []string, why string) string {
	const shown = 5
	list := slices.Clone(paths)
	slices.Sort(list)
	more := ""
	if len(list) > shown {
		more = fmt.Sprintf(" and %d more", len(list)-shown)
		list = list[:shown]
	}
	return fmt.Sprintf("%d copied or hard-linked file(s) were removed unverified (%s): %s%s",
		len(paths), why, strings.Join(list, ", "), more)
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
