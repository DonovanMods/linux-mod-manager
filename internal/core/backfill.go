package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// profileBackfillPendingPrefix prefixes the db_meta key of a profile whose
// share of the backfill could not be written yet (see pendingProfile). The
// whole obligation lives under db.MetaProfileDisabledBackfill itself, so one
// prefix query answers "is anything still owed?".
const profileBackfillPendingPrefix = db.MetaProfileDisabledBackfill + ":"

// ProfileBackfillReport is what one run of the #431 profile-document
// backfill did: every reference it marked, and every profile file it could
// not edit (kept, and retried once that file changes).
type ProfileBackfillReport struct {
	Marked  []ProfileBackfillMark
	Skipped []ProfileBackfillSkip
}

// ProfileBackfillMark is one reference the backfill wrote `disabled: true`
// onto: the mod, the profile it belongs to, and the file that now says so.
type ProfileBackfillMark struct {
	GameID   string
	Profile  string
	File     string
	SourceID string
	ModID    string
	Name     string
}

// ProfileBackfillSkip is one profile file the backfill could not edit, with
// the display names of the mods it would have marked there and why it could
// not.
type ProfileBackfillSkip struct {
	GameID  string
	Profile string
	File    string
	Mods    []string
	Err     error
}

// pendingProfile is the db_meta record of one profile whose share of the
// backfill is still owed: the rows it was captured with - frozen, because
// the rows themselves are rewritten by the new binary's own flows and would
// not say the same thing later - and the fingerprint of the file as it was
// when the write last failed, so it is retried only once something about
// that file has changed.
type pendingProfile struct {
	GameID      string       `json:"game_id"`
	Profile     string       `json:"profile"`
	Fingerprint string       `json:"fingerprint"`
	NeedsActive bool         `json:"needs_active,omitempty"`
	Mods        []pendingMod `json:"mods"`
}

type pendingMod struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	Name     string `json:"name"`
}

func pendingProfileKey(gameID, profile string) string {
	return profileBackfillPendingPrefix + gameID + "/" + profile
}

// BackfillProfileDisabledMarkers runs #431's one-time profile-document
// backfill if it is owed, and returns what it did (nil when nothing was
// owed or nothing could be retried yet). The same report is printed on the
// Service's WarnWriter.
//
// WHAT IT IS FOR. A mod disabled before the `disabled:` marker existed
// recorded that intent in installed_mods and nowhere else. Every converge
// flow reads the profile document, so without this the first `lmm profile
// switch` or `lmm profile apply` after the upgrade would switch such a mod
// back on, and `lmm profile sync` would delete its reference outright.
// Teaching the plans to prefer the row over an unmarked reference would
// invert the invariant the design rests on (a missing `disabled` key means
// enabled) and make ProfileApplyPlan.ToEnable unreachable; recording the
// intent once leaves every flow's reading of the document as documented.
//
// WHAT IT MARKS - AND WHY SO LITTLE. The row is weak evidence. A false
// marker is the expensive mistake: the next switch into that profile leaves
// the mod undeployed, and the user never asked for that. A missed marker
// only reproduces the pre-upgrade behaviour, which one `lmm mod disable`
// then fixes for good. So a row is marked only when nothing else lmm does
// could have left it that way:
//
//   - under the game's ACTIVE profile - the one profile file that says
//     `is_default: true` (none, or several, is no answer: GetDefault's
//     first-profile fallback is a guess). Every profile switch writes
//     enabled = 0 onto the profile it switches AWAY from, so a disabled row
//     anywhere else is at least as likely to be a mod the user wants on;
//     and `lmm purge` (or `deploy --purge`, or a failed `verify --fix`
//     re-link) followed by a switch leaves exactly the (0, 0) a real
//     disable does. Non-active profiles are never marked.
//   - with enabled = 0 AND deployed = 0, what `lmm mod disable` has written
//     since #183 (v1.28.0). A row at (0, 1) under the active profile is
//     what the #430 switch bug leaves for a mod that is live, and what a
//     failed enable during the switch into it leaves; it is also a
//     pre-v1.28 disable, and that is the accepted cost.
//   - not EXTERNAL (#269): lmm cannot switch a Steam Workshop item off.
//
// A reference the document does not list is left alone (it has no
// desired-state entry to mark), as is one already marked.
//
// WHEN IT RUNS. migrateV17 records the obligation, once, when an older
// lmm's database holds a candidate row, so a fresh installation never owes
// it. It is discharged inside the mutation slot: here, at app.Open, and -
// because the new binary's own flows write the very flag values it reads
// (`lmm purge` clears deployed on every row) - at the start of whichever
// mutation reaches beginOp first. Everything before the slot is a read of
// db_meta, so an installation that owes nothing takes no lock; the rows are
// read only once the slot is held, so a change another process committed
// while this one waited is the one acted on.
//
// HOW IT WRITES. config.MarkModsDisabled inserts the marker into the file's
// own bytes - the file the profile was read from - so comments, `~/` paths
// and layout survive; SaveProfile's round trip would have rewritten them
// from a read-only command.
//
// WHAT IF A FILE CANNOT BE WRITTEN. That profile's share is kept in db_meta
// with the rows it was captured with, reported by file, and retried when
// the file (or its directory) changes - every other profile is still
// backfilled, and an unchanged file costs an ordinary open a stat and no
// lock. Its rows are frozen evidence, and any enable written since
// supersedes one (supersedePendingProfileBackfill). A profile file that will
// not even parse is not the active profile as far as lmm can tell, so it is
// kept only when no readable profile is the explicit default, and marked on
// repair only if it then turns out to be the one.
//
// RESIDUAL (by design). The obligation is discharged by a durable marker,
// so it is never re-derived: a disable made after that - by an older lmm
// still running across the upgrade (usually `lmm serve`), or after a
// downgrade and a re-upgrade - is not recorded, and neither is a marker lost
// by restoring an older copy of a profile file. Each is one `lmm mod
// disable` away from being recorded.
func (s *Service) BackfillProfileDisabledMarkers(ctx context.Context) (*ProfileBackfillReport, error) {
	if !s.backfillPending.Load() {
		return nil, nil
	}
	keys, err := s.db.MetaWithPrefix(ctx, db.MetaProfileDisabledBackfill)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		s.backfillPending.Store(false)
		return nil, nil
	}
	_, owed := keys[db.MetaProfileDisabledBackfill]
	if !owed && !s.anyPendingProfileChanged(keys) {
		return nil, nil
	}

	release, err := s.acquireOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.dischargeProfileBackfill(ctx)
}

// dischargeProfileBackfill is the backfill's work, run with the mutation
// slot held: the whole obligation if it is still owed, then every pending
// profile whose file has changed. It prints its report.
func (s *Service) dischargeProfileBackfill(ctx context.Context) (*ProfileBackfillReport, error) {
	keys, err := s.db.MetaWithPrefix(ctx, db.MetaProfileDisabledBackfill)
	if err != nil {
		return nil, err
	}
	report := &ProfileBackfillReport{}
	// Printed on the way out whatever happens: a marker already written
	// before a failure is still one the user has to hear about, and a
	// retry would skip it as already marked.
	defer s.printProfileBackfillReport(report)
	if _, owed := keys[db.MetaProfileDisabledBackfill]; owed {
		if err := s.captureProfileBackfill(ctx, report); err != nil {
			return report, err
		}
	}
	for key, value := range sortedMeta(keys) {
		if !strings.HasPrefix(key, profileBackfillPendingPrefix) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if err := s.retryPendingProfile(ctx, key, value, report); err != nil {
			return report, err
		}
	}

	left, err := s.db.MetaWithPrefix(ctx, db.MetaProfileDisabledBackfill)
	if err != nil {
		return report, err
	}
	if len(left) == 0 {
		s.backfillPending.Store(false)
	}
	return report, nil
}

// captureProfileBackfill is the one-time pass: read the rows, decide which
// may be marked, mark them, keep what could not be written, and discharge
// the obligation.
func (s *Service) captureProfileBackfill(ctx context.Context, report *ProfileBackfillReport) error {
	if s.beforeProfileBackfillScan != nil {
		s.beforeProfileBackfillScan()
	}
	rows, err := s.db.DisabledModRows(ctx)
	if err != nil {
		return err
	}

	// Candidate rows, grouped by game and then by profile; DisabledModRows
	// orders by (game, profile), so each group is contiguous.
	byGame := make(map[string]map[string][]pendingMod)
	var games []string
	for _, row := range rows {
		if row.External || row.Deployed {
			continue
		}
		profiles, ok := byGame[row.GameID]
		if !ok {
			profiles = make(map[string][]pendingMod)
			byGame[row.GameID] = profiles
			games = append(games, row.GameID)
		}
		profiles[row.ProfileName] = append(profiles[row.ProfileName], pendingMod{SourceID: row.SourceID, ModID: row.ModID, Name: row.Name})
	}

	for _, gameID := range games {
		if err := ctx.Err(); err != nil {
			return err
		}
		profiles := byGame[gameID]
		flagged, unreadable, err := s.explicitDefaults(gameID)
		if err != nil {
			// The profiles directory itself could not be read, so any of
			// these profiles might be the active one.
			flagged, unreadable = nil, make(map[string]error, len(profiles))
			for name := range profiles {
				unreadable[name] = err
			}
		}
		switch {
		case len(flagged) == 1:
			if mods, ok := profiles[flagged[0]]; ok {
				if err := s.markPendingProfile(ctx, pendingProfile{GameID: gameID, Profile: flagged[0], Mods: mods}, "", report); err != nil {
					return err
				}
			}
		case len(flagged) == 0:
			// No readable profile is the explicit default, so an unreadable
			// one might be. Its rows are kept, frozen, until it can be read.
			for _, name := range slices.Sorted(maps.Keys(unreadable)) {
				mods, ok := profiles[name]
				if !ok {
					continue
				}
				pending := pendingProfile{GameID: gameID, Profile: name, NeedsActive: true, Mods: mods}
				if err := s.keepPendingProfile(ctx, pending, report, unreadable[name]); err != nil {
					return err
				}
			}
		}
	}
	return s.db.DeleteMeta(ctx, db.MetaProfileDisabledBackfill)
}

// explicitDefaults returns the profiles of gameID - by FILE name, the name
// every row and every load uses - whose file says `is_default: true`, and
// why each file that could not be read at all could not be.
func (s *Service) explicitDefaults(gameID string) (flagged []string, unreadable map[string]error, err error) {
	names, err := config.ListProfiles(s.configDir, gameID)
	if err != nil {
		return nil, nil, err
	}
	slices.Sort(names)
	unreadable = make(map[string]error)
	for _, name := range names {
		profile, err := config.LoadProfile(s.configDir, gameID, name)
		if err != nil {
			unreadable[name] = err
			continue
		}
		if profile.IsDefault {
			flagged = append(flagged, name)
		}
	}
	return flagged, unreadable, nil
}

// retryPendingProfile retries one kept profile if its file has changed since
// the last attempt, and drops it once there is nothing left to do.
func (s *Service) retryPendingProfile(ctx context.Context, key, value string, report *ProfileBackfillReport) error {
	var pending pendingProfile
	if err := json.Unmarshal([]byte(value), &pending); err != nil {
		s.logger().Warn("profile backfill: dropping an unreadable pending record", "key", key, "error", err)
		return s.db.DeleteMeta(ctx, key)
	}
	path, err := config.ProfilePath(s.configDir, pending.GameID, pending.Profile)
	if err != nil {
		return s.db.DeleteMeta(ctx, key)
	}
	fingerprint, exists := fileFingerprint(path)
	if !exists {
		// The profile is gone; if it comes back it is a different document.
		return s.db.DeleteMeta(ctx, key)
	}
	if fingerprint == pending.Fingerprint {
		return nil
	}

	if pending.NeedsActive {
		if _, err := config.LoadProfile(s.configDir, pending.GameID, pending.Profile); err != nil {
			return s.keepPendingProfile(ctx, pending, report, err)
		}
		flagged, _, err := s.explicitDefaults(pending.GameID)
		if err != nil || len(flagged) != 1 || flagged[0] != pending.Profile {
			// Readable now, and not the game's one explicit default: under
			// the rule, nothing in it is marked.
			return s.db.DeleteMeta(ctx, key)
		}
		pending.NeedsActive = false
	}

	// Frozen evidence, re-checked against what is true now: a row that is
	// gone, or that says enabled, no longer asks for a marker.
	current := make([]pendingMod, 0, len(pending.Mods))
	for _, mod := range pending.Mods {
		row, err := s.db.GetInstalledMod(ctx, mod.SourceID, mod.ModID, pending.GameID, pending.Profile)
		if err != nil {
			if errors.Is(err, domain.ErrModNotFound) {
				continue
			}
			return err
		}
		if !row.Enabled && !row.External {
			current = append(current, mod)
		}
	}
	pending.Mods = current
	return s.markPendingProfile(ctx, pending, key, report)
}

// markPendingProfile writes pending's markers into its profile file. On
// success the profile's pending record (key, when it has one) is dropped;
// on failure the profile is kept for a retry.
func (s *Service) markPendingProfile(ctx context.Context, pending pendingProfile, key string, report *ProfileBackfillReport) error {
	path, err := config.ProfilePath(s.configDir, pending.GameID, pending.Profile)
	if err != nil {
		return err
	}
	if len(pending.Mods) > 0 {
		refs := make([]domain.ModReference, 0, len(pending.Mods))
		names := make(map[string]string, len(pending.Mods))
		for _, mod := range pending.Mods {
			refs = append(refs, domain.ModReference{SourceID: mod.SourceID, ModID: mod.ModID})
			names[domain.ModKey(mod.SourceID, mod.ModID)] = mod.Name
		}
		marked, err := config.MarkModsDisabled(path, refs)
		switch {
		case errors.Is(err, domain.ErrProfileNotFound):
			// Deleted since it was listed: nothing to mark.
		case err != nil:
			return s.keepPendingProfile(ctx, pending, report, err)
		}
		for _, ref := range marked {
			report.Marked = append(report.Marked, ProfileBackfillMark{
				GameID: pending.GameID, Profile: pending.Profile, File: path,
				SourceID: ref.SourceID, ModID: ref.ModID, Name: names[domain.ModKey(ref.SourceID, ref.ModID)],
			})
		}
	}
	if key == "" {
		key = pendingProfileKey(pending.GameID, pending.Profile)
	}
	return s.db.DeleteMeta(ctx, key)
}

// keepPendingProfile records pending for a retry once its file changes, and
// reports why it could not be written now.
func (s *Service) keepPendingProfile(ctx context.Context, pending pendingProfile, report *ProfileBackfillReport, cause error) error {
	path, err := config.ProfilePath(s.configDir, pending.GameID, pending.Profile)
	if err != nil {
		return err
	}
	pending.Fingerprint, _ = fileFingerprint(path)
	value, err := json.Marshal(pending)
	if err != nil {
		return fmt.Errorf("recording the profile backfill still owed for %s: %w", path, err)
	}
	if err := s.db.SetMeta(ctx, pendingProfileKey(pending.GameID, pending.Profile), string(value)); err != nil {
		return err
	}
	names := make([]string, 0, len(pending.Mods))
	for _, mod := range pending.Mods {
		names = append(names, mod.Name)
	}
	report.Skipped = append(report.Skipped, ProfileBackfillSkip{
		GameID: pending.GameID, Profile: pending.Profile, File: path, Mods: names, Err: cause,
	})
	return nil
}

// anyPendingProfileChanged reports whether any kept profile's file differs
// from when its write last failed - the one thing that makes a retry worth
// taking the mutation lock for. A read of the files only.
func (s *Service) anyPendingProfileChanged(keys map[string]string) bool {
	for key, value := range keys {
		if !strings.HasPrefix(key, profileBackfillPendingPrefix) {
			continue
		}
		var pending pendingProfile
		if err := json.Unmarshal([]byte(value), &pending); err != nil {
			return true // the retry drops it
		}
		path, err := config.ProfilePath(s.configDir, pending.GameID, pending.Profile)
		if err != nil {
			return true
		}
		if fingerprint, _ := fileFingerprint(path); fingerprint != pending.Fingerprint {
			return true
		}
	}
	return false
}

// fileFingerprint identifies what a write to path depends on: the file's
// bytes, mode and modification time, where a symlink points, and the mode
// of the directory the atomic write creates its temporary file in. Any of
// them changing is a reason to try again. exists is false when path is
// absent altogether.
func fileFingerprint(path string) (fingerprint string, exists bool) {
	if _, err := os.Lstat(path); err != nil {
		return "", !errors.Is(err, os.ErrNotExist)
	}
	h := sha256.New()
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		_, _ = fmt.Fprintf(h, "unresolved:%v", err)
		return hex.EncodeToString(h.Sum(nil)), true
	}
	_, _ = fmt.Fprintf(h, "target:%s\n", target)
	if info, err := os.Stat(target); err == nil {
		_, _ = fmt.Fprintf(h, "mode:%s mtime:%d\n", info.Mode(), info.ModTime().UnixNano())
	}
	if info, err := os.Stat(filepath.Dir(target)); err == nil {
		_, _ = fmt.Fprintf(h, "dir:%s\n", info.Mode())
	}
	if file, err := os.Open(target); err == nil {
		_, _ = io.Copy(h, file)
		_ = file.Close()
	} else {
		_, _ = fmt.Fprintf(h, "open:%v", err)
	}
	return hex.EncodeToString(h.Sum(nil)), true
}

// supersedePendingProfileBackfill drops sourceID/modID from gameID/profile's
// kept backfill record, if it has one: the row was just written enabled,
// which is newer intent than the evidence the record froze. Called from the
// two writers every enable goes through (setModEnabled, saveInstalledMod),
// and free when nothing is pending. A failure to update the record drops it
// whole - losing a marker is the safe direction.
func (s *Service) supersedePendingProfileBackfill(ctx context.Context, gameID, profile, sourceID, modID string) {
	if !s.backfillPending.Load() {
		return
	}
	// The record must be updated even when the enable's own ctx is being
	// cancelled: the row write it follows has already happened.
	ctx = context.WithoutCancel(ctx)
	key := pendingProfileKey(gameID, profile)
	value, err := s.db.GetMeta(ctx, key)
	if err != nil {
		s.logger().Warn("profile backfill: could not read a record an enable may supersede; dropping it", "key", key, "error", err)
		_ = s.db.DeleteMeta(ctx, key)
		return
	}
	if value == "" {
		return
	}
	var pending pendingProfile
	if err := json.Unmarshal([]byte(value), &pending); err != nil {
		_ = s.db.DeleteMeta(ctx, key)
		return
	}
	i := slices.IndexFunc(pending.Mods, func(m pendingMod) bool {
		return m.SourceID == sourceID && m.ModID == modID
	})
	if i < 0 {
		return
	}
	pending.Mods = slices.Delete(pending.Mods, i, i+1)
	if len(pending.Mods) == 0 {
		if err := s.db.DeleteMeta(ctx, key); err != nil {
			s.logger().Warn("profile backfill: could not drop a superseded record", "key", key, "error", err)
		}
		return
	}
	updated, err := json.Marshal(pending)
	if err == nil {
		err = s.db.SetMeta(ctx, key, string(updated))
	}
	if err != nil {
		s.logger().Warn("profile backfill: could not update a superseded record; dropping it", "key", key, "error", err)
		_ = s.db.DeleteMeta(ctx, key)
	}
}

// printProfileBackfillReport writes report on the Service's always-on user
// channel: every mod marked, by game, profile, name and file, then the
// command that switches each one back on - so a marker lmm got wrong is
// seen when it happens and undone with one command, rather than discovered
// later as a missing mod - and every profile file that was kept for later.
func (s *Service) printProfileBackfillReport(report *ProfileBackfillReport) {
	if s.warnWriter == nil || report == nil {
		return
	}
	w := s.warnWriter
	for _, skip := range report.Skipped {
		_, _ = fmt.Fprintf(w, "warning: could not record %d mod(s) disabled before this upgrade in %s (game %s, profile %s): %v\n",
			len(skip.Mods), skip.File, skip.GameID, skip.Profile, skip.Err)
		_, _ = fmt.Fprintf(w, "  not recorded: %s - lmm looks at that file again once it changes\n", strings.Join(skip.Mods, ", "))
	}
	if len(report.Marked) == 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "recorded %d mod(s) disabled before this upgrade as `disabled: true` in their profile files (a one-time step):\n", len(report.Marked))
	for _, m := range report.Marked {
		_, _ = fmt.Fprintf(w, "  game %s, profile %s: %s (%s) in %s\n", m.GameID, m.Profile, m.Name, domain.ModKey(m.SourceID, m.ModID), m.File)
	}
	_, _ = fmt.Fprintln(w, "If any of them should be on, switch it back on with:")
	for _, m := range report.Marked {
		_, _ = fmt.Fprintf(w, "  lmm mod enable %s --game %s --source %s --profile %s\n", m.ModID, m.GameID, m.SourceID, m.Profile)
	}
}

// sortedMeta iterates keys in key order, so a run's report is deterministic.
func sortedMeta(keys map[string]string) func(yield func(string, string) bool) {
	return func(yield func(string, string) bool) {
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		slices.Sort(names)
		for _, k := range names {
			if !yield(k, keys[k]) {
				return
			}
		}
	}
}
