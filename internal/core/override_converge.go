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
	"syscall"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// OverrideChangeAction is what a switch or an apply does to one file a
// profile override names (#631).
type OverrideChangeAction string

const (
	// OverridePlace writes the profile's override where the file holds no
	// override of lmm's: over the game's own file (kept first, as a
	// deploy keeps it), in a file of its own where nothing was, or over a
	// mod's own file, breaking its link (#630).
	OverridePlace OverrideChangeAction = "place"
	// OverrideUpdate rewrites a file that holds another override lmm
	// wrote: another profile's, or this one's before it changed.
	OverrideUpdate OverrideChangeAction = "update"
	// OverrideRestore takes away an override the profile no longer sets:
	// the game's original goes back, a file the override created is
	// removed, or a mod's own file is deployed again (#630).
	OverrideRestore OverrideChangeAction = "restore"
)

// OverrideChange is one file a switch or an apply changes because the
// profile's overrides differ from what the game directory holds (#631):
// SwitchPlan.Overrides and ProfileApplyPlan.Overrides.
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

// overrideWrite is one file under a game's install_path a profile override
// last wrote, as the originals manifest records it (#631).
type overrideWrite struct {
	// RelativePath is the override's key, cleaned, in slash form.
	RelativePath string `json:"relative_path"`
	// SHA256 is the hex SHA-256 of what lmm wrote.
	SHA256 string `json:"sha256"`
	// Created marks a file the override created where the game had none:
	// taking the override away removes it.
	Created bool `json:"created,omitzero"`
}

// sha256Hex is content's hex SHA-256.
func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// recordOverrideWrite records that a profile override wrote content at
// rel, under the install_path. Best effort, as markKeptUser is: a record
// that cannot be written costs only the later restore, which then leaves
// the file as one lmm cannot vouch for. A nil store records nothing.
func (s *originalsStore) recordOverrideWrite(rel string, content []byte, created bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return
	}
	w := overrideWrite{RelativePath: rel, SHA256: sha256Hex(content), Created: created}
	if i := slices.IndexFunc(m.OverrideWrites, func(o overrideWrite) bool { return o.RelativePath == rel }); i >= 0 {
		if m.OverrideWrites[i] == w {
			return
		}
		m.OverrideWrites[i] = w
	} else {
		m.OverrideWrites = append(m.OverrideWrites, w)
		slices.SortFunc(m.OverrideWrites, func(a, b overrideWrite) int {
			switch {
			case a.RelativePath < b.RelativePath:
				return -1
			case a.RelativePath > b.RelativePath:
				return 1
			}
			return 0
		})
	}
	if err := s.write(m); err != nil {
		s.log.Debug("could not record what a profile override wrote", "path", rel, "err", err)
	}
}

// forgetOverrideWrite drops rel's record, once the override is gone.
func (s *originalsStore) forgetOverrideWrite(rel string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return
	}
	n := len(m.OverrideWrites)
	m.OverrideWrites = slices.DeleteFunc(m.OverrideWrites, func(o overrideWrite) bool { return o.RelativePath == rel })
	if len(m.OverrideWrites) == n {
		return
	}
	if err := s.write(m); err != nil {
		s.log.Debug("could not forget what a profile override wrote", "path", rel, "err", err)
	}
}

// ownOverrideWrite reports whether the regular file at path - rel under the
// install_path - holds exactly what a profile override last wrote there,
// and that record.
func (s *originalsStore) ownOverrideWrite(rel, path string) (overrideWrite, bool) {
	if s == nil {
		return overrideWrite{}, false
	}
	s.mu.Lock()
	m, err := s.read()
	s.mu.Unlock()
	if err != nil {
		return overrideWrite{}, false
	}
	i := slices.IndexFunc(m.OverrideWrites, func(o overrideWrite) bool { return o.RelativePath == rel })
	if i < 0 {
		return overrideWrite{}, false
	}
	sum, _, err := hashFile(path)
	return m.OverrideWrites[i], err == nil && sum == m.OverrideWrites[i].SHA256
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
		if rel, err := cleanOriginalRelPath(key); err == nil {
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
		row, hasRow := rows[rel]
		w, hasWrite := writes[rel]
		forget := func() {
			if !dryRun && hasWrite {
				store.forgetOverrideWrite(rel)
			}
		}
		info, err := os.Lstat(realDest)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if !hasRow {
				forget() // created, and already gone
				continue
			}
		case err != nil:
			warn(rel, fmt.Sprintf("it could not be checked (%v), so lmm left it", err))
			continue
		case !info.Mode().IsRegular():
			warn(rel, "no profile override sets it any more, but it is no longer the file lmm wrote, so lmm left it")
			continue
		default:
			sum, _, err := hashFile(realDest)
			if err != nil {
				warn(rel, fmt.Sprintf("it could not be checked (%v), so lmm left it", err))
				continue
			}
			if hasRow && sum == row.SHA256 {
				// The game's own file is back already: nothing to do but
				// let the store go of it.
				if !dryRun {
					if _, err := store.release(OriginalRootInstallPath, rel, filepath.Join(base, filepath.FromSlash(rel))); err != nil {
						s.log.Debug("could not drop an original already back in place", "path", rel, "err", err)
					}
					forget()
				}
				continue
			}
			var ours bool
			if hasWrite {
				ours = sum == w.SHA256
			} else {
				if known == nil {
					known = s.overrideChecksums(game)
				}
				ours = known[rel][sum]
			}
			if !ours {
				warn(rel, "no profile override sets it any more, but it changed after lmm wrote the override, so lmm left your file (the game's original is still kept)")
				continue
			}
			if !hasRow && (!hasWrite || !w.Created) {
				if hasWrite {
					warn(rel, "no profile override sets it any more, but lmm kept no original of it to put back, so it left the file")
					forget()
				}
				continue
			}
		}
		changes = append(changes, OverrideChange{Path: rel, Root: domain.RootInstallPath, Action: OverrideRestore})
		if dryRun {
			continue
		}
		if hasRow {
			if _, err := store.release(OriginalRootInstallPath, rel, filepath.Join(base, filepath.FromSlash(rel))); err != nil {
				warn(rel, fmt.Sprintf("the game's original could not be put back: %v", err))
				continue
			}
		} else if err := removeCreatedOverride(realBase, dirRel, realDest, w.SHA256); err != nil {
			warn(rel, fmt.Sprintf("the file the override created could not be removed: %v", err))
			continue
		}
		forget()
	}
	return changes, warnings
}

// removeCreatedOverride removes the file a profile override created at
// realDest, beneath realBase in dirRel, held by the no-follow walk, after
// checking once more that it holds what lmm wrote (sum).
func removeCreatedOverride(realBase, dirRel, realDest, sum string) error {
	dir, err := openDirBeneath(realBase, dirRel, false)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	name := filepath.Base(realDest)
	st, exists, err := lstatAt(dir, name)
	if err != nil || !exists {
		return err
	}
	if !isRegular(st) {
		return errors.New("it is no longer the file lmm wrote")
	}
	if got, _, err := hashFile(realDest); err != nil || got != sum {
		return errors.New("it is no longer the file lmm wrote")
	}
	if err := syscall.Unlinkat(int(dir.Fd()), name); err != nil && !errors.Is(err, syscall.ENOENT) {
		return &os.PathError{Op: "remove", Path: realDest, Err: err}
	}
	return nil
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
	rows, writes, _ := env.originals.overrideRecords()
	var changes []OverrideChange
	for key, content := range profile.Overrides {
		rel, err := cleanOriginalRelPath(key)
		if err != nil {
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
		}
		action := OverridePlace
		if _, ok := writes[rel]; ok && err == nil {
			action = OverrideUpdate
		} else if _, ok := rows[rel]; ok && err == nil {
			action = OverrideUpdate
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
