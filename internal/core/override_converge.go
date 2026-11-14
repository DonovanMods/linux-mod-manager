// Package core: override_converge.go is #631 - a profile switch and a
// profile apply converge the profile's overrides on files that are no
// mod's own (a stock game INI), as `lmm deploy` writes them, and both plans
// preview every override change they make, #630's overridden mod files
// included.
//
// The two directions:
//
//   - The live profile's overrides are written by deploy's own override
//     pass (applyProfileOverrides), held to the paths no mod deploys
//     (overrideEnv.nonModOnly): #630's convergence settles a mod's file.
//     The capture rule is deploy's - the game's file is copied into the
//     originals store first, first original wins.
//   - An override the live profile does not set any more goes back: the
//     original the originals store keeps for the path is put back, or -
//     where the override created the file - the file is removed. Only
//     lmm's own write is replaced (#445/#466's rule): the file must still
//     hold exactly what lmm last wrote there, which the originals manifest
//     records (overrideWrite) for every override written since #631. For
//     an override written before then, a file holding exactly some
//     profile's override for the path counts as lmm's; anything else is
//     the user's, and stays.
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
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"golang.org/x/sys/unix"
)

// OverrideChangeAction is what a switch, an apply or a deploy does to one
// file a profile override names (#631).
type OverrideChangeAction string

const (
	// OverridePlace writes the profile's override where the file holds no
	// override of lmm's: over the game's own file (kept first, as a
	// deploy keeps it), in a file of its own where nothing was, or over a
	// mod's own file, breaking its link (#630).
	OverridePlace OverrideChangeAction = "place"
	// OverrideUpdate rewrites a file that holds exactly the override lmm
	// last wrote there: another profile's, or this one's before it
	// changed.
	OverrideUpdate OverrideChangeAction = "update"
	// OverrideReplaceChanged writes the override over a file that changed
	// after lmm wrote an override there - the user's edit, or the game's
	// own rewrite of its INI. The changed file is kept first: set aside in
	// the originals store (named in the flow's warnings) when the game's
	// original is kept already, or kept as the original otherwise.
	OverrideReplaceChanged OverrideChangeAction = "replace_changed"
	// OverrideRestore takes away an override the profile no longer sets:
	// the game's original goes back, a file the override created is
	// removed, or a mod's own file is deployed again (#630).
	OverrideRestore OverrideChangeAction = "restore"
	// OverrideKeep is no change: an override the profile no longer sets,
	// whose file changed after lmm wrote it, is left as it is. The game's
	// original stays kept. Listed so every flow shows it the same way; it
	// never makes a plan one with changes.
	OverrideKeep OverrideChangeAction = "keep"
)

// OverrideChange is one file a switch, an apply or a deploy changes because
// the profile's overrides differ from what the game directory holds
// (#631): SwitchPlan.Overrides, ProfileApplyPlan.Overrides and
// DeployPlan.Overrides.
type OverrideChange struct {
	// Path is the file's path relative to Root, in slash form. For a
	// game file it is the override's own key.
	Path string `json:"path"`
	// Root is absent for the mod_path and "install_path" for a path under
	// the game's install_path - every game file an override names, and a
	// mod file deployed under the game root (#629).
	Root domain.DeployRoot `json:"root,omitzero"`
	// Action is what happens to the file.
	Action OverrideChangeAction `json:"action"`
	// SourceID and ModID name the mod whose own file the override
	// replaces (#630); both are absent for a game file.
	SourceID string `json:"source_id,omitempty"`
	ModID    string `json:"mod_id,omitempty"`
}

// overrideChanged reports whether changes holds a change - anything but a
// file kept as it is.
func overrideChanged(changes []OverrideChange) bool {
	return slices.ContainsFunc(changes, func(c OverrideChange) bool { return c.Action != OverrideKeep })
}

// overrideWrite is one file under a game's install_path a profile override
// last wrote, as the originals manifest records it (#631). It is recorded
// before the write (review nit: a crash between the two never leaves lmm's
// own content unrecorded, to be taken later for the game's original).
type overrideWrite struct {
	// RelativePath is the override's key, cleaned, in slash form.
	RelativePath string `json:"relative_path"`
	// SHA256 is the hex SHA-256 of what lmm writes.
	SHA256 string `json:"sha256"`
	// PrevSHA256 is lmm's own earlier write the file held when this one
	// was recorded, until this write is known to have landed: the file
	// holding either is lmm's.
	PrevSHA256 string `json:"prev_sha256,omitempty"`
	// Created marks a file the override created where the game had none:
	// taking the override away removes it.
	Created bool `json:"created,omitzero"`
	// CreatedDirs is the folders (slash form, relative to the install
	// path) the override created for a Created file, outermost first:
	// taking the override away removes each one left empty.
	CreatedDirs []string `json:"created_dirs,omitempty"`
}

// holds reports whether sum is lmm's write at the path.
func (w overrideWrite) holds(sum string) bool {
	return sum != "" && (sum == w.SHA256 || (w.PrevSHA256 != "" && sum == w.PrevSHA256))
}

// sha256Hex is content's hex SHA-256.
func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// editOverrideWrite applies edit to rel's record (nil when there is none;
// returning nil drops it) and writes the manifest when it changed. A nil
// store records nothing.
func (s *originalsStore) editOverrideWrite(rel string, edit func(*overrideWrite) *overrideWrite) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return err
	}
	i := slices.IndexFunc(m.OverrideWrites, func(o overrideWrite) bool { return o.RelativePath == rel })
	var cur *overrideWrite
	if i >= 0 {
		c := m.OverrideWrites[i]
		cur = &c
	}
	next := edit(cur)
	switch {
	case next == nil && i < 0:
		return nil
	case next == nil:
		m.OverrideWrites = slices.Delete(m.OverrideWrites, i, i+1)
	case i >= 0:
		if slices.Equal(next.CreatedDirs, m.OverrideWrites[i].CreatedDirs) &&
			next.SHA256 == m.OverrideWrites[i].SHA256 && next.PrevSHA256 == m.OverrideWrites[i].PrevSHA256 &&
			next.Created == m.OverrideWrites[i].Created {
			return nil
		}
		m.OverrideWrites[i] = *next
	default:
		next.RelativePath = rel
		m.OverrideWrites = append(m.OverrideWrites, *next)
		slices.SortFunc(m.OverrideWrites, func(a, b overrideWrite) int { return strings.Compare(a.RelativePath, b.RelativePath) })
	}
	return s.write(m)
}

// recordOverrideWrite records, BEFORE the write, that a profile override
// is about to write content at rel under the install_path. prev is lmm's
// own write the file holds now ("" when it holds none); created and dirs
// say the override makes the file, and which folders. An error refuses the
// write: unrecorded, lmm's content could later be taken for the game's.
func (s *originalsStore) recordOverrideWrite(rel string, content []byte, prev string, created bool, dirs []string) error {
	return s.editOverrideWrite(rel, func(*overrideWrite) *overrideWrite {
		return &overrideWrite{RelativePath: rel, SHA256: sha256Hex(content), PrevSHA256: prev, Created: created, CreatedDirs: dirs}
	})
}

// settleOverrideWrite drops the PrevSHA256 of rel's record once its write
// has landed. Best effort: a record left with it only answers for one more
// content.
func (s *originalsStore) settleOverrideWrite(rel string) {
	if err := s.editOverrideWrite(rel, func(cur *overrideWrite) *overrideWrite {
		if cur == nil {
			return nil
		}
		cur.PrevSHA256 = ""
		return cur
	}); err != nil {
		s.log.Debug("could not settle what a profile override wrote", "path", rel, "err", err)
	}
}

// forgetOverrideWrite drops rel's record, once the override is gone.
func (s *originalsStore) forgetOverrideWrite(rel string) {
	if err := s.editOverrideWrite(rel, func(*overrideWrite) *overrideWrite { return nil }); err != nil {
		s.log.Debug("could not forget what a profile override wrote", "path", rel, "err", err)
	}
}

// gameFile is what lmm knows of a file under the install_path a profile
// override names (#631): the single judgement the write, the restore and
// every plan make of it.
type gameFile struct {
	// sum is the file's SHA-256; "" when it is no regular file or
	// cannot be read.
	sum string
	// rec is what an override last wrote there, when hasRec.
	rec    overrideWrite
	hasRec bool
	// row is the game's original the store keeps, when hasRow.
	row    OriginalFile
	hasRow bool
	// own: the file holds what lmm wrote there - its record's content,
	// or, for a write from before records were kept, some profile's
	// override over an original lmm kept.
	own bool
	// stock: the file holds the original the store keeps.
	stock bool
}

// changed reports that lmm wrote an override at the path and the file no
// longer holds it, nor the game's original: the user's edit, or the
// game's own rewrite.
func (g gameFile) changed() bool {
	return g.sum != "" && !g.own && !g.stock && (g.hasRec || g.hasRow)
}

// judgeGameFile judges the regular file at path, rel under the install
// path, against rows and writes (overrideRecords). known is every profile's
// override checksums by path (overrideChecksums), the proof for a write
// made before records were kept; nil knows none.
func judgeGameFile(rel, path string, rows map[string]OriginalFile, writes map[string]overrideWrite, known map[string]map[string]bool) gameFile {
	var g gameFile
	g.row, g.hasRow = rows[rel]
	g.rec, g.hasRec = writes[rel]
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return g
	}
	sum, _, err := hashFile(path)
	if err != nil {
		return g
	}
	g.sum = sum
	g.stock = g.hasRow && sum == g.row.SHA256
	switch {
	case g.hasRec:
		g.own = g.rec.holds(sum)
	case g.hasRow:
		g.own = known[rel][sum]
	}
	return g
}

// setAsideFile is one file lmm set aside before an override replaced it,
// where the originals store already kept the game's own (#631 review): the
// user's changed file, kept where they can take it back.
type setAsideFile struct {
	// Root and RelativePath name the file it was.
	Root         OriginalRoot `json:"root"`
	RelativePath string       `json:"relative_path"`
	// Stored is where its bytes are, relative to the store's directory.
	Stored string `json:"stored"`
	// SHA256 and Size describe the bytes kept.
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	// SetAsideAt is when, and Profile the profile whose override replaced
	// it.
	SetAsideAt time.Time `json:"set_aside_at"`
	Profile    string    `json:"profile,omitempty"`
}

// setAsideDirName is the store's folder for set-aside files, beside files/:
// the same per-root layout, each name suffixed with when it was set aside,
// so a second one never replaces the first.
const setAsideDirName = "set-aside"

// setAside copies the file at path - rel under root - into the store's
// set-aside folder and records it, returning where it is. It is the user's
// only copy, so a failure refuses the write that would replace it.
func (s *originalsStore) setAside(root OriginalRoot, rel, path, profile string) (string, error) {
	if s == nil {
		return "", errors.New("lmm has nowhere to keep it")
	}
	rel, err := cleanOriginalRelPath(rel)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return "", err
	}
	at := time.Now().UTC().Truncate(time.Second)
	base := filepath.Join(s.storeDir(), setAsideDirName, string(root), filepath.FromSlash(rel)) + "." + at.Format("20060102T150405Z")
	dest := base
	for n := 2; ; n++ {
		if _, err := os.Lstat(dest); errors.Is(err, fs.ErrNotExist) {
			break
		}
		dest = fmt.Sprintf("%s-%d", base, n)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", err
	}
	sum, size, err := copyAndHash(path, dest)
	if err != nil {
		return "", err
	}
	stored, _ := filepath.Rel(s.storeDir(), dest)
	m.SetAside = append(m.SetAside, setAsideFile{
		Root: root, RelativePath: rel, Stored: filepath.ToSlash(stored),
		SHA256: sum, Size: size, SetAsideAt: at, Profile: profile,
	})
	if err := s.write(m); err != nil {
		_ = os.Remove(dest)
		return "", err
	}
	return dest, nil
}

// heldOriginal reports whether the store keeps an original of rel under the
// install_path. A store that cannot be read answers true: the file is then
// never taken for one an override created.
func (s *originalsStore) heldOriginal(rel string) bool {
	if s == nil {
		return false
	}
	held, err := s.holds(OriginalRootInstallPath, rel)
	return held || err != nil
}

// overrideRecords is what the store knows of the files profile overrides
// replaced under the install_path: the originals kept of them, and what
// each override last wrote.
func (s *originalsStore) overrideRecords() (map[string]OriginalFile, map[string]overrideWrite, error) {
	if s == nil {
		return nil, nil, nil
	}
	s.mu.Lock()
	m, err := s.read()
	s.mu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	rows := map[string]OriginalFile{}
	for _, row := range m.Originals {
		if row.Root == OriginalRootInstallPath && row.Op == OriginalOpProfileOverride {
			rows[row.RelativePath] = row
		}
	}
	writes := map[string]overrideWrite{}
	for _, w := range m.OverrideWrites {
		writes[w.RelativePath] = w
	}
	return rows, writes, nil
}

// noteOverrideCopy remembers that an overridden copy was written at rel, a
// mod's deployed path (#631). Best effort; a nil store remembers nothing.
func (s *originalsStore) noteOverrideCopy(rel domain.DeployPath) {
	s.editOverrideCopies(rel, true)
}

// forgetOverrideCopy drops rel from the overridden copies remembered, once
// the mod's own file is back.
func (s *originalsStore) forgetOverrideCopy(rel domain.DeployPath) {
	s.editOverrideCopies(rel, false)
}

// editOverrideCopies adds rel to the remembered overridden copies, or
// removes it.
func (s *originalsStore) editOverrideCopies(rel domain.DeployPath, add bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return
	}
	list := m.overrideCopyList(rel)
	p := filepath.ToSlash(filepath.Clean(rel.Rel))
	has := slices.Contains(*list, p)
	switch {
	case add && !has:
		*list = append(*list, p)
		slices.Sort(*list)
	case !add && has:
		*list = slices.DeleteFunc(*list, func(e string) bool { return e == p })
	default:
		return
	}
	if err := s.write(m); err != nil {
		s.log.Debug("could not record an overridden mod file", "path", p, "err", err)
	}
}

// overrideCopies is the overridden copies remembered, by deployed path. A
// store that cannot be read remembers none.
func (s *originalsStore) overrideCopies() map[domain.DeployPath]bool {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	m, err := s.read()
	s.mu.Unlock()
	if err != nil {
		return nil
	}
	out := map[domain.DeployPath]bool{}
	for _, p := range m.OverrideCopies {
		out[domain.ModPathFile(p)] = true
	}
	for _, p := range m.OverrideCopiesGameRoot {
		out[domain.GameRootFile(p)] = true
	}
	return out
}

// shareOverrideCopyRecord re-records every OTHER profile of game that
// records rel with fp, the fingerprint of the overridden copy profileName's
// override just put there (#631). The file on disk is one file, whichever
// profiles record it: a profile that shares the mod and is live later
// judges the path by its own record alone (deployedFormOf), and would
// otherwise find a copy it cannot tell from the user's - and leave the
// override in place after a switch to it. Best effort, logged.
func shareOverrideCopyRecord(ctx context.Context, database *db.DB, log *slog.Logger, game *domain.Game, profileName string, rel domain.DeployPath, fp *db.FileFingerprint) {
	if database == nil || fp == nil {
		return
	}
	states, err := currentStates(ctx, database, game, rel)
	if err != nil {
		log.Debug("could not read the records of an overridden mod file", "path", rel.String(), "err", err)
		return
	}
	for _, st := range states {
		if st.Profile == profileName {
			continue
		}
		if err := database.RecordDeployedFile(ctx, db.DeployedFileRecord{
			GameID: game.ID, Profile: st.Profile, Path: rel,
			SourceID: st.SourceID, ModID: st.ModID,
			ModPath: recordRoot(game), Fingerprint: fp,
		}); err != nil {
			log.Debug("could not record an overridden mod file for another profile", "path", rel.String(), "profile", st.Profile, "err", err)
		}
	}
}

// convergeProfileOverrides converges profileName's overrides in game after
// a switch or an apply made it live: #630's overridden mod files, then
// (#631) its overrides on game files, then the game files an override it
// no longer sets replaced. It returns the warnings, one per file.
//
// Like convergeOverriddenFiles it runs to the end on a context its
// caller's cancellation does not reach, and acts only for the game's live
// profile when its adapter allows a deploy.
func (s *Service) convergeProfileOverrides(ctx context.Context, game *domain.Game, profileName string) []string {
	warnings := s.convergeOverridesOnce(context.WithoutCancel(ctx), game, profileName)
	// What the passes above could not keep or put back is this flow's to
	// report: the flow drained the store before they ran, and the end of
	// the mutation forgets what is left (forgetKept).
	if store := s.originalsStoreFor(game.ID); store != nil {
		warnings = append(warnings, store.takeFailures()...)
	}
	return warnings
}

// convergeOverridesOnce is convergeProfileOverrides' work, before the
// drain.
func (s *Service) convergeOverridesOnce(ctx context.Context, game *domain.Game, profileName string) []string {
	warnings := s.convergeOverriddenFiles(ctx, game, profileName)
	if s.db == nil || game.InstallPath == "" || game.ModPath == "" {
		return warnings
	}
	// The same gates as convergeOverriddenFiles': the live profile only
	// (#462), and only when the adapter allows a deploy.
	if live, err := s.liveProfile(ctx, game.ID); err != nil || live != profileName {
		return warnings
	}
	if err := s.deployRefusal(ctx, game, profileName); err != nil {
		return warnings
	}
	profile, err := config.LoadProfile(s.configDir, game.ID, profileName)
	if err != nil {
		return append(warnings, fmt.Sprintf("applying profile overrides: %v", err))
	}
	env := s.overrideEnvFor(ctx, game, profileName)
	env.nonModOnly = true
	skipped, err := applyProfileOverrides(game, profile, env)
	if err != nil {
		skipped = append(skipped, err.Error())
	}
	for _, w := range skipped {
		warnings = append(warnings, fmt.Sprintf("applying profile overrides: %s", w))
	}
	_, restoreWarnings := s.restoreGameOverrides(ctx, game, profile, env, false)
	return append(warnings, restoreWarnings...)
}

// installBase is game's install_path made absolute and resolved through its
// symlinks: the base an override key is joined to, and the real directory
// everything written is held inside.
func installBase(game *domain.Game) (base, realBase string, err error) {
	if game.InstallPath == "" {
		return "", "", fmt.Errorf("game %q has no install path", game.ID)
	}
	base, err = filepath.Abs(game.InstallPath)
	if err != nil {
		return "", "", err
	}
	base = filepath.Clean(base)
	realBase, err = resolveExisting(base)
	return base, realBase, err
}

// realOverridePath is the real path the override key's file is at - its
// directory resolved through its symlinks - and that directory relative to
// realBase. ok is false for a key a deploy refuses, or one whose directory
// leaves the game directory.
func realOverridePath(base, realBase, key string) (realDest, dirRel string, ok bool) {
	dest, _, err := overrideDest(base, key)
	if err != nil {
		return "", "", false
	}
	realDir, err := resolveExisting(filepath.Dir(dest))
	if err != nil {
		return "", "", false
	}
	dirRel, ok = dirUnder(realBase, realDir)
	if !ok {
		return "", "", false
	}
	return filepath.Join(realDir, filepath.Base(dest)), dirRel, true
}

// restoreGameOverrides puts back, in game, every game file a profile
// override replaced or created that profile does not override (#631): the
// original the originals store keeps, or - for a file the override created
// - nothing. A file lmm cannot prove it wrote is the user's and stays,
// with a warning; a mod's file is #630's convergence's. With dryRun
// nothing is changed, and the changes say what would be.
func (s *Service) restoreGameOverrides(ctx context.Context, game *domain.Game, profile *domain.Profile, env overrideEnv, dryRun bool) ([]OverrideChange, []string) {
	store := env.originals
	if store == nil {
		return nil, nil
	}
	rows, writes, err := store.overrideRecords()
	if err != nil {
		return nil, []string{fmt.Sprintf("checking the files profile overrides replaced: %v", err)}
	}
	if len(rows) == 0 && len(writes) == 0 {
		return nil, nil
	}
	base, realBase, err := installBase(game)
	if err != nil {
		return nil, []string{fmt.Sprintf("checking the files profile overrides replaced: %v", err)}
	}
	mine := map[string]bool{}
	for key := range profile.Overrides {
		if rel, ok := overrideRel(key); ok {
			mine[rel] = true
		}
	}
	paths := make([]string, 0, len(rows)+len(writes))
	for rel := range rows {
		paths = append(paths, rel)
	}
	for rel := range writes {
		if _, dup := rows[rel]; !dup {
			paths = append(paths, rel)
		}
	}
	sort.Strings(paths)

	var known map[string]map[string]bool // lazily: every profile's override checksums, by path
	var changes []OverrideChange
	var warnings []string
	warn := func(rel, why string) {
		warnings = append(warnings, fmt.Sprintf("override %s: %s", rel, why))
	}
	keep := func(rel, why string) {
		changes = append(changes, OverrideChange{Path: rel, Root: domain.RootInstallPath, Action: OverrideKeep})
		warn(rel, why)
	}
	for _, rel := range paths {
		if mine[rel] {
			continue
		}
		realDest, dirRel, ok := realOverridePath(base, realBase, rel)
		if !ok {
			continue
		}
		if env.modPathFile != nil {
			if _, isMod := env.modPathFile(realDest); isMod {
				continue
			}
		}
		if _, hasRec := writes[rel]; !hasRec && known == nil {
			if env.known != nil {
				known = env.known()
			} else {
				known = s.overrideChecksums(game)
			}
		}
		g := judgeGameFile(rel, realDest, rows, writes, known)
		forget := func() {
			if !dryRun && g.hasRec {
				store.forgetOverrideWrite(rel)
			}
		}
		info, err := os.Lstat(realDest)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if !g.hasRow {
				// Created, and already gone: so are the records of it, and
				// the folders it left empty.
				if !dryRun {
					removeEmptyDirs(realBase, g.rec.CreatedDirs)
				}
				forget()
				continue
			}
		case err != nil:
			warn(rel, fmt.Sprintf("it could not be checked (%v), so lmm left it", err))
			continue
		case !info.Mode().IsRegular():
			keep(rel, "no profile override sets it any more, but it is no longer the file lmm wrote, so lmm left it")
			continue
		case g.sum == "":
			warn(rel, "it could not be read to check it, so lmm left it")
			continue
		case g.stock:
			// The game's own file is back already: nothing to do but let
			// the store go of it.
			if !dryRun {
				if _, err := store.release(OriginalRootInstallPath, rel, filepath.Join(base, filepath.FromSlash(rel))); err != nil {
					s.log.Debug("could not drop an original already back in place", "path", rel, "err", err)
				}
				forget()
			}
			continue
		case !g.own:
			keep(rel, "no profile override sets it any more, but it changed after lmm wrote the override, so lmm left your file (the game's original is still kept)")
			continue
		case !g.hasRow && !(g.hasRec && g.rec.Created):
			if g.hasRec {
				warn(rel, "no profile override sets it any more, but lmm kept no original of it to put back, so it left the file")
				forget()
			}
			continue
		}
		changes = append(changes, OverrideChange{Path: rel, Root: domain.RootInstallPath, Action: OverrideRestore})
		if dryRun {
			continue
		}
		if g.hasRow {
			if _, err := store.release(OriginalRootInstallPath, rel, filepath.Join(base, filepath.FromSlash(rel))); err != nil {
				warn(rel, fmt.Sprintf("the game's original could not be put back: %v", err))
				continue
			}
		} else if err := removeCreatedOverride(realBase, dirRel, realDest, g.rec); err != nil {
			warn(rel, fmt.Sprintf("the file the override created could not be removed: %v", err))
			continue
		}
		forget()
	}
	return changes, warnings
}

// overrideRel is an override key as every record keeps it: cleaned, in
// slash form, relative to the install path. ok is false for a key a write
// refuses.
func overrideRel(key string) (string, bool) {
	_, rel, err := overrideDest(string(filepath.Separator)+"game", key)
	if err != nil || rel == "." {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// removeCreatedOverride removes the file a profile override created at
// realDest, beneath realBase in dirRel, held by the no-follow walk, after
// checking once more that it holds what lmm wrote (rec) - then the
// folders the override created, each one left empty (#631 review).
func removeCreatedOverride(realBase, dirRel, realDest string, rec overrideWrite) error {
	dir, err := openDirBeneath(realBase, dirRel, false)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	name := filepath.Base(realDest)
	st, exists, err := lstatAt(dir, name)
	if err != nil {
		return err
	}
	if exists {
		if !isRegular(st) {
			return errors.New("it is no longer the file lmm wrote")
		}
		if got, _, err := hashFile(realDest); err != nil || !rec.holds(got) {
			return errors.New("it is no longer the file lmm wrote")
		}
		if err := syscall.Unlinkat(int(dir.Fd()), name); err != nil && !errors.Is(err, syscall.ENOENT) {
			return &os.PathError{Op: "remove", Path: realDest, Err: err}
		}
	}
	removeEmptyDirs(realBase, rec.CreatedDirs)
	return nil
}

// removeEmptyDirs removes each of dirs - folders an override created,
// slash form relative to realBase, outermost first - that is empty now,
// innermost first, through the no-follow walk. A folder that is not empty,
// or is no folder, stays, and so does every folder outside it.
func removeEmptyDirs(realBase string, dirs []string) {
	for i := len(dirs) - 1; i >= 0; i-- {
		d := filepath.FromSlash(dirs[i])
		if !filepath.IsLocal(d) {
			return
		}
		parent, err := openDirBeneath(realBase, filepath.Dir(d), false)
		if err != nil {
			return
		}
		err = unix.Unlinkat(int(parent.Fd()), filepath.Base(d), unix.AT_REMOVEDIR)
		_ = parent.Close()
		if err != nil && !errors.Is(err, unix.ENOENT) {
			return
		}
	}
}

// overrideChecksums is the checksum of every override some profile of game
// sets, by the override's cleaned key: the proof a file written by an
// override before #631 recorded its writes is still lmm's.
func (s *Service) overrideChecksums(game *domain.Game) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	names, err := config.ListProfiles(s.configDir, game.ID)
	if err != nil {
		return out
	}
	for _, name := range names {
		profile, err := config.LoadProfile(s.configDir, game.ID, name)
		if err != nil {
			continue
		}
		for key, content := range profile.Overrides {
			rel, err := cleanOriginalRelPath(key)
			if err != nil {
				continue
			}
			if out[rel] == nil {
				out[rel] = map[string]bool{}
			}
			out[rel][sha256Hex(content)] = true
		}
	}
	return out
}

// planGameOverrides is what writing profile's overrides on game files
// (#631) would change, judged as applyOneOverride judges each - without
// writing, or making a directory. A path a mod deploys is #630's, and an
// override the write would refuse is left out: the apply reports it.
func (s *Service) planGameOverrides(game *domain.Game, profile *domain.Profile, env overrideEnv) []OverrideChange {
	if len(profile.Overrides) == 0 {
		return nil
	}
	base, realBase, err := installBase(game)
	if err != nil {
		return nil
	}
	var changes []OverrideChange
	for key, content := range profile.Overrides {
		rel, ok := overrideRel(key)
		if !ok {
			continue
		}
		realDest, _, ok := realOverridePath(base, realBase, key)
		if !ok {
			continue
		}
		if env.modPathFile != nil {
			if _, isMod := env.modPathFile(realDest); isMod {
				continue
			}
		}
		action := OverridePlace
		info, err := os.Lstat(realDest)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			continue
		case info.Mode()&os.ModeSymlink != 0:
			if !linkStaysInside(realDest, realBase, env.cacheRoots) {
				continue
			}
		case !info.Mode().IsRegular():
			continue
		default:
			if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
				continue // refused: it shares its data with another file
			}
			if holdsContent(realDest, content) {
				continue
			}
			// applyOneOverride's own judgement of the file.
			g := env.judge(rel, realDest)
			switch {
			case g.own:
				action = OverrideUpdate
			case g.changed():
				action = OverrideReplaceChanged
			}
		}
		changes = append(changes, OverrideChange{Path: rel, Root: domain.RootInstallPath, Action: action})
	}
	return changes
}

// holdsContent reports whether the regular file at path holds exactly
// content, without following a link or blocking.
func holdsContent(path string, content []byte) bool {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return false
	}
	defer func() { _ = dir.Close() }()
	return sameContentAt(dir, filepath.Base(path), content)
}

// planOverrideChanges is every override change converging target's
// overrides in game makes once target is live (#631): #630's overridden mod
// files of the mods the plan leaves in place (skip names the ones it
// deploys or removes, by domain.ModKey), target's overrides on game files,
// and the game files an override target does not set replaced - in path
// order. Nothing for a game whose adapter refuses a deploy, where the
// apply converges nothing either.
//
// It is a preview: a game file a mod the plan installs or removes ships is
// judged by what is deployed now.
func (s *Service) planOverrideChanges(ctx context.Context, game *domain.Game, target *domain.Profile, skip map[string]bool) []OverrideChange {
	if s.db == nil || game.ModPath == "" || target == nil {
		return nil
	}
	if err := s.deployRefusal(ctx, game, target.Name); err != nil {
		return nil
	}
	var changes []OverrideChange
	steps, _, _ := s.overriddenSteps(ctx, game, target.Name, overriddenDeployed(game, target), skip)
	for _, st := range steps {
		if st.prune {
			continue
		}
		action := OverrideRestore
		switch {
		case st.content != nil && st.update:
			action = OverrideUpdate
		case st.content != nil:
			action = OverridePlace
		}
		changes = append(changes, OverrideChange{
			Path: relOf(st.row.Path), Root: rootOf(st.row.Path), Action: action,
			SourceID: st.row.SourceID, ModID: st.row.ModID,
		})
	}
	if game.InstallPath != "" {
		env := s.overrideEnvFor(ctx, game, target.Name)
		changes = append(changes, s.planGameOverrides(game, target, env)...)
		restores, _ := s.restoreGameOverrides(ctx, game, target, env, true)
		changes = append(changes, restores...)
	}
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Root != changes[j].Root {
			return changes[i].Root < changes[j].Root
		}
		return changes[i].Path < changes[j].Path
	})
	return changes
}

// restoreDroppedOverrides is `lmm deploy`'s half of the restore direction
// (#631, owner ruling): every game file an override profileName no longer
// sets replaced goes back, under restoreGameOverrides' rule. It returns the
// warnings, one per file.
func (s *Service) restoreDroppedOverrides(ctx context.Context, game *domain.Game, profileName string, profile *domain.Profile) []string {
	if game.InstallPath == "" {
		return nil
	}
	_, warnings := s.restoreGameOverrides(ctx, game, profile, s.overrideEnvFor(ctx, game, profileName), false)
	return warnings
}

// planDeployOverrides is the game files a deploy of profileName's override
// pass changes (#631): planGameOverrides and the restore direction, dry, in
// path order. A profile that cannot be read changes nothing here; the
// deploy reports it.
func (s *Service) planDeployOverrides(ctx context.Context, game *domain.Game, profileName string) []OverrideChange {
	if game.InstallPath == "" {
		return nil
	}
	profile, err := config.LoadProfile(s.configDir, game.ID, profileName)
	if err != nil {
		return nil
	}
	env := s.overrideEnvFor(ctx, game, profileName)
	changes := s.planGameOverrides(game, profile, env)
	restores, _ := s.restoreGameOverrides(ctx, game, profile, env, true)
	changes = append(changes, restores...)
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes
}

// overridesDigest is a digest of profile's overrides - every key and its
// content - which a switch or an apply plan records so its Apply refuses
// it once an override changed (#631 review): the plan's preview, and the
// files it would write, are those overrides'.
func overridesDigest(profile *domain.Profile) string {
	if profile == nil {
		return ""
	}
	keys := make([]string, 0, len(profile.Overrides))
	for k := range profile.Overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	h.Write([]byte("overrides\x00")) // never "", which means unrecorded
	for _, k := range keys {
		fmt.Fprintf(h, "%d:%s%d:", len(k), k, len(profile.Overrides[k]))
		h.Write(profile.Overrides[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// checkOverridesFresh returns a wrapped ErrStalePlan when profileName's
// overrides of gameID are no longer the ones a plan recorded (want); a plan
// that recorded none ("") is not checked.
func (s *Service) checkOverridesFresh(gameID, profileName, want string) error {
	if want == "" {
		return nil
	}
	profile, err := config.LoadProfile(s.configDir, gameID, profileName)
	if err != nil {
		return fmt.Errorf("loading profile %s: %w", profileName, err)
	}
	if overridesDigest(profile) != want {
		return fmt.Errorf("%w: profile %s's overrides changed since it was computed: %s/%s", ErrStalePlan, profileName, gameID, profileName)
	}
	return nil
}

// planSkipKeys is the domain.ModKey of every mod in the lists a plan
// deploys or removes.
func planSkipKeys(lists ...[]domain.InstalledMod) map[string]bool {
	out := map[string]bool{}
	for _, list := range lists {
		for _, im := range list {
			out[domain.ModKey(im.SourceID, im.ID)] = true
		}
	}
	return out
}
