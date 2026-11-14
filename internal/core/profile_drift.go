package core

// profile_drift.go is the active profile's drift summary (#641): what `lmm
// profile apply` would converge, counted by reason. It is the apply plan's
// own comparison (diffProfileApply, planOverrideChanges), stopped before the
// plan resolves anything against a source - so GameStatus, which the web UI
// polls, can carry it, and the Profile card can offer Apply exactly when
// the plan would have something to do.

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// ProfileDrift is how the active profile differs from what is installed
// under it (#641): the mods (and override files) `lmm profile apply` would
// change, counted by reason. Every mod is counted once, under the first of
// its reasons in the order the fields are listed. GameStatus.ProfileDrift
// carries it, absent when there is no drift.
type ProfileDrift struct {
	// Profile is the profile the counts are for: the game's active one.
	Profile string `json:"profile"`
	// NotInstalled counts listed mods (not switched off) with no install
	// under the profile.
	NotInstalled int `json:"not_installed,omitzero"`
	// Version counts mods installed at another version than the profile
	// pins (a hand edit, a collection update).
	Version int `json:"version,omitzero"`
	// ModType counts mods installed as another mod type than the profile
	// states (#607).
	ModType int `json:"mod_type,omitzero"`
	// Files counts mods installed from another set of files than the
	// profile pins, at the same version (#639).
	Files int `json:"files,omitzero"`
	// Fomod counts mods whose installed tree is not the one the profile's
	// FOMOD choices or tree inputs (a file list, patches) build (#614,
	// #610).
	Fomod int `json:"fomod,omitzero"`
	// NotEnabled counts mods the profile lists and switches on that are
	// installed but disabled.
	NotEnabled int `json:"not_enabled,omitzero"`
	// Unlisted counts mods enabled under the profile that it does not list.
	Unlisted int `json:"unlisted,omitzero"`
	// SwitchedOff counts mods the profile lists with `disabled: true`
	// (#431) that are still enabled or deployed.
	SwitchedOff int `json:"switched_off,omitzero"`
	// Overrides counts the files the profile's overrides would change
	// (#631): ProfileApplyPlan.Overrides, less the ones kept as they are.
	Overrides int `json:"overrides,omitzero"`
}

// Total is every change the drift counts: mods and override files.
func (d ProfileDrift) Total() int {
	return d.NotInstalled + d.Version + d.ModType + d.Files + d.Fomod + d.NotEnabled + d.Unlisted + d.SwitchedOff + d.Overrides
}

// countReinstall counts a mod the apply reinstalls under its first reason:
// version, mod type, files, else its FOMOD tree.
func (d *ProfileDrift) countReinstall(ref domain.ModReference, im *domain.InstalledMod) {
	switch {
	case ref.Version != "" && im.Version != ref.Version:
		d.Version++
	case im.ModType != ref.ModType:
		d.ModType++
	case fileSetDrift(ref, im):
		d.Files++
	default:
		d.Fomod++
	}
}

// profileDriftOf is game's active profile's drift summary (#641), or nil
// when an apply would change nothing - or could not run: no profile is
// active, or the game's mod_path moved (both refuse the apply plan). It
// reads the profile, the installed rows and the originals manifest, and
// asks no source; see diffProfileApply. GameStatus computes the same from
// what it has read already (profileDrift).
func (s *Service) profileDriftOf(ctx context.Context, game *domain.Game) (*ProfileDrift, error) {
	pm := s.NewProfileManager()
	profiles, err := pm.List(ctx, game.ID)
	if err != nil {
		return nil, err
	}
	active := activeOf(profiles)
	if active == nil {
		return nil, nil
	}
	mods, err := s.GetInstalledMods(ctx, game.ID, active.Name)
	if err != nil {
		return nil, err
	}
	return s.profileDrift(ctx, game, pm, active, profiles, mods, false)
}

// activeOf is the profile GetDefault picks from profiles: the default one,
// else the first; nil for none.
func activeOf(profiles []*domain.Profile) *domain.Profile {
	for _, p := range profiles {
		if p.IsDefault {
			return p
		}
	}
	if len(profiles) > 0 {
		return profiles[0]
	}
	return nil
}

// profileDrift is profileDriftOf over what the caller has read already:
// every profile of game, the active one among them, and its installed rows.
// modPathFine says the caller found no ModPathProblem, which asks the
// moved-mod_path question first - GameStatus has, so the query is not
// made twice.
func (s *Service) profileDrift(ctx context.Context, game *domain.Game, pm *ProfileManager, active *domain.Profile, profiles []*domain.Profile, mods []domain.InstalledMod, modPathFine bool) (*ProfileDrift, error) {
	// The two refusals PlanProfileApply makes before it compares anything:
	// an apply that would be refused has no drift to offer.
	if live, err := s.liveProfile(ctx, game.ID); err != nil || live != active.Name {
		return nil, ctx.Err()
	}
	if !modPathFine {
		if err := s.refuseModPathMoved(ctx, game.ID); err != nil {
			return nil, ctx.Err()
		}
	}
	plan, drift := s.diffProfileApply(ctx, pm, game, active.Name, active, mods)
	drift.Overrides = s.overrideDrift(ctx, game, active, plan, profiles)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if drift.Total() == 0 {
		return nil, nil
	}
	return &drift, nil
}

// overridesInPlay reports whether an override can change anything in game
// for profile, the drift summary's gate (#641): when it is false the
// summary counts no override change without looking at a file, where
// planOverrideChanges - which the apply plan always runs - would walk the
// deployed tree under symlink and hardlink to find nothing. Every override
// change starts from one of these: planGameOverrides from the profile's own
// overrides, restoreGameOverrides from the originals store's records, and
// overriddenSteps from the profile's overrides, another profile's, a copy
// the store recorded, or - under symlink and hardlink - a deployed record
// carrying a fingerprint, which a plain link never has: an overridden copy
// whose note in the store is missing (the manifest deleted, a note that
// failed to write, a copy from before #631 recorded them) is still found by
// it. Anything that cannot be read counts as in play: the full comparison
// then decides.
func (s *Service) overridesInPlay(ctx context.Context, game *domain.Game, profile *domain.Profile, profiles []*domain.Profile) bool {
	if len(profile.Overrides) > 0 {
		return true
	}
	if profiles == nil {
		names, err := config.ListProfiles(s.configDir, game.ID)
		if err != nil {
			return true
		}
		for _, name := range names {
			p, err := config.LoadProfile(s.configDir, game.ID, name)
			if err != nil {
				continue // overriddenElsewhere skips it too
			}
			profiles = append(profiles, p)
		}
	}
	for _, p := range profiles {
		if len(p.Overrides) > 0 {
			return true
		}
	}
	if s.originalsStoreFor(game.ID).holdsOverrideRecords() {
		return true
	}
	if s.db == nil {
		return false
	}
	// overriddenSteps' linked walk: the profile's own method decides it.
	method, err := s.GetEffectiveLinkMethod(ctx, game, profile.Name)
	if err != nil {
		return true
	}
	if method == domain.LinkCopy {
		return false
	}
	found, err := s.db.HasFingerprintedLinkedFiles(ctx, game.ID, profile.Name, method)
	return err != nil || found
}

// holdsOverrideRecords reports whether the manifest records anything an
// override did (overridesInPlay): a game file one replaced, a write, or an
// overridden copy. A manifest that cannot be read does, as far as anyone
// can tell; a nil store (no data dir) records nothing.
func (s *originalsStore) holdsOverrideRecords() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	m, err := s.read()
	s.mu.Unlock()
	if err != nil {
		return true
	}
	if len(m.OverrideWrites) > 0 || len(m.OverrideCopies) > 0 || len(m.OverrideCopiesGameRoot) > 0 {
		return true
	}
	for _, row := range m.Originals {
		if row.Op == OriginalOpProfileOverride {
			return true
		}
	}
	return false
}

// overrideDrift is ProfileDrift.Overrides: the changes planOverrideChanges
// finds - the apply plan's own override comparison - less the files kept as
// they are, or none when overridesInPlay says none can exist. With overrides in play that
// comparison looks at the game directory - one lstat per deployed file
// under symlink and hardlink - so its answer is memoised on
// overrideFingerprint, and a status poll that changed nothing is answered
// without the walk.
func (s *Service) overrideDrift(ctx context.Context, game *domain.Game, profile *domain.Profile, plan *ProfileApplyPlan, profiles []*domain.Profile) int {
	if !s.overridesInPlay(ctx, game, profile, profiles) {
		return 0
	}
	skip := applySkipKeys(plan)
	key := game.ID + "\x00" + profile.Name
	fingerprint, gen := s.overrideFingerprint(game, skip)
	if n, ok := s.overrideMemoLookup(key, fingerprint); ok {
		return n
	}
	n := 0
	for _, c := range s.planOverrideChanges(ctx, game, profile, skip) {
		if c.Action != OverrideKeep {
			n++
		}
	}
	s.overrideMemoStore(key, fingerprint, gen, n)
	return n
}

// overrideMemoEntry is one memoised override count and the fingerprint of
// the state it was counted from.
type overrideMemoEntry struct {
	fingerprint string
	count       int
}

// overrideMemoLookup returns the count memoised under key for fingerprint.
// An empty fingerprint never matches.
func (s *Service) overrideMemoLookup(key, fingerprint string) (int, bool) {
	if fingerprint == "" {
		return 0, false
	}
	s.overrideMemoMu.Lock()
	defer s.overrideMemoMu.Unlock()
	entry, ok := s.overrideMemo[key]
	if !ok || entry.fingerprint != fingerprint {
		return 0, false
	}
	return entry.count, true
}

// overrideMemoStore memoises count under key for fingerprint, taken at
// mutation generation gen - unless a mutation of this process is running,
// or began while the count was taken: the count then saw a game directory
// in the middle of changing, and the mutation's end bumps nothing a later
// fingerprint would see.
func (s *Service) overrideMemoStore(key, fingerprint string, gen uint64, count int) {
	if fingerprint == "" || s.opHeld.Load() || s.currentOpGen() != gen {
		return
	}
	s.overrideMemoMu.Lock()
	defer s.overrideMemoMu.Unlock()
	if s.overrideMemo == nil {
		s.overrideMemo = map[string]overrideMemoEntry{}
	}
	s.overrideMemo[key] = overrideMemoEntry{fingerprint: fingerprint, count: count}
}

// currentOpGen is the count of mutations begun in this process (opGen).
func (s *Service) currentOpGen() uint64 {
	s.verifyMemoMu.Lock()
	defer s.verifyMemoMu.Unlock()
	return s.opGen
}

// overrideFingerprint is a cheap summary of what the override comparison
// depends on, from stats only - no file is read: this process's mutation
// generation (beginOp bumps it), the game's paths and link method, the
// size, modification time, inode and change time of every profile file of
// the game (their overrides), of the originals manifest (what overrides
// wrote and replaced), of the database and its WAL (deployed files,
// written by this process or another lmm beside it) and of the operation
// lock (another lmm's mutation), and the mods the plan deploys or removes
// (skip). It returns "" - memoise nothing - while a mutation of
// this process runs.
//
// KNOWN LIMIT: a file of the game directory changed by hand (an override's
// file edited, a deployed file replaced) moves none of these, so the
// memoised count stands until the next mutation or edit of the profile;
// opening the Apply plan always compares afresh.
func (s *Service) overrideFingerprint(game *domain.Game, skip map[string]bool) (string, uint64) {
	gen := s.currentOpGen()
	if s.opHeld.Load() {
		return "", gen
	}
	var b strings.Builder
	fmt.Fprintf(&b, "gen=%d\x00mod=%s\x00install=%s\x00link=%s\x00", gen, game.ModPath, game.InstallPath, game.LinkMethod)
	stat := func(label, path string) {
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(&b, "%s=-\x00", label)
			return
		}
		fmt.Fprintf(&b, "%s=%d:%d", label, info.Size(), info.ModTime().UnixNano())
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			// A file replaced by a rename, or restored with its old
			// mtime, still moves its inode or its ctime.
			fmt.Fprintf(&b, ":%d:%d:%d", st.Ino, st.Ctim.Sec, st.Ctim.Nsec)
		}
		b.WriteString("\x00")
	}
	profilesDir := filepath.Join(s.configDir, "games", game.ID, "profiles")
	if entries, err := os.ReadDir(profilesDir); err == nil {
		for _, e := range entries {
			stat("profile:"+e.Name(), filepath.Join(profilesDir, e.Name()))
		}
	}
	if store := s.originalsStoreFor(game.ID); store != nil {
		stat("manifest", store.manifestPath())
	}
	if s.dataDir != "" {
		stat("db", filepath.Join(s.dataDir, "lmm.db"))
		stat("wal", filepath.Join(s.dataDir, "lmm.db-wal"))
	}
	if s.opLockPath != "" {
		// Another lmm's mutation stamps the lock file as it takes it
		// (probeOpLock reads the same stamp); stat only, never a flock.
		stat("oplock", s.opLockPath)
	}
	for _, k := range slices.Sorted(maps.Keys(skip)) {
		fmt.Fprintf(&b, "skip=%s\x00", k)
	}
	return b.String(), gen
}

// applySkipKeys is the mods plan deploys or removes, by domain.ModKey:
// planOverrideChanges' skip.
func applySkipKeys(plan *ProfileApplyPlan) map[string]bool {
	skip := planSkipKeys(plan.ToDisable, plan.ToEnable)
	for _, entry := range plan.ToInstall {
		skip[domain.ModKey(entry.Ref.SourceID, entry.Ref.ModID)] = true
	}
	return skip
}
