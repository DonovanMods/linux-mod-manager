package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// Installer handles mod installation and uninstallation
type Installer struct {
	cache  *cache.Cache
	linker linker.Linker
	db     *db.DB // Optional: enables file tracking for conflict detection
	log    *slog.Logger

	// originals is #350's originals store, set by
	// Service.getInstallerForProfile. Nil disables capture entirely, which
	// is what keeps the white-box tests that build an Installer directly
	// working unchanged. Every deploy in the product funnels through this
	// type, which is why ONE field here covers the accepted-Overwrite
	// install, the archive import and a compile game's merged artifact
	// alike - see internal/core/originals.go's package comment.
	originals *originalsStore

	// adapter is the game adapter whose FileRouter decides which of a mod's
	// cached files the linker deploys (#353). Never nil: NewInstaller
	// defaults it to the built-in identity - which routes every file to the
	// linker, exactly as lmm always did - and Service.newInstallerWithLinker
	// replaces it with the game's resolved adapter.
	adapter adapter.GameAdapter

	// recordedOnly narrows every removal to the paths a deployed_files row
	// names for the mod (#413 fix round 4, F2). Service.newInstallerWithLinker
	// sets it when the game's adapter is refused: purge and uninstall still
	// run then (removalSnapshotOf), but without the adapter's routing a
	// path the cache entry merely NAMES - a seeded config the user has
	// since replaced with a link of their own - is no proof lmm put what is
	// there, and the linker removes any symlink it is pointed at.
	recordedOnly bool

	// refused is the adapter refusal this Installer was built under, or
	// nil (#413). Service.newInstallerWithLinker sets it with recordedOnly;
	// every deploy method returns it before touching anything. The flows
	// ask the adapter first (deployRefusal, a Plan's snapshot) and say so in
	// their own words - this is the backstop that makes a path that forgot
	// fail rather than deploy through the identity routing a refused
	// adapter leaves behind.
	refused error

	// otherGames reads the records of every other configured game whose mod
	// directory overlaps the one a call is for (Service.otherGamesRecording,
	// #445 gate 2, G2-1). Every removal and every overwrite asks it first
	// and leaves a path another game records exactly as it is (heldPath):
	// that game still tracks the file, which may be its live, user-edited
	// copy, and its own flows decide it. A read that fails refuses the call
	// before anything is touched. Nil - an Installer built without a
	// Service - asks nothing.
	otherGames func(context.Context, *domain.Game) (*otherGameRecords, error)

	// keptUser is the record of each path (slash form) a removal through
	// this Installer left because the file there is the user's (#466), so a
	// deploy through the same Installer leaves it too, and records it
	// again. An Installer with an originals store keeps this on the store
	// instead (rememberKept), where every Installer of the running flow
	// sees it - `lmm deploy --purge` purges and deploys through two.
	keptUser map[string]keptFile

	// lastSkipped is how many deployable paths the last Install left as
	// they were - another game's, the user's, or content lmm could not
	// preserve - so a flow can count only the files it wrote (#466 review
	// D7).
	lastSkipped int

	// cacheRoots is every directory lmm-owned content for the game lives
	// under (Service.cacheRoots): a link into one is lmm's (#466 review
	// F1). Nil - an Installer built without a Service - has none.
	cacheRoots []string
}

// NewInstaller creates a new installer
// The db parameter is optional - if nil, file tracking is disabled
func NewInstaller(cache *cache.Cache, linker linker.Linker, database *db.DB) *Installer {
	return &Installer{
		cache:   cache,
		linker:  linker,
		db:      database,
		log:     slog.New(slog.DiscardHandler),
		adapter: adapter.Generic{},
	}
}

// SetLogger sets the logger used for diagnostics. nil resets to discard,
// which is also the default.
func (i *Installer) SetLogger(l *slog.Logger) {
	if l == nil {
		l = slog.New(slog.DiscardHandler)
	}
	i.log = l
}

// setOriginals wires the originals store this Installer captures into
// (#350). Unexported: an Installer is a core primitive, and the store is
// resolved from the Service's data directory, never by a caller.
func (i *Installer) setOriginals(store *originalsStore) { i.originals = store }

// setAdapter wires the game's resolved adapter into this Installer (#353).
// Unexported for setOriginals' reason: an Installer is a core primitive, and
// the adapter is resolved from the Service's registry, never by a caller.
func (i *Installer) setAdapter(a adapter.GameAdapter) { i.adapter = a }

// captureOriginal preserves whatever is at dstPath before a deploy
// replaces it, when that file is one lmm does not own.
//
// "Does not own" is three cheap tests in order of cost: the destination
// must exist (Lstat), it must be a REGULAR file (a symlink is lmm's own
// deployment, or another manager's link - never stock content), and the
// deployed_files table must not already attribute it to a mod in this GAME
// (any profile - minor 7). The DB query only ever runs for a destination
// that is already a real file, which on a normal deploy is nothing at all,
// so this costs a stat per file and no more.
//
// A capture failure does not fail the deploy, but since #466 review D4 it
// does stop the deploy of that path: the file lmm could not preserve is
// left where it is, the other files go ahead, and the returned error is
// what the caller reports (uncapturableOverwriteNote) on the pending list
// the running flow drains onto its result's Warnings. The diagnostic Warn
// line stays for the log. A nil error is a capture made, or none needed.
func (i *Installer) captureOriginal(ctx context.Context, game *domain.Game, profileName, relPath, dstPath string, mod *domain.Mod) error {
	if i.originals == nil {
		return nil
	}
	info, err := os.Lstat(dstPath)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	if i.db != nil {
		// Game-scoped, not profile-scoped (#350 review minor 7): the
		// question here is "did lmm put this here at all", and `lmm deploy
		// -p B` over a copy deployment made under profile A must not treat
		// A's own file as stock content.
		owned, err := i.db.AnyProfileOwnsFile(ctx, game.ID, filepath.ToSlash(relPath))
		if err == nil && owned {
			return nil
		}
	}
	row := OriginalFile{
		Root: OriginalRootModPath, RelativePath: filepath.ToSlash(relPath),
		Op: OriginalOpDeploy, Profile: profileName,
	}
	if mod != nil {
		row.SourceID, row.ModID = mod.SourceID, mod.ID
	}
	if err := i.originals.capture(row, dstPath); err != nil {
		i.log.Warn("could not preserve the file this deploy would replace, so it is left in place",
			"path", dstPath, "err", err)
		return err
	}
	return nil
}

// restoreReplacedOriginal puts back whatever lmm displaced at relPath, at
// the moment lmm's own file there is removed (coordinator ruling on review
// note 13). No-op when this Installer has no store, or when nothing was
// ever captured for that path - which is every ordinary uninstall.
//
// Never fatal: a removal that succeeded must not be reported as a failure
// because the original could not go back. The failure is recorded on the
// store's always-on channel instead (review finding 5), which is where a
// user needs it - the file lmm cannot return is the one it holds the only
// copy of.
func (i *Installer) restoreReplacedOriginal(relPath, dstPath string) {
	if i.originals == nil {
		return
	}
	restored, err := i.originals.release(OriginalRootModPath, filepath.ToSlash(relPath), dstPath)
	if err != nil {
		i.log.Warn("could not put back the file this mod replaced", "path", dstPath, "err", err)
		i.originals.noteFailure(fmt.Sprintf(
			"could not put back the file lmm replaced at %s; `lmm snapshot restore` can still do it: %v", dstPath, err))
		return
	}
	if restored {
		i.log.Debug("put back the file this mod replaced", "path", dstPath)
	}
}

// notLinkerOwned reports that the game's adapter routed this cache member
// away from the linker (#413): adapter.RouteCopyOnce, which is a real file
// the user owns after its first deploy, or adapter.RouteSkip, which is
// never deployed at all.
//
// Every REMOVAL loop asks it, and each of them iterates the cache entry's
// RAW ListFiles union rather than its deployable set - deliberately, so a
// stale member a pre-#210 deploy linked is still cleaned up. That union
// names the routed members too, and neither of them is lmm's to remove:
// "never entered into deployed_files, and never removed by an uninstall"
// is the whole standing adapter.RouteCopyOnce inherits from a profile
// override.
//
// It is asked BEFORE foreignFile rather than left to it. In production
// foreignFile already protects a seeded file - it is a regular file no
// profile has a deployed_files row for - so this changes nothing a user
// can see (#413 review F8). What it changes is where the guarantee comes
// from: foreignFile can only answer with a database and answers false
// without one, so the promise held for a Service and not for a bare
// Installer. Asking the adapter first makes it a property of the route
// rather than of the ownership lookup.
func (i *Installer) notLinkerOwned(game *domain.Game, file string) bool {
	return adapter.Route(i.adapter, game, filepath.ToSlash(file)) != adapter.RouteLink
}

// heldPath is a path a removal or an overwrite left as it is because other
// games whose mod directories overlap this one record it (#445 gate 2,
// G2-1), or - userReason set - because the file there is the user's
// (#466).
type heldPath struct {
	path       string
	games      []string
	userReason string
}

// removalNote is what a removal that left h reports.
func (h heldPath) removalNote() string {
	if h.userReason != "" {
		return userFileNote(h.path, h.userReason)
	}
	return fmt.Sprintf("%s was left in place: %s records it too", h.path, gamesText(h.games))
}

// overwriteNote is what a deploy that did not replace h reports.
func (h heldPath) overwriteNote() string {
	return fmt.Sprintf("%s was not replaced: %s records it too, so lmm left that game's file there", h.path, gamesText(h.games))
}

// gamesText names games: "game a", or "games a, b".
func gamesText(games []string) string {
	if len(games) == 1 {
		return "game " + games[0]
	}
	return "games " + strings.Join(games, ", ")
}

// otherGamesFor reads what the games overlapping game's mod directory
// record (Installer.otherGames), or nil when there is no one to ask.
func (i *Installer) otherGamesFor(ctx context.Context, game *domain.Game) (*otherGameRecords, error) {
	if i.otherGames == nil {
		return nil, nil
	}
	others, err := i.otherGames(ctx, game)
	if err != nil {
		return nil, fmt.Errorf("checking which other games record files under %s, so nothing was changed: %w", game.ModPath, err)
	}
	return others, nil
}

// heldElsewhere is file as a heldPath when another game records it and
// something is at dstPath for that game to lose - a path that cannot be
// checked counts as there.
func heldElsewhere(others *otherGameRecords, file, dstPath string) (heldPath, bool) {
	games := others.recording(file)
	if len(games) == 0 {
		return heldPath{}, false
	}
	if _, err := os.Lstat(dstPath); errors.Is(err, fs.ErrNotExist) {
		return heldPath{}, false
	}
	return heldPath{path: filepath.ToSlash(file), games: games}, true
}

// noteHeld puts a held path's report on the pending list the running flow
// drains onto its result's Warnings (Service.takeCaptureWarnings).
func (i *Installer) noteHeld(msg string) {
	if i.originals != nil {
		i.originals.note(msg)
	}
}

// refuseStranded refuses a deploy while game has files recorded under a
// mod_path other than its current one (#451): deploying here would upsert
// their records onto this mod_path and orphan the files where they are.
// The refusal is ModPathProblem's, naming both ways out.
func (i *Installer) refuseStranded(ctx context.Context, game *domain.Game) error {
	problem, err := modPathMovedProblem(ctx, i.db, game)
	if err != nil {
		return err
	}
	if problem != nil {
		return problem
	}
	return nil
}

// deployedFingerprint is the fingerprint a deploy records for the file it
// just wrote at dstPath (#466): none for a symlink, whose link is its
// identity, and none when the file cannot be read back - that row is then
// unverified, removed as rows always were.
func (i *Installer) deployedFingerprint(dstPath string) *db.FileFingerprint {
	if i.linker.Method() == domain.LinkSymlink {
		return nil
	}
	fp, err := fingerprintFile(dstPath)
	if err != nil {
		i.log.Warn("could not fingerprint a deployed file; a removal will not be able to tell it from yours", "path", dstPath, "err", err)
		return nil
	}
	return fp
}

// heldFingerprint is the fingerprint a deploy records for file, a path it
// left because another game records it (#466 review D5): the content on
// disk when it is what that game deployed, otherwise that game's record of
// what it deployed - so this game's own removals can still tell a changed
// file from lmm's. None when that game fingerprinted nothing.
func (i *Installer) heldFingerprint(ctx context.Context, others *otherGameRecords, file, dstPath string) *db.FileFingerprint {
	if i.linker.Method() == domain.LinkSymlink {
		return nil
	}
	theirs, err := others.fingerprints(ctx, file)
	if err != nil || len(theirs) == 0 {
		return nil
	}
	if live, err := fingerprintFile(dstPath); err == nil {
		for _, st := range theirs {
			if st.Fingerprint.Checksum == live.Checksum {
				return live
			}
		}
	}
	return theirs[0].Fingerprint
}

// recordDeployed records file as mod's, deployed under game's current
// mod_path with fingerprint fp (nil for none).
func (i *Installer) recordDeployed(ctx context.Context, game *domain.Game, profileName, file string, mod *domain.Mod, fp *db.FileFingerprint) error {
	return i.db.RecordDeployedFile(ctx, db.DeployedFileRecord{
		GameID: game.ID, Profile: profileName, RelativePath: file,
		SourceID: mod.SourceID, ModID: mod.ID,
		ModPath: recordRoot(game), Fingerprint: fp,
	})
}

// judgeFor is the deployedJudge an Installer asks about game's paths on
// behalf of profileName. others is what the overlapping games record;
// cached, when set, is the cache entry of the mod whose deployment is being
// removed, which is the proof a record with no fingerprint still has.
func (i *Installer) judgeFor(game *domain.Game, profileName string, others *otherGameRecords, cached *cachedMod) deployedJudge {
	jd := deployedJudge{db: i.db, game: game, profile: profileName, cacheRoots: i.cacheRoots, others: others}
	if cached != nil {
		jd.cached = cached.fileFor
	}
	return jd
}

// cachedMod is one version of a mod in a cache: what a removal of that
// deployment compares an unfingerprinted file with (#466 review D10).
// method is how that deployment was made: only a copy or a hard link is
// compared - a regular file where a symlink deployment's link should be is
// #469's state, which keeps today's handling.
type cachedMod struct {
	cache  *cache.Cache
	game   *domain.Game
	mod    *domain.Mod
	method domain.LinkMethod
}

// fileFor is c's cached copy of rel when st records c's mod, the
// deployment was a copy or a hard link, and the copy is a regular file on
// disk, otherwise "".
func (c *cachedMod) fileFor(st db.DeployedFileState, rel string) string {
	if c == nil || c.cache == nil || c.method == domain.LinkSymlink || st.SourceID != c.mod.SourceID || st.ModID != c.mod.ID {
		return ""
	}
	path := c.cache.GetFilePath(c.game.ID, c.mod.SourceID, c.mod.ID, c.mod.Version, filepath.FromSlash(rel))
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return path
}

// removalCheck is keepOnRemoval's answer.
type removalCheck struct {
	// keep: leave the file, and report held.
	keep bool
	held heldPath
	// keepRecord: leave the path's record too - the file could not be
	// judged at all (#466 review D3), so nothing about it may change.
	keepRecord bool
	// unverified: the file goes with no content to compare; legacy says
	// its records predate schema v18.
	unverified, legacy bool
}

// keepOnRemoval reports whether a removal must leave dstPath where it is
// because the file there is not provably lmm's (#466); the Installer
// remembers it for a deploy that follows (keptUser).
func (i *Installer) keepOnRemoval(ctx context.Context, jd deployedJudge, file, dstPath string) removalCheck {
	j := jd.judge(ctx, file, dstPath)
	switch j.verdict {
	case deployedUsers:
		rel := filepath.ToSlash(file)
		i.rememberKept(rel, keptFile{record: j.kept, reason: j.reason, sharesCache: j.sharesCache})
		if !j.unchecked && j.recorded && (j.regular || j.kept != nil) {
			// Its record goes: a later deploy that sets it aside says so.
			i.markKeptUser(rel)
		}
		return removalCheck{keep: true, held: heldPath{path: rel, userReason: j.reason}, keepRecord: j.unchecked}
	case deployedUnverified:
		return removalCheck{unverified: true, legacy: j.legacy}
	}
	return removalCheck{}
}

// keptFile is what a removal remembers about a path it left as the
// user's: the fingerprinted record it was judged against (nil when the keep
// rested on none), and why.
type keptFile struct {
	record      *db.DeployedFileState
	reason      string
	sharesCache bool
}

// rememberKept records that a removal left rel as the user's (keptUser).
func (i *Installer) rememberKept(rel string, kf keptFile) {
	if i.originals != nil {
		i.originals.rememberKept(rel, kf)
		return
	}
	if i.keptUser == nil {
		i.keptUser = make(map[string]keptFile)
	}
	i.keptUser[rel] = kf
}

// markKeptUser marks rel in the originals store as a file a removal left
// as the user's and stopped tracking (originalsStore.markKeptUser).
func (i *Installer) markKeptUser(rel string) {
	if i.originals != nil {
		i.originals.markKeptUser(filepath.ToSlash(rel))
	}
}

// keptBefore is rememberKept's answer for rel.
func (i *Installer) keptBefore(rel string) (keptFile, bool) {
	if i.originals != nil {
		return i.originals.keptBefore(rel)
	}
	kf, ok := i.keptUser[rel]
	return kf, ok
}

// noteUnverified puts rel on the flow's unverified report.
func (i *Installer) noteUnverified(rel string, legacy bool) {
	if i.originals != nil {
		i.originals.noteUnverified(filepath.ToSlash(rel), legacy)
	}
}

// deployCheck is keepOnDeploy's answer.
type deployCheck struct {
	// keep: the deploy leaves the path as it is.
	keep bool
	// restore is the record the deploy writes for a kept path: the
	// fingerprinted record it was judged against. Nil writes nothing, and
	// leaves whatever record the path had as it was (#466 review F6).
	restore *db.DeployedFileState
	// preserve: the path holds content lmm has no record of, which the
	// deploy may replace only once captureOriginal has preserved it (#466
	// review D4).
	preserve bool
}

// keepOnDeploy reports whether a deploy must leave dstPath as it is (#466)
// and notes why: a file a removal in this flow kept as the user's, a
// recorded file that is no longer provably lmm's, a link or directory lmm
// did not make (review D2), or an unrecorded file the originals store
// cannot preserve because it already holds an earlier original of the
// path.
func (i *Installer) keepOnDeploy(ctx context.Context, jd deployedJudge, file, dstPath string) deployCheck {
	rel := filepath.ToSlash(file)
	if kf, ok := i.keptBefore(rel); ok {
		if _, err := os.Lstat(dstPath); !errors.Is(err, fs.ErrNotExist) {
			i.noteHeld(userFileOverwriteNote(rel, kf.reason, kf.sharesCache))
			return deployCheck{keep: true, restore: kf.record}
		}
	}
	if i.db == nil {
		return deployCheck{}
	}
	j := jd.judge(ctx, file, dstPath)
	switch {
	case j.verdict != deployedUsers:
		return deployCheck{}
	case !j.regular && j.kept == nil:
		// A link or directory no record of the acting profile names
		// (#466 re-review R1): the originals store cannot preserve it.
		i.noteHeld(uncapturableOverwriteNote(rel, j.reason))
		return deployCheck{keep: true}
	case j.recorded:
		i.noteHeld(userFileOverwriteNote(rel, j.reason, j.sharesCache))
		return deployCheck{keep: true, restore: j.kept}
	case !j.regular:
		i.noteHeld(uncapturableOverwriteNote(rel, j.reason))
		return deployCheck{keep: true}
	case i.originals == nil:
		return deployCheck{}
	}
	// Unrecorded content: captureOriginal preserves it before the deploy
	// replaces it - unless the store already holds an original of the path,
	// which a capture would leave as it is, or cannot say whether it does.
	held, err := i.originals.holds(OriginalRootModPath, rel)
	switch {
	case err != nil:
		i.originals.noteFailure(uncapturableOverwriteNote(rel, fmt.Sprintf("the originals store could not be read: %v", err)))
		return deployCheck{keep: true}
	case held:
		i.noteHeld(unpreservedOverwriteNote(rel))
		return deployCheck{keep: true}
	}
	return deployCheck{preserve: true}
}

// deployOver deploys srcPath at dstPath once keepOnDeploy has judged what
// is there replaceable. A link there is then lmm's own, and it is removed
// first: a linker never writes through one (#466 review D2).
func (i *Installer) deployOver(srcPath, dstPath string) error {
	if info, err := os.Lstat(dstPath); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		if err := os.Remove(dstPath); err != nil {
			return fmt.Errorf("removing the link lmm deployed: %w", err)
		}
	}
	return i.linker.Deploy(srcPath, dstPath)
}

// preserveBeforeDeploy preserves what is at dstPath before a deploy
// replaces it (#350), and reports whether the deploy may go ahead: a path
// whose unrecorded content could not be preserved is left (#466 review D4).
func (i *Installer) preserveBeforeDeploy(ctx context.Context, game *domain.Game, profileName, file, dstPath string, mod *domain.Mod, check deployCheck) bool {
	err := i.captureOriginal(ctx, game, profileName, file, dstPath, mod)
	if err == nil {
		// #466 review F5: a file an earlier removal left as the user's is
		// now set aside, and the user was told it was kept - so say where
		// it went.
		if check.preserve && i.originals.takeKeptUser(filepath.ToSlash(file)) {
			i.noteHeld(fmt.Sprintf("%s is the file you changed, which lmm left in place when it last removed the mod; this deploy set it aside and put the mod's version there - `lmm purge` puts yours back",
				filepath.ToSlash(file)))
		}
		return true
	}
	if check.preserve {
		i.originals.noteFailure(uncapturableOverwriteNote(file, err.Error()))
		return false
	}
	// A file lmm has a record of was never the store's to keep; the failed
	// capture of one (a record under another profile of the game) is a
	// note, as it always was.
	i.originals.noteFailure(fmt.Sprintf(
		"could not preserve %s before replacing it; it will not be restorable from a snapshot: %v", dstPath, err))
	return true
}

// foreignFile reports whether dstPath holds content lmm did not put there:
// a REGULAR file (a symlink is a deployment, lmm's or another tool's) that
// no profile of this GAME has a deployed_files row for.
//
// It is the same judgement captureOriginal makes before storing an
// original, and it is deliberately conservative in the same direction: an
// Installer with no database cannot tell, and answers false, so a
// db-less Installer behaves exactly as it always has.
//
// The ownership question is asked twice, profile first and then game-wide
// (#404): the deployed tree is GAME-GLOBAL, one directory every profile
// shares, so a file ANOTHER profile deployed is lmm's own file - not stock
// content - and a cross-profile Replace must be free to remove it. Asking
// only the profile-scoped question made "exactly one version of a mod on
// disk" true for symlink deployments and false for copy and hardlink ones:
// under those the deployed file IS regular, the acting profile has no row
// of its own for it, and the obsolete-file loop skipped the very file it
// exists to remove - leaving both versions live after a `lmm profile
// import` or a `lmm profile switch` that converges across profiles. It is
// the same "did lmm put this here at all" question #350 review minor 7
// added AnyProfileOwnsFile for and wired into captureOriginal.
//
// The widening is deliberate and bounded by the callers: each loop iterates
// ONE mod's own cache listing, so the paths reached are that mod's, and
// game-global "last writer wins" is the semantics the deployed tree already
// has. A file no profile ever deployed - the game's own content - is still
// foreign, so it is still captured rather than destroyed.
func (i *Installer) foreignFile(ctx context.Context, game *domain.Game, profileName, relPath, dstPath string) bool {
	if i.db == nil {
		return false
	}
	info, err := os.Lstat(dstPath)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	owner, err := i.db.GetFileOwner(ctx, game.ID, profileName, relPath)
	if err != nil || owner != nil {
		return false
	}
	owned, err := i.db.AnyProfileOwnsFile(ctx, game.ID, filepath.ToSlash(relPath))
	return err == nil && !owned
}

// Install deploys a mod to the game directory. If DB tracking is enabled and a
// SaveDeployedFile fails, only the file that failed to track is rolled back so
// the filesystem stays consistent with the database (previously deployed+tracked
// files are left in place).
func (i *Installer) Install(ctx context.Context, game *domain.Game, mod *domain.Mod, profileName string) error {
	if i.refused != nil {
		return i.refused
	}
	if err := i.refuseStranded(ctx, game); err != nil {
		return err
	}
	// Check if mod is cached
	if !i.cache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
		return fmt.Errorf("mod not in cache: %s/%s@%s", mod.SourceID, mod.ID, mod.Version)
	}

	// Get list of files in the cached mod
	files, err := deployableFiles(i.cache, i.adapter, game, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return fmt.Errorf("resolving deployable files: %w", err)
	}

	// #353/#413: the adapter's copy-once members are seeded BEFORE anything
	// is linked, and they are not in `files` at all - deployableFiles
	// already routed them out, because they are not the linker's to deploy.
	// They take no deployed_files row and no rollback slot deliberately:
	// once one is on disk it is the user's file, and the two things that
	// make a file lmm's own are exactly the two things a hand-edited config
	// must not be subject to.
	//
	// First rather than interleaved (where #358's BepInEx version sat), so
	// a seeding failure aborts with NOTHING deployed rather than with a
	// partial deployment to roll back. The install still fails, which is
	// the semantics that matters: a mod whose defaults could not be written
	// is not installed.
	others, err := i.otherGamesFor(ctx, game)
	if err != nil {
		return err
	}
	if err := seedCopyOnceFiles(i.cache, i.adapter, game, mod.SourceID, mod.ID, mod.Version); err != nil {
		return err
	}

	var deployed []string
	i.lastSkipped = 0
	for _, file := range files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		srcPath := i.cache.GetFilePath(game.ID, mod.SourceID, mod.ID, mod.Version, file)
		dstPath := filepath.Join(game.ModPath, file)

		// #445 gate 2, G2-1: another game's file is never replaced. The
		// profile still records the path - it is where its mod's file
		// goes - so its own purge keeps the file for that game, and a purge
		// of that game's keeps it for this one.
		if held, ok := heldElsewhere(others, file, dstPath); ok {
			i.noteHeld(held.overwriteNote())
			i.lastSkipped++
			if i.db != nil {
				if err := i.recordDeployed(ctx, game, profileName, file, mod, i.heldFingerprint(ctx, others, file, dstPath)); err != nil {
					return fmt.Errorf("tracking deployed file %s: %w", file, err)
				}
			}
			continue
		}
		// #466: nor the user's.
		check := i.keepOnDeploy(ctx, i.judgeFor(game, profileName, others, nil), file, dstPath)
		if check.keep {
			i.lastSkipped++
			if check.restore != nil {
				if err := i.recordDeployed(ctx, game, profileName, file, mod, check.restore.Fingerprint); err != nil {
					return fmt.Errorf("tracking deployed file %s: %w", file, err)
				}
			}
			continue
		}

		// #350: preserve whatever is there before the deploy replaces it.
		if !i.preserveBeforeDeploy(ctx, game, profileName, file, dstPath, mod, check) {
			i.lastSkipped++
			continue
		}

		if err := i.deployOver(srcPath, dstPath); err != nil {
			rollbackErr := rollbackDeploy(i.linker, game.ModPath, deployed)
			// A rolled-back install must leave no hole either: every file
			// it removed on the way out gets its original back, including
			// the one whose deploy just failed (capture ran before it).
			i.restoreReplacedOriginals(game, append(append([]string(nil), deployed...), file))
			if i.db != nil {
				_ = i.db.DeleteDeployedFiles(ctx, game.ID, profileName, mod.SourceID, mod.ID)
			}
			if rollbackErr != nil {
				return &domain.DeployError{Op: fmt.Sprintf("deploying %s", file), Primary: err, Rollback: rollbackErr}
			}
			return fmt.Errorf("deploying %s: %w", file, err)
		}
		deployed = append(deployed, file)

		// Track file ownership in database (for conflict detection)
		if i.db != nil {
			if err := i.recordDeployed(ctx, game, profileName, file, mod, i.deployedFingerprint(dstPath)); err != nil {
				// Roll back only the file that failed to track; leave previously
				// deployed+tracked files and DB records intact.
				rollbackErr := rollbackDeploy(i.linker, game.ModPath, []string{file})
				i.restoreReplacedOriginals(game, []string{file})
				if rollbackErr != nil {
					return &domain.DeployError{Op: fmt.Sprintf("tracking deployed file %s", file), Primary: err, Rollback: rollbackErr}
				}
				return fmt.Errorf("tracking deployed file %s: %w", file, err)
			}
		}
	}

	return nil
}

// Replace swaps an existing deployment with a new cached version and restores
// the old files if the replacement fails.
func (i *Installer) Replace(ctx context.Context, game *domain.Game, oldMod, newMod *domain.Mod, profileName string) error {
	return i.replaceWithCaches(ctx, game, i.cache, i.cache, oldMod, newMod, profileName, nil, nil)
}

// ReplaceForUpdate is Replace carrying the update path's file-ID transition:
// the mod's installed file IDs BEFORE the update (oldFileIDs) and the full
// set being installed by it (newFileIDs - ApplyUpdate's downloadedFileIDs,
// exactly what it records to the DB row and profile ref). They matter only in
// the degenerate same-version shape - a file-only update whose version string
// does not change shares ONE version-keyed cache directory between the old
// and new files, so the plain union replace could never undeploy a departing
// file's members (#144 item 4). ApplyRollback wires the same transition
// reversed - current FileIDs -> PreviousFileIDs - so a same-version rollback
// narrows identically instead of deploying the union (#150). See
// resolveSharedDirUpdate for the exact
// ownership rules; on a normal different-version update (distinct cache dirs)
// this behaves exactly like Replace.
func (i *Installer) ReplaceForUpdate(ctx context.Context, game *domain.Game, oldMod, newMod *domain.Mod, profileName string, oldFileIDs, newFileIDs []string) error {
	return i.replaceWithCaches(ctx, game, i.cache, i.cache, oldMod, newMod, profileName, oldFileIDs, newFileIDs)
}

// ReplaceWithCaches swaps an existing deployment using explicit old and new caches.
func (i *Installer) ReplaceWithCaches(ctx context.Context, game *domain.Game, oldCache, newCache *cache.Cache, oldMod, newMod *domain.Mod, profileName string) error {
	return i.replaceWithCaches(ctx, game, oldCache, newCache, oldMod, newMod, profileName, nil, nil)
}

// ReplaceWithOldCache swaps an existing deployment using an alternate cache
// snapshot for the old version.
func (i *Installer) ReplaceWithOldCache(ctx context.Context, game *domain.Game, oldCache *cache.Cache, oldMod, newMod *domain.Mod, profileName string) error {
	return i.replaceWithCaches(ctx, game, oldCache, i.cache, oldMod, newMod, profileName, nil, nil)
}

func (i *Installer) replaceWithCaches(ctx context.Context, game *domain.Game, oldCache, newCache *cache.Cache, oldMod, newMod *domain.Mod, profileName string, oldFileIDs, newFileIDs []string) error {
	if i.refused != nil {
		return i.refused
	}
	if err := i.refuseStranded(ctx, game); err != nil {
		return err
	}
	if !oldCache.Exists(game.ID, oldMod.SourceID, oldMod.ID, oldMod.Version) {
		return fmt.Errorf("old mod not in cache: %s/%s@%s", oldMod.SourceID, oldMod.ID, oldMod.Version)
	}
	if !newCache.Exists(game.ID, newMod.SourceID, newMod.ID, newMod.Version) {
		return fmt.Errorf("new mod not in cache: %s/%s@%s", newMod.SourceID, newMod.ID, newMod.Version)
	}

	others, err := i.otherGamesFor(ctx, game)
	if err != nil {
		return err
	}
	oldFiles, err := oldCache.ListFiles(game.ID, oldMod.SourceID, oldMod.ID, oldMod.Version)
	if err != nil {
		return fmt.Errorf("listing old cached files: %w", err)
	}
	newFiles, err := deployableFiles(newCache, i.adapter, game, newMod.SourceID, newMod.ID, newMod.Version)
	if err != nil {
		return fmt.Errorf("resolving deployable new-side files: %w", err)
	}

	// #144 item 4: in the degenerate same-version shape (old and new resolve
	// to ONE cache dir, so both listings are the same union), the deploy set
	// narrows to the members the mod's CURRENT file IDs own - every other
	// listed member is excluded from the NEW side, so the obsolete-file loop
	// below undeploys it like any other old-only file (Undeploy tolerates an
	// already-absent path) and the deploy/DB loops never touch it. Rollback
	// likewise narrows to what the OLD side's IDs actually owned. A false ok
	// (distinct dirs, no departing ID, or incomplete provenance) leaves the
	// historical union behavior byte-for-byte intact.
	newCurrent, oldDeployed, provenanceOK := resolveSharedDirUpdate(game.ID, oldCache, newCache, oldMod, newMod, oldFileIDs, newFileIDs, newFiles)

	// oldRestorable starts as the old side's deployable set (deploy-direction,
	// #210), not the raw oldFiles union: a rollback restores the pre-replace
	// DEPLOYMENT, which is whatever deployableFiles resolved for the old
	// entry, never the union. An old-side mixed entry (recorded markers plus
	// a retained source plus an unclaimed stale file) never deploys the stale
	// file in the first place, so a mid-replace failure must not link it
	// fresh either - using oldFiles here would resurrect it through this
	// error path even though the #210 narrowing kept it off disk on every
	// success path.
	oldRestorable, err := deployableFiles(oldCache, i.adapter, game, oldMod.SourceID, oldMod.ID, oldMod.Version)
	if err != nil {
		return fmt.Errorf("resolving deployable old-side files: %w", err)
	}
	if provenanceOK {
		kept := make([]string, 0, len(newFiles))
		for _, file := range newFiles {
			if newCurrent[file] {
				kept = append(kept, file)
			}
		}
		newFiles = kept

		kept = make([]string, 0, len(oldFiles))
		for _, file := range oldFiles {
			if oldDeployed[file] {
				kept = append(kept, file)
			}
		}
		oldRestorable = kept
	}

	// oldSet drives every restore decision: only members the OLD deployment
	// actually owned may be put back by a rollback. Without provenance it is
	// the full old listing, preserving the historical behavior exactly.
	oldSet := make(map[string]bool, len(oldRestorable))
	for _, file := range oldRestorable {
		oldSet[file] = true
	}
	newSet := make(map[string]bool, len(newFiles))
	for _, file := range newFiles {
		newSet[file] = true
	}

	// #466 review D9: the old deployment's records exactly as they are, so
	// a failure below puts them back as they were - fingerprints and all -
	// and so a path this replace leaves as the user's keeps its record.
	var oldRecords map[string]db.DeployedFileRecord
	if i.db != nil {
		records, err := i.db.DeployedFileRecordsForMod(ctx, game.ID, profileName, oldMod.SourceID, oldMod.ID)
		if err != nil {
			return fmt.Errorf("reading file tracking: %w", err)
		}
		oldRecords = make(map[string]db.DeployedFileRecord, len(records))
		for _, rec := range records {
			oldRecords[rec.RelativePath] = rec
		}
	}
	// keepRecords is the old-side paths whose records stay exactly as they
	// are: a file this replace could not judge (#466 review D3).
	keepRecords := make(map[string]bool)

	var removedOld []string
	for _, file := range oldFiles {
		if newSet[file] {
			continue
		}
		dstPath := filepath.Join(game.ModPath, file)
		// #413: a copy-once member the OLD version shipped stays exactly
		// where it is. It is the user's file, and the new version's own
		// default is seeded above only if nothing is there.
		if i.notLinkerOwned(game, file) {
			continue
		}
		// #350 / review finding 4: this loop iterates the OLD entry's RAW
		// ListFiles union, not its deployable set, so a member lmm never
		// deployed is visited here - the #210 narrowing case, and the
		// stale-unclaimed-member case. Under copy or hardlink Undeploy
		// removes whatever is at the path, which for such a member is the
		// game's own content. Same guard, same reason, as Uninstall's:
		// leave a regular file with no deployed_files row alone. Nothing
		// is captured, because nothing is being replaced - the file stays
		// exactly where it is.
		if i.foreignFile(ctx, game, profileName, file, dstPath) {
			i.log.Debug("leaving a file this update does not own where it is", "path", dstPath)
			continue
		}
		// #466: nor the user's - asked before whether another game
		// records it, so a changed file is reported as the user's (review
		// D5).
		rc := i.keepOnRemoval(ctx, i.judgeFor(game, profileName, others, &cachedMod{cache: oldCache, game: game, mod: oldMod, method: i.linker.Method()}), file, dstPath)
		if rc.keep {
			i.noteHeld(rc.held.removalNote())
			if rc.keepRecord {
				keepRecords[file] = true
			}
			continue
		}
		// #445 gate 2, G2-1: nor one another game records.
		if held, ok := heldElsewhere(others, file, dstPath); ok {
			i.noteHeld(held.removalNote())
			continue
		}
		if err := i.linker.Undeploy(dstPath); err != nil {
			if rollbackErr := i.restoreOldFiles(oldCache, game, oldMod, removedOld, nil, oldSet); rollbackErr != nil {
				return &domain.DeployError{Op: fmt.Sprintf("removing obsolete file %s", file), Primary: err, Rollback: rollbackErr}
			}
			return fmt.Errorf("removing obsolete file %s: %w", file, err)
		}
		if rc.unverified {
			i.noteUnverified(file, rc.legacy)
		}
		removedOld = append(removedOld, file)
	}

	// #353/#413: the new version's copy-once defaults, seeded before
	// anything is linked. They are not part of the replacement and never
	// were: the old version's copy stays exactly where it is (the
	// obsolete-file loop above skips it - a regular file with no
	// deployed_files row, which foreignFile answers for), and copyOnce
	// never overwrites the user's edit with a new version's default.
	if err := seedCopyOnceFiles(newCache, i.adapter, game, newMod.SourceID, newMod.ID, newMod.Version); err != nil {
		if rollbackErr := i.restoreOldFiles(oldCache, game, oldMod, removedOld, nil, oldSet); rollbackErr != nil {
			return &domain.DeployError{Op: "seeding adapter files", Primary: err, Rollback: rollbackErr}
		}
		return err
	}

	var replacedOrAdded []string
	// What the DB loop below records for each new-side path: the
	// fingerprint of the file this loop wrote, or - for a path it left as
	// the user's (#466) - the record that path is kept under, or no record
	// at all (skipped).
	fingerprints := make(map[string]*db.FileFingerprint)
	kept := make(map[string]*db.DeployedFileState)
	skipped := make(map[string]bool)
	newJudge := i.judgeFor(game, profileName, others, nil)
	for _, file := range newFiles {
		select {
		case <-ctx.Done():
			if rollbackErr := i.restoreOldFiles(oldCache, game, oldMod, removedOld, replacedOrAdded, oldSet); rollbackErr != nil {
				return &domain.DeployError{Primary: ctx.Err(), Rollback: rollbackErr}
			}
			return ctx.Err()
		default:
		}

		srcPath := newCache.GetFilePath(game.ID, newMod.SourceID, newMod.ID, newMod.Version, file)
		dstPath := filepath.Join(game.ModPath, file)
		// #445 gate 2, G2-1: another game's file is never replaced; the
		// path is still recorded for the new version below, as Install
		// records it.
		if held, ok := heldElsewhere(others, file, dstPath); ok {
			i.noteHeld(held.overwriteNote())
			fingerprints[file] = i.heldFingerprint(ctx, others, file, dstPath)
			continue
		}
		// #466: nor the user's.
		check := i.keepOnDeploy(ctx, newJudge, file, dstPath)
		if check.keep {
			if check.restore != nil {
				kept[file] = check.restore
			} else {
				skipped[file] = true
			}
			continue
		}
		// #350: a replace can also land on a file lmm does not own - a
		// new version whose file list grew into stock content - so the
		// original is preserved here before the new file goes over it.
		// The obsolete-file loop above needs no capture of its own: since
		// review finding 4 it SKIPS a path lmm does not own rather than
		// removing it, and restoreOldFiles only ever puts lmm's own files
		// back. What that loop DOES need is the release, which is at the
		// end of this function - see the comment there.
		if !i.preserveBeforeDeploy(ctx, game, profileName, file, dstPath, newMod, check) {
			skipped[file] = true
			continue
		}
		if err := i.deployOver(srcPath, dstPath); err != nil {
			cleanupErr := i.linker.Undeploy(dstPath)
			rollbackFiles := append(append([]string(nil), replacedOrAdded...), file)
			rollbackErr := i.restoreOldFiles(oldCache, game, oldMod, removedOld, rollbackFiles, oldSet)
			if cleanupErr != nil || rollbackErr != nil {
				return &domain.DeployError{Op: fmt.Sprintf("deploying %s", file), Primary: err, Cleanup: cleanupErr, Rollback: rollbackErr}
			}
			return fmt.Errorf("deploying %s: %w", file, err)
		}
		replacedOrAdded = append(replacedOrAdded, file)
		fingerprints[file] = i.deployedFingerprint(dstPath)
	}

	if i.db != nil {
		restoreOldRecords := func() {
			_ = i.db.DeleteDeployedFiles(ctx, game.ID, profileName, newMod.SourceID, newMod.ID)
			for _, rec := range oldRecords {
				_ = i.db.RecordDeployedFile(ctx, rec)
			}
		}
		if err := i.db.DeleteDeployedFiles(ctx, game.ID, profileName, oldMod.SourceID, oldMod.ID); err != nil {
			if rollbackErr := i.restoreOldFiles(oldCache, game, oldMod, removedOld, replacedOrAdded, oldSet); rollbackErr != nil {
				return &domain.DeployError{Op: "resetting file tracking", Primary: err, Rollback: rollbackErr}
			}
			return fmt.Errorf("resetting file tracking: %w", err)
		}
		// What is written: each new-side path this replace deployed or
		// kept with a fingerprinted record; each path it left with no
		// record to write (#466 review F6) or could not judge keeps its old
		// record exactly as it was, under the new mod.
		var records []db.DeployedFileRecord
		written := make(map[string]bool)
		for _, file := range newFiles {
			rec := db.DeployedFileRecord{
				GameID: game.ID, Profile: profileName, RelativePath: file,
				SourceID: newMod.SourceID, ModID: newMod.ID,
				ModPath: recordRoot(game), Fingerprint: fingerprints[file],
			}
			if restore := kept[file]; restore != nil {
				rec.Fingerprint = restore.Fingerprint
			} else if skipped[file] {
				old, ok := oldRecords[file]
				if !ok {
					continue
				}
				rec.ModPath, rec.Fingerprint = old.ModPath, old.Fingerprint
			}
			records = append(records, rec)
			written[file] = true
		}
		for file := range keepRecords {
			if old, ok := oldRecords[file]; ok && !written[file] {
				records = append(records, old)
			}
		}
		for _, rec := range records {
			if err := i.db.RecordDeployedFile(ctx, rec); err != nil {
				file := rec.RelativePath
				restoreOldRecords()
				if rollbackErr := i.restoreOldFiles(oldCache, game, oldMod, removedOld, replacedOrAdded, oldSet); rollbackErr != nil {
					return &domain.DeployError{Op: fmt.Sprintf("tracking deployed file %s", file), Primary: err, Rollback: rollbackErr}
				}
				return fmt.Errorf("tracking deployed file %s: %w", file, err)
			}
		}
	}

	// #350 re-review finding N1 - ruling (a) for the obsolete-file loop.
	// That loop removed lmm's OWN files (a member the new side no longer
	// ships), which is exactly the removal ruling (a) covers: whatever each
	// of them replaced goes back, so `lmm update` and `lmm update rollback`
	// stop leaving the hole every other removal path has stopped leaving.
	//
	// Deliberately HERE rather than inside the loop, because this function -
	// unlike Uninstall and Install's rollbacks - can still fail after the
	// removal: every error path above replays removedOld through
	// restoreOldFiles, which would deploy the old mod's file back OVER a
	// just-restored original whose manifest row had already been dropped,
	// leaving lmm with no record and the user with the wrong bytes. Past the
	// last failure point there is nothing left to roll back, and "the row
	// goes once the original is back in place" stays true. A failed put-back
	// is reported on the always-on channel and keeps its row, so
	// `lmm snapshot restore` can still do it (restoreReplacedOriginal).
	i.restoreReplacedOriginals(game, removedOld)

	return nil
}

// resolveSharedDirUpdate resolves member ownership for a same-version
// file-only update whose old and new cache keys share ONE version directory
// (#144 item 4). It returns ok=false - meaning "fall back to today's union
// behavior exactly" - unless EVERY condition for positive provenance holds:
//
//   - both ID sets are known, the transition actually CHANGES the installed
//     ID set (set(oldFileIDs) != set(newFileIDs) - a symmetric predicate, so
//     the forward call and its swapped-transition compensation call always
//     answer the same way; asking only "does an old ID depart?" diverges on
//     pure-removal/pure-addition transitions and made a compensated failure
//     union-deploy a stale generation's never-deployed member), and the old
//     and new keys resolve to the same version directory (distinct dirs are
//     already handled by the obsolete-file loop),
//   - every ID in oldFileIDs and newFileIDs has a completion marker with a
//     RECORDED member manifest (a legacy bare marker - any pre-manifest
//     cache entry - makes what that file contributed, or what it still
//     needs, unknowable),
//   - every file in the shared directory's listing is attributed by at least
//     one recorded manifest, stale markers included (unattributed content
//     proves an unmanifested contributor exists, e.g. an entry populated
//     directly by `lmm import`).
//
// unionFiles is the caller's deploy-direction set (deployableFiles' output,
// #210), not always the raw ListFiles union: when that resolver narrowed to
// recorded members, every entry here is attributed by construction, since
// resolveSharedDirUpdate independently requires all-recorded provenance too;
// when it fell back to the full union, the attribution check below behaves
// exactly as before.
//
// The ownership rule: the DEPLOY set is exactly the members attributed to the
// mod's current (new-side) file IDs - newCurrent. Every other listed member
// is undeployed, its provenance being its own manifest: that covers both the
// departing IDs of THIS update and stale members left by earlier same-version
// updates, whose markers remain in the shared dir after their IDs left the
// installed set. Those stale markers are deliberately NOT survivors - a
// survivor is an ID the mod still installs, never "any marker present" -
// otherwise chained same-version updates would resurrect the members the
// previous update correctly removed. oldDeployed (the members attributed to
// the old-side IDs) is what a rollback may restore: the pre-replace
// deployment, not the union.
//
// The fallback is deliberately silent - pre-manifest caches are the norm for
// existing installs, and they must not produce a warning storm; they simply
// keep the historical union behavior. Never guess, never undeploy without
// positive provenance.
func resolveSharedDirUpdate(gameID string, oldCache, newCache *cache.Cache, oldMod, newMod *domain.Mod, oldFileIDs, newFileIDs, unionFiles []string) (newCurrent, oldDeployed map[string]bool, ok bool) {
	if len(oldFileIDs) == 0 || len(newFileIDs) == 0 {
		return nil, nil, false
	}
	newIDs := make(map[string]bool, len(newFileIDs))
	for _, id := range newFileIDs {
		newIDs[id] = true
	}
	oldIDs := make(map[string]bool, len(oldFileIDs))
	for _, id := range oldFileIDs {
		oldIDs[id] = true
	}
	sameIDSet := len(oldIDs) == len(newIDs)
	if sameIDSet {
		for id := range oldIDs {
			if !newIDs[id] {
				sameIDSet = false
				break
			}
		}
	}
	if sameIDSet {
		return nil, nil, false
	}
	oldDir := oldCache.ModPath(gameID, oldMod.SourceID, oldMod.ID, oldMod.Version)
	newDir := newCache.ModPath(gameID, newMod.SourceID, newMod.ID, newMod.Version)
	if filepath.Clean(oldDir) != filepath.Clean(newDir) {
		return nil, nil, false
	}

	manifests, err := newCache.FileManifests(gameID, newMod.SourceID, newMod.ID, newMod.Version)
	if err != nil {
		return nil, nil, false // unreadable bookkeeping is absent bookkeeping: union fallback
	}
	memberSet := func(ids []string) (map[string]bool, bool) {
		set := make(map[string]bool)
		for _, id := range ids {
			m, present := manifests[id]
			if !present || !m.Recorded {
				return nil, false
			}
			for _, member := range m.Members {
				set[member] = true
			}
		}
		return set, true
	}
	newCurrent, ok = memberSet(newFileIDs)
	if !ok {
		return nil, nil, false
	}
	oldDeployed, ok = memberSet(oldFileIDs)
	if !ok {
		return nil, nil, false
	}

	attributed := make(map[string]bool)
	for _, m := range manifests {
		if !m.Recorded {
			continue // a bare STALE marker attributes nothing; old/new-side bareness already failed above
		}
		for _, member := range m.Members {
			attributed[member] = true
		}
	}
	for _, file := range unionFiles {
		if !attributed[file] {
			return nil, nil, false
		}
	}
	return newCurrent, oldDeployed, true
}

func (i *Installer) restoreOldFiles(oldCache *cache.Cache, game *domain.Game, oldMod *domain.Mod, removedOld, replacedOrAdded []string, oldSet map[string]bool) error {
	var errs []error

	for j := len(replacedOrAdded) - 1; j >= 0; j-- {
		file := replacedOrAdded[j]
		dstPath := filepath.Join(game.ModPath, file)
		if oldSet[file] {
			srcPath := oldCache.GetFilePath(game.ID, oldMod.SourceID, oldMod.ID, oldMod.Version, file)
			if err := i.linker.Deploy(srcPath, dstPath); err != nil {
				errs = append(errs, fmt.Errorf("restoring %s: %w", file, err))
			}
			continue
		}
		if err := i.linker.Undeploy(dstPath); err != nil {
			errs = append(errs, fmt.Errorf("removing %s: %w", file, err))
			continue
		}
		// #369, ruling (a) in the failure direction. This file is NEW-only:
		// the replace deployed it over whatever was at the path, capturing
		// the original first (replaceWithCaches' deploy loop). Removing it
		// again is the same removal ruling (a) covers, so the original goes
		// back here - after the Undeploy and only if it succeeded, exactly
		// as Uninstall's own loop orders the pair. The oldSet branch above
		// needs no release: it puts lmm's OWN file back at that path, so
		// nothing lmm displaced there has been given up.
		i.restoreReplacedOriginal(file, dstPath)
	}

	for j := len(removedOld) - 1; j >= 0; j-- {
		file := removedOld[j]
		// Only restore members the OLD deployment actually owned. Without
		// provenance oldSet is the full old listing and this never skips;
		// with it, a stale member routed through the obsolete loop (its
		// Undeploy was a no-op - it wasn't deployed) must not be deployed
		// by the rollback either.
		if !oldSet[file] {
			continue
		}
		srcPath := oldCache.GetFilePath(game.ID, oldMod.SourceID, oldMod.ID, oldMod.Version, file)
		dstPath := filepath.Join(game.ModPath, file)
		if err := i.linker.Deploy(srcPath, dstPath); err != nil {
			errs = append(errs, fmt.Errorf("restoring removed %s: %w", file, err))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// rollbackDeploy undeploys the given relative paths under modPath (reverse order).
// Returns the first Undeploy error encountered, if any.
// restoreReplacedOriginals is restoreReplacedOriginal over a set of
// relative paths - the rollback shape.
func (i *Installer) restoreReplacedOriginals(game *domain.Game, relativePaths []string) {
	if i.originals == nil {
		return
	}
	for _, rel := range relativePaths {
		i.restoreReplacedOriginal(rel, filepath.Join(game.ModPath, rel))
	}
}

func rollbackDeploy(lnk linker.Linker, modPath string, relativePaths []string) error {
	var firstErr error
	for j := len(relativePaths) - 1; j >= 0; j-- {
		dstPath := filepath.Join(modPath, relativePaths[j])
		if err := lnk.Undeploy(dstPath); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Uninstall removes a mod from the game directory, then prunes the
// directories its own removals emptied (#415 - only those; see
// linker.CleanupEmptyDirs).
//
// A path another game records is left as it is (#445 gate 2, G2-1) and
// reported on the flow's Warnings; the profile's record of it goes, as the
// recorded-only purge's PurgeKeptOtherGame drops it.
func (i *Installer) Uninstall(ctx context.Context, game *domain.Game, mod *domain.Mod, profileName string) error {
	removed, held, err := i.uninstall(ctx, game, mod, profileName)
	for _, h := range held {
		i.noteHeld(h.removalNote())
	}
	// Pruned even on failure: the paths in removed are gone either way, so
	// the directories they emptied are lmm's to tidy either way.
	linker.CleanupEmptyDirs(game.ModPath, removed)
	return err
}

// uninstall is Uninstall without the prune or the report, returning the
// paths (relative to game.ModPath) it actually removed and the ones it left
// for another game. purgeMods composes it so that a whole purge prunes
// ONCE, over its whole removal set, instead of per mod - which is what its
// single trailing CleanupEmptyDirs has always been - and reports the held
// paths as its own.
func (i *Installer) uninstall(ctx context.Context, game *domain.Game, mod *domain.Mod, profileName string) ([]string, []heldPath, error) {
	files, err := i.removalPaths(ctx, game, mod, profileName)
	if err != nil {
		return nil, nil, err
	}
	others, err := i.otherGamesFor(ctx, game)
	if err != nil {
		return nil, nil, err
	}
	// #451: rows recorded under another mod_path stay; only a purge knows
	// how to clear them.
	stranded, err := strandedRoots(ctx, i.db, game)
	if err != nil {
		return nil, nil, err
	}
	keepRoots := make([]string, len(stranded))
	for j, r := range stranded {
		keepRoots[j] = r.ModPath
	}

	jd := i.judgeFor(game, profileName, others, &cachedMod{cache: i.cache, game: game, mod: mod, method: i.linker.Method()})
	// keepPaths is the paths whose records stay: files this removal could
	// not judge (#466 review D3).
	var keepPaths []string

	// Undeploy each file
	var removed []string
	var held []heldPath
	for _, file := range files {
		select {
		case <-ctx.Done():
			return removed, held, ctx.Err()
		default:
		}

		dstPath := filepath.Join(game.ModPath, file)

		// #350: never delete a file lmm does not own.
		//
		// This loop undeploys every path the mod's cache entry NAMES,
		// which is not the same as every path the mod actually put there.
		// The copy and hardlink linkers remove whatever is at the path, so
		// a deploy (which undeploys before it installs), a purge or an
		// ordinary uninstall would DELETE stock game content sitting where
		// one of this mod's files would go - silently, and with no way
		// back. The symlink linker refuses to remove a non-symlink, which
		// is exactly why this went unnoticed: the default link method does
		// not have the bug.
		//
		// lmm's own deployments are unaffected: a symlink is not a regular
		// file, and a copy/hardlink deployment carries a deployed_files
		// row written by the same loop that created it - under the
		// DEPLOYING profile, which is why foreignFile asks the game-wide
		// question too (#404).
		if i.foreignFile(ctx, game, profileName, file, dstPath) {
			i.log.Debug("leaving a file this mod does not own where it is", "path", dstPath)
			continue
		}
		// #466: nor the user's - a copy or hardlink whose content is no
		// longer what lmm deployed. Asked before whether another game
		// records the file, so a changed one is reported, and kept, as the
		// user's (review D5).
		rc := i.keepOnRemoval(ctx, jd, file, dstPath)
		if rc.keep {
			held = append(held, rc.held)
			if rc.keepRecord {
				keepPaths = append(keepPaths, file)
			}
			continue
		}
		// #445 gate 2, G2-1: nor one another game records.
		if h, ok := heldElsewhere(others, file, dstPath); ok {
			held = append(held, h)
			continue
		}

		if err := i.linker.Undeploy(dstPath); err != nil {
			return removed, held, fmt.Errorf("undeploying %s: %w", file, err)
		}
		if rc.unverified {
			i.noteUnverified(file, rc.legacy)
		}
		removed = append(removed, file)
		// lmm's own file is gone; whatever it displaced goes back.
		i.restoreReplacedOriginal(file, dstPath)
	}

	// Remove file ownership records from database
	if i.db != nil {
		if err := i.db.DeleteDeployedFilesExcept(ctx, game.ID, profileName, mod.SourceID, mod.ID, keepRoots, keepPaths); err != nil {
			return removed, held, fmt.Errorf("removing file tracking: %w", err)
		}
	}

	return removed, held, nil
}

// removalPaths lists the paths, relative to game.ModPath, an uninstall of
// mod considers removing - the ONE list the removal loop walks and every
// removal preview (PlanUninstall, PlanPurge's merged artifact, deploy
// --purge) describes, so a dry run cannot name a file the real run leaves.
//
// It is the cache entry's full ListFiles union, not deployableFiles (#210):
// removal must cover anything that might ever have been linked, including
// stale unclaimed files a pre-fix deploy linked. Narrowing this would
// strand those links forever.
//
// An absent cache entry is not an error (#260): uninstall must stay
// idempotent when the entry is already gone - the steady state
// syncMergedPak's zero branch and purge --uninstall leave behind. The
// deployment can still be fully on disk, though (a copy/hardlink deploy
// owns real files, not links back into the cache), so the DB's tracked
// deployed paths stand in rather than orphaning them while erasing the only
// record that they were ours. Ownership rows upsert on overwrite ("new mod
// takes ownership"), so the fallback never names a path another mod has
// since claimed.
//
// Two narrowings follow. A member the adapter routes away from the linker
// is left out (notLinkerOwned). And when the game's adapter is refused
// (recordedOnly), only a path this profile's deployed_files rows name for
// the mod is kept: without the adapter, a listing is not proof.
func (i *Installer) removalPaths(ctx context.Context, game *domain.Game, mod *domain.Mod, profileName string) ([]string, error) {
	files, err := i.cache.ListFiles(game.ID, mod.SourceID, mod.ID, mod.Version)
	switch {
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("listing cached files: %w", err)
	case err != nil:
		files = nil
		if i.db == nil {
			break
		}
		if files, err = i.db.GetDeployedFilesForMod(ctx, game.ID, profileName, mod.SourceID, mod.ID); err != nil {
			return nil, fmt.Errorf("listing tracked deployed files: %w", err)
		}
	case i.recordedOnly:
		if files, err = i.onlyRecorded(ctx, game, mod, profileName, files); err != nil {
			return nil, err
		}
	}

	kept := files[:0:0]
	for _, file := range files {
		// #413: a member the adapter routed away from the linker was never
		// deployed as mod content, so an uninstall does not remove it.
		if !i.notLinkerOwned(game, file) {
			kept = append(kept, file)
		}
	}
	return kept, nil
}

// onlyRecorded keeps the entries of files that profileName's deployed_files
// rows name for mod. An Installer with no database has no proof of any.
func (i *Installer) onlyRecorded(ctx context.Context, game *domain.Game, mod *domain.Mod, profileName string, files []string) ([]string, error) {
	if i.db == nil {
		return nil, nil
	}
	rows, err := i.db.GetDeployedFilesForMod(ctx, game.ID, profileName, mod.SourceID, mod.ID)
	if err != nil {
		return nil, fmt.Errorf("listing tracked deployed files: %w", err)
	}
	recorded := make(map[string]bool, len(rows))
	for _, r := range rows {
		recorded[r] = true
	}
	var kept []string
	for _, f := range files {
		if recorded[filepath.ToSlash(f)] {
			kept = append(kept, f)
		}
	}
	return kept, nil
}

// IsInstalled checks if a mod is currently deployed. Returns true only if every
// cached file is deployed (partial installs report as not installed).
func (i *Installer) IsInstalled(ctx context.Context, game *domain.Game, mod *domain.Mod) (bool, error) {
	// Check if mod is cached first
	if !i.cache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
		return false, nil
	}

	// Get list of files in the cached mod
	files, err := deployableFiles(i.cache, i.adapter, game, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return false, fmt.Errorf("resolving deployable files: %w", err)
	}

	if len(files) == 0 {
		return false, nil
	}

	// Consider installed only if all files are deployed
	for _, file := range files {
		dstPath := filepath.Join(game.ModPath, file)
		deployed, err := i.linker.IsDeployed(dstPath)
		if err != nil {
			return false, err
		}
		if !deployed {
			return false, nil
		}
	}
	return true, nil
}

// Conflict represents a file that would be overwritten by installing a mod
type Conflict struct {
	RelativePath    string `json:"relative_path"`
	CurrentSourceID string `json:"current_source_id"`
	CurrentModID    string `json:"current_mod_id"`
}

// GetConflicts checks if installing a mod would overwrite files from other mods.
// Returns conflicts for files owned by OTHER mods (not the mod being installed).
//
// It stays EXPORTED with no in-tree caller outside this package: Task 19
// (#291) folded ImportArchive's own conflict check inline, which removed
// its last cmd/lmm caller, production and test alike. Unlike the
// Service-primitive ratchet elsewhere in this phase, this is one method on
// an exported type - package main still legitimately holds an *Installer
// via Service.GetInstaller (see that method's own doc comment for the
// precedent: cmd/lmm's tests build fixtures through it), so unexporting
// just this method would buy nothing but a fourth shim.
func (i *Installer) GetConflicts(ctx context.Context, game *domain.Game, mod *domain.Mod, profileName string) ([]Conflict, error) {
	if i.db == nil {
		return nil, nil
	}

	// Check if mod is cached
	if !i.cache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
		return nil, fmt.Errorf("mod not in cache: %s/%s@%s", mod.SourceID, mod.ID, mod.Version)
	}

	// Get list of files in the cached mod
	files, err := deployableFiles(i.cache, i.adapter, game, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return nil, fmt.Errorf("resolving deployable files: %w", err)
	}

	return i.conflictsForPaths(ctx, game, mod, profileName, files)
}

// conflictsForPaths is GetConflicts' twin for a caller that already knows the
// game-dir-relative paths in question rather than owning a cache entry to
// derive them from (#314): PlanImportArchive computes an archive's file list
// from its LISTING, so it can ask the conflict question before anything has
// been ingested. Same self-filter, same wrapping - only the source of the
// path list differs.
func (i *Installer) conflictsForPaths(ctx context.Context, game *domain.Game, mod *domain.Mod, profileName string, paths []string) ([]Conflict, error) {
	if i.db == nil {
		return nil, nil
	}

	dbConflicts, err := i.db.CheckFileConflicts(ctx, game.ID, profileName, paths)
	if err != nil {
		return nil, fmt.Errorf("checking conflicts: %w", err)
	}

	// Filter out conflicts with self (re-installing same mod)
	var conflicts []Conflict
	for _, c := range dbConflicts {
		if c.SourceID != mod.SourceID || c.ModID != mod.ID {
			conflicts = append(conflicts, Conflict{
				RelativePath:    c.RelativePath,
				CurrentSourceID: c.SourceID,
				CurrentModID:    c.ModID,
			})
		}
	}
	sortConflicts(conflicts)

	return conflicts, nil
}

// sortConflicts puts a conflict list in the one order every renderer wants
// (#315, Ruling 4's determinism rule): OWNING MOD first, path second. The
// DB answers ordered by path alone, which interleaves two owners' files -
// and every renderer groups per owning mod (the CLI's "From <mod> (<id>):"
// block, `--json`'s details.conflicts), so grouping a path-ordered list
// meant iterating a map, and the group order varied run to run. Sorting
// here means the group order falls out of the slice order, once, for every
// caller and every frontend.
func sortConflicts(conflicts []Conflict) {
	sort.Slice(conflicts, func(a, b int) bool {
		x, y := conflicts[a], conflicts[b]
		if x.CurrentSourceID != y.CurrentSourceID {
			return x.CurrentSourceID < y.CurrentSourceID
		}
		if x.CurrentModID != y.CurrentModID {
			return x.CurrentModID < y.CurrentModID
		}
		return x.RelativePath < y.RelativePath
	})
}

// GetDeployedFiles returns the list of files deployed for a mod
func (i *Installer) GetDeployedFiles(ctx context.Context, game *domain.Game, mod *domain.Mod) ([]string, error) {
	if !i.cache.Exists(game.ID, mod.SourceID, mod.ID, mod.Version) {
		return nil, nil
	}

	files, err := deployableFiles(i.cache, i.adapter, game, mod.SourceID, mod.ID, mod.Version)
	if err != nil {
		return nil, fmt.Errorf("resolving deployable files: %w", err)
	}

	var deployed []string
	for _, file := range files {
		dstPath := filepath.Join(game.ModPath, file)
		isDeployed, err := i.linker.IsDeployed(dstPath)
		if err != nil {
			i.log.Debug("checking deployed state failed", "path", dstPath, "err", err)
			continue
		}
		if isDeployed {
			deployed = append(deployed, file)
		}
	}

	return deployed, nil
}
