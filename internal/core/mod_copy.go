// Package core: this file holds `lmm mod copy`'s core flow - PlanCopyMod/
// ApplyCopyMod (#437). A copy puts a mod one profile already has into
// another profile of the same game without re-installing it: the cache is
// keyed by game/source/mod/version with no profile in the path, so the
// bytes are shared already, and what a copy writes is the destination's
// profile ref and its installed row (copyRowToProfile). No source is ever
// asked anything. An active destination also gets the files deployed, by
// the destination's own link method; an inactive one is recorded only, and
// its next deploy or switch puts them in place.
package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ReasonExternalNoCopy explains why a Steam Workshop item is not copied: it
// is game-global, so every profile already has it while Steam keeps it.
const ReasonExternalNoCopy = "a Steam Workshop item is game-global, not per-profile - every profile already has it while Steam keeps it subscribed"

// CopyModRequest names what PlanCopyMod plans: the mod SourceID/ModID of
// profile From, copied into profile To - or, with All, every mod From
// lists (SourceID/ModID then ignored).
type CopyModRequest struct {
	From     string
	To       string
	SourceID string
	ModID    string
	All      bool
}

// CopyModEntry is one mod a CopyModPlan copies: the destination's ref will
// carry this Version and these FileIDs (the source row's - what the cache
// holds), and the source ref's Locked and Disabled markers.
type CopyModEntry struct {
	SourceID string   `json:"source_id"`
	ModID    string   `json:"mod_id"`
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	FileIDs  []string `json:"file_ids,omitempty"`
	// Locked reports that the source ref is locked (#97) and the copy
	// carries the lock: it is the user's own intent about that mod.
	Locked bool `json:"locked"`
	// Disabled reports that the source ref is marked off (#431): the copy
	// is recorded off too, and never deployed by the copy.
	Disabled bool `json:"disabled"`
	// AlreadyListed reports that the destination lists this mod already,
	// at ExistingVersion. ApplyCopyMod refuses it with ModAlreadyListedError
	// unless CopyModOptions.Replace is set - or, in an All copy, skips it.
	AlreadyListed   bool   `json:"already_listed"`
	ExistingVersion string `json:"existing_version,omitempty"`
}

// CopyModSkip is a mod a copy leaves out, with the reason as a sentence.
type CopyModSkip struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	Name     string `json:"name,omitempty"`
	Reason   string `json:"reason"`
}

// CopyModPlan is the pure, displayable result of PlanCopyMod - what
// `lmm mod copy` would do.
type CopyModPlan struct {
	GameID string `json:"game_id"`
	From   string `json:"from"`
	To     string `json:"to"`
	All    bool   `json:"all"`
	// Mods is what ApplyCopyMod copies, in the source profile's load order;
	// each is appended to the destination after its own refs, or replaced
	// in place when it is AlreadyListed and the caller says Replace.
	Mods []CopyModEntry `json:"mods"`
	// Skipped is what an All copy leaves out at plan time: an external
	// mod (ReasonExternalNoCopy), a ref with no installed row, or a row
	// whose bytes are not in the cache. Always empty for a single mod,
	// which refuses each of those instead.
	Skipped []CopyModSkip `json:"skipped,omitempty"`
	// DestinationActive is whether To is the game's active profile: the
	// copy deploys into the game directory when it is, and only records
	// when it is not.
	DestinationActive bool `json:"destination_active"`
	// LinkMethod is the destination's effective link method - what a
	// deploy into it uses and what its rows record; SourceLinkMethod is
	// From's, and LinkMethodDiffers says the two disagree.
	LinkMethod        domain.LinkMethod `json:"link_method"`
	SourceLinkMethod  domain.LinkMethod `json:"source_link_method"`
	LinkMethodDiffers bool              `json:"link_method_differs"`
	// NoChanges is true when there is nothing to copy at all.
	NoChanges bool `json:"no_changes"`

	// fromSnapshot/toSnapshot are the two profiles' installed state this
	// plan was computed against (Ruling 5).
	fromSnapshot installedSnapshot
	toSnapshot   installedSnapshot
}

// PlanCopyMod computes what `lmm mod copy` would do. Read-only, and never
// contacts a source. It refuses, before anything is planned: From == To,
// an unknown profile (domain.ErrProfileNotFound), a mod From has no row
// for (domain.ErrModNotFound), an external mod (ExternalModError with
// ReasonExternalNoCopy) and a mod whose bytes are not in the cache - an
// All copy skips the last three per mod instead.
func (s *Service) PlanCopyMod(ctx context.Context, game *domain.Game, req CopyModRequest) (*CopyModPlan, error) {
	if req.From == req.To {
		return nil, fmt.Errorf("cannot copy a mod from profile %q into itself", req.From)
	}
	pm := s.NewProfileManager()
	from, err := pm.Get(ctx, game.ID, req.From)
	if err != nil {
		return nil, err
	}
	to, err := pm.Get(ctx, game.ID, req.To)
	if err != nil {
		return nil, err
	}
	fromSnapshot, err := s.currentInstalledSnapshot(ctx, game.ID, req.From)
	if err != nil {
		return nil, err
	}
	toSnapshot, err := s.currentMarkedSnapshot(ctx, game.ID, req.To)
	if err != nil {
		return nil, err
	}
	fromRows, err := s.GetInstalledMods(ctx, game.ID, req.From)
	if err != nil {
		return nil, fmt.Errorf("loading installed mods: %w", err)
	}
	toRows, err := s.GetInstalledMods(ctx, game.ID, req.To)
	if err != nil {
		return nil, fmt.Errorf("loading installed mods: %w", err)
	}

	plan := &CopyModPlan{GameID: game.ID, From: req.From, To: req.To, All: req.All,
		fromSnapshot: fromSnapshot, toSnapshot: toSnapshot}
	if plan.LinkMethod, err = s.GetEffectiveLinkMethod(ctx, game, req.To); err != nil {
		return nil, err
	}
	if plan.SourceLinkMethod, err = s.GetEffectiveLinkMethod(ctx, game, req.From); err != nil {
		return nil, err
	}
	plan.LinkMethodDiffers = plan.LinkMethod != plan.SourceLinkMethod
	if plan.DestinationActive, err = s.copyDestinationActive(ctx, game.ID, req.To); err != nil {
		return nil, err
	}

	rows := make(map[string]domain.InstalledMod, len(fromRows))
	for _, r := range fromRows {
		rows[domain.ModKey(r.SourceID, r.ID)] = r
	}
	listed := make(map[string]string, len(to.Mods))
	for _, ref := range to.Mods {
		listed[domain.ModKey(ref.SourceID, ref.ModID)] = ref.Version
	}
	for _, r := range toRows {
		if _, ok := listed[domain.ModKey(r.SourceID, r.ID)]; !ok {
			listed[domain.ModKey(r.SourceID, r.ID)] = r.Version
		}
	}

	if !req.All {
		row, ok := rows[domain.ModKey(req.SourceID, req.ModID)]
		if !ok {
			return nil, fmt.Errorf("%w: %s in profile %s", domain.ErrModNotFound, domain.ModKey(req.SourceID, req.ModID), req.From)
		}
		ref := from.FindRef(req.SourceID, req.ModID)
		entry, skip := s.copyEntry(game, row, ref, listed)
		if skip != "" {
			if row.External {
				return nil, externalRefusal("copy", &row, ReasonExternalNoCopy)
			}
			return nil, errors.New(skip)
		}
		plan.Mods = []CopyModEntry{entry}
		return plan, nil
	}

	seen := make(map[string]bool, len(from.Mods))
	for i := range from.Mods {
		ref := &from.Mods[i]
		key := domain.ModKey(ref.SourceID, ref.ModID)
		if seen[key] {
			continue
		}
		seen[key] = true
		row, ok := rows[key]
		if !ok {
			plan.Skipped = append(plan.Skipped, CopyModSkip{SourceID: ref.SourceID, ModID: ref.ModID,
				Reason: fmt.Sprintf("not installed in profile %s", req.From)})
			continue
		}
		entry, skip := s.copyEntry(game, row, ref, listed)
		if skip != "" {
			plan.Skipped = append(plan.Skipped, CopyModSkip{SourceID: row.SourceID, ModID: row.ID, Name: row.Name, Reason: skip})
			continue
		}
		plan.Mods = append(plan.Mods, entry)
	}
	plan.NoChanges = len(plan.Mods) == 0
	return plan, nil
}

// copyEntry builds the plan entry for row (with its source ref, nil when
// the document does not list it), or returns why it cannot be copied.
func (s *Service) copyEntry(game *domain.Game, row domain.InstalledMod, ref *domain.ModReference, listed map[string]string) (CopyModEntry, string) {
	if row.External {
		return CopyModEntry{}, ReasonExternalNoCopy
	}
	if !s.GetGameCache(game).Exists(game.ID, row.SourceID, row.ID, row.Version) {
		return CopyModEntry{}, fmt.Sprintf("%s %s is not in the cache - reinstall it with 'lmm install --id %s' first", row.Name, row.Version, row.ID)
	}
	entry := CopyModEntry{SourceID: row.SourceID, ModID: row.ID, Name: row.Name, Version: row.Version, FileIDs: row.FileIDs}
	if ref != nil {
		entry.Locked = ref.Locked
		entry.Disabled = ref.Disabled
	}
	if v, ok := listed[domain.ModKey(row.SourceID, row.ID)]; ok {
		entry.AlreadyListed = true
		entry.ExistingVersion = v
	}
	return entry, ""
}

// copyDestinationActive reports whether profile is gameID's active one. A
// game whose active profile cannot be told is answered "not active": the
// copy then records only, which every reading of the game directory
// agrees is safe.
func (s *Service) copyDestinationActive(ctx context.Context, gameID, profile string) (bool, error) {
	live, err := s.liveProfile(ctx, gameID)
	if errors.Is(err, ErrActiveProfileUnknown) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return live == profile, nil
}

// CopyModOptions carries the decisions ApplyCopyMod cannot make itself
// (Ruling 1).
type CopyModOptions struct {
	// Replace overwrites a destination that already lists the mod: its ref
	// is replaced in place (keeping its load-order position) and its row
	// rewritten from the source's. Without it a single-mod copy refuses
	// with ModAlreadyListedError, and an All copy skips the mod.
	Replace bool
}

// ModAlreadyListedError is ApplyCopyMod's refusal of a single-mod copy into
// a profile that already lists the mod, returned before anything is
// written. The caller answers it by re-running ApplyCopyMod with
// CopyModOptions.Replace.
type ModAlreadyListedError struct {
	// Profile is the destination profile.
	Profile string
	// Mod names the mod, at the version the destination lists it.
	Mod domain.ModReference
	// ModName is the display name.
	ModName string
}

// Error names the mod, the profile and the version it has, and the remedy.
func (e *ModAlreadyListedError) Error() string {
	name := e.ModName
	if name == "" {
		name = domain.ModKey(e.Mod.SourceID, e.Mod.ModID)
	}
	return fmt.Sprintf("profile %s already lists %s (version %s) - copy with replace to overwrite it", e.Profile, name, e.Mod.Version)
}

// CopyModOutcome is one mod ApplyCopyMod copied.
type CopyModOutcome struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	// Deployed is true when the copy put the mod's files in the game
	// directory (an active destination, an enabled mod, a deploy that
	// succeeded).
	Deployed bool `json:"deployed"`
	// Replaced is true when the copy overwrote the destination's own
	// entry for the mod (CopyModOptions.Replace).
	Replaced bool `json:"replaced,omitzero"`
}

// CopyModResult reports the outcome of ApplyCopyMod.
type CopyModResult struct {
	From   string           `json:"from"`
	To     string           `json:"to"`
	Copied []CopyModOutcome `json:"copied"`
	// Skipped is the plan's own Skipped, then every AlreadyListed mod an All
	// copy without Replace left out.
	Skipped []CopyModSkip `json:"skipped,omitempty"`
	// Warnings holds diagnostics that must reach the user unconditionally -
	// a deploy that failed (the mod is recorded, and the next deploy puts
	// it in place), a checksum the copy could not carry, a merged-pak sync
	// problem - raw text, no prefix, each also emitted as a ModCopyWarning
	// event where it happens.
	Warnings []string `json:"warnings,omitempty"`
}

// ApplyCopyMod applies plan. For each mod, in order: the destination's row
// is written (copyRowToProfile - a fresh row with the destination's link
// method, enabled unless the source ref is disabled, not yet deployed) and
// then its profile ref, as one Ruling 16 pair (completeProfileWrite); then,
// for an active destination and an enabled mod, the files are deployed
// from the cache - the same Installer path `lmm mod enable` takes - and the
// row marked deployed. No hook runs: like an enable, a copy installs
// nothing.
//
// The plan is re-checked first (ErrStalePlan), and every AlreadyListed
// answer is re-derived from the destination's document as it is now. A
// single-mod copy of a listed mod without opts.Replace is
// ModAlreadyListedError before anything is written; an All copy skips it.
// sink may be nil.
func (s *Service) ApplyCopyMod(ctx context.Context, game *domain.Game, plan *CopyModPlan, opts CopyModOptions, sink EventSink) (*CopyModResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	result, err := s.applyCopyMod(ctx, game, plan, opts, sink)
	if result != nil {
		s.takeCaptureWarnings(game.ID, OpModCopy, ModCopyWarning, &result.Warnings, sink)
	}
	return result, err
}

func (s *Service) applyCopyMod(ctx context.Context, game *domain.Game, plan *CopyModPlan, opts CopyModOptions, sink EventSink) (*CopyModResult, error) {
	if err := s.checkPlanFresh(ctx, game.ID, plan.From, plan.fromSnapshot); err != nil {
		return nil, err
	}
	if err := s.checkPlanFresh(ctx, game.ID, plan.To, plan.toSnapshot); err != nil {
		return nil, err
	}
	emit := func(e Event) {
		if sink != nil {
			sink(e)
		}
	}
	pm := s.NewProfileManager()
	to, err := pm.Get(ctx, game.ID, plan.To)
	if err != nil {
		return nil, err
	}
	// #462: which profile is active decides whether the copy deploys, and
	// it is asked again here, under the gate, rather than trusted from the
	// plan. An active profile that cannot be told records only, as
	// copyDestinationActive answers for the plan.
	live, err := s.liveProfile(ctx, game.ID)
	if err != nil && !errors.Is(err, ErrActiveProfileUnknown) {
		return nil, err
	}
	active := err == nil && live == plan.To
	method, err := s.GetEffectiveLinkMethod(ctx, game, plan.To)
	if err != nil {
		return nil, err
	}

	// Every refusal before any write: a listed mod a single copy may not
	// replace.
	if !plan.All && !opts.Replace {
		for _, e := range plan.Mods {
			if existing := s.copyExisting(ctx, game.ID, to, e); existing != nil {
				return nil, &ModAlreadyListedError{Profile: plan.To, ModName: e.Name,
					Mod: domain.ModReference{SourceID: e.SourceID, ModID: e.ModID, Version: existing.Version}}
			}
		}
	}
	if active {
		// The deploy below writes into the game directory, so the game's
		// adapter has its say first, as it does for an enable (#413).
		if err := s.deployRefusal(ctx, game, plan.To); err != nil {
			return nil, err
		}
	}

	result := &CopyModResult{From: plan.From, To: plan.To, Skipped: append([]CopyModSkip(nil), plan.Skipped...)}
	warn := func(scope Scope, msg string) {
		result.Warnings = append(result.Warnings, msg)
		emit(StepEvent{Scope: scope, Phase: ModCopyWarning, Detail: msg})
	}
	var installer *Installer
	destInstaller := func() (*Installer, error) {
		if installer != nil {
			return installer, nil
		}
		var err error
		installer, err = s.getInstallerForProfile(ctx, game, plan.To)
		return installer, err
	}
	deployedAny := false

	for idx, e := range plan.Mods {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		scope := Scope{Op: OpModCopy, Index: idx + 1, Total: len(plan.Mods), ModName: e.Name,
			Mod: &domain.ModReference{SourceID: e.SourceID, ModID: e.ModID}}

		existing := s.copyExisting(ctx, game.ID, to, e)
		if existing != nil && !opts.Replace {
			skip := CopyModSkip{SourceID: e.SourceID, ModID: e.ModID, Name: e.Name,
				Reason: fmt.Sprintf("already in profile %s (version %s)", plan.To, existing.Version)}
			result.Skipped = append(result.Skipped, skip)
			emit(ModEvent{Scope: scope, Phase: ModCopySkipped, Detail: skip.Reason})
			continue
		}
		row, err := s.GetInstalledMod(ctx, e.SourceID, e.ModID, game.ID, plan.From)
		if err != nil {
			return result, fmt.Errorf("reading %s under %s: %w", e.Name, plan.From, err)
		}
		// The row being replaced, when its files are live: the deploy below
		// moves them to the copied version rather than linking beside them.
		var prior *domain.InstalledMod
		if existing != nil && existing.row != nil && existing.row.Deployed {
			prior = existing.row
		}

		ref := domain.ModReference{SourceID: e.SourceID, ModID: e.ModID, Version: e.Version,
			FileIDs: e.FileIDs, Locked: e.Locked, Disabled: e.Disabled}
		msgs, err := s.copyRowToProfile(ctx, game, *row, plan.To, rowCopy{enabled: !e.Disabled, method: method})
		if err != nil {
			return result, fmt.Errorf("recording %s under %s: %w", e.Name, plan.To, err)
		}
		for _, msg := range msgs {
			warn(scope, msg)
		}
		// Ruling 16: the row is written, so its ref is too, cancelled or not.
		if err := completeProfileWrite(ctx, func(ctx context.Context) error {
			return pm.putRef(ctx, game.ID, plan.To, ref)
		}); err != nil {
			return result, fmt.Errorf("recording %s in profile %s: %w", e.Name, plan.To, err)
		}

		outcome := CopyModOutcome{SourceID: e.SourceID, ModID: e.ModID, Name: e.Name, Version: e.Version, Replaced: existing != nil}
		if active && !e.Disabled {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			in, err := destInstaller()
			if err != nil {
				return result, err
			}
			if err := s.deployCopiedMod(ctx, game, in, prior, &row.Mod, plan.To); err != nil {
				warn(scope, fmt.Sprintf("could not deploy %s: %v - it is recorded in %s, and the next deploy puts it in place", e.Name, err, plan.To))
			} else {
				deployedAny = true
				outcome.Deployed = true
				if err := s.setModDeployed(ctx, e.SourceID, e.ModID, game.ID, plan.To, true); err != nil {
					warn(scope, fmt.Sprintf("could not mark %s as deployed: %v", e.Name, err))
				}
			}
		} else if prior != nil {
			// A replaced row whose files were live and are not any more
			// (the copy is disabled): nothing is left deployed for it.
			in, err := destInstaller()
			if err != nil {
				return result, err
			}
			if err := in.Uninstall(ctx, game, &prior.Mod, plan.To); err != nil {
				warn(scope, fmt.Sprintf("could not remove %s's previous files: %v", e.Name, err))
			}
		}
		result.Copied = append(result.Copied, outcome)
		emit(ModEvent{Scope: scope, Phase: ModCopyCopied})
	}

	if deployedAny {
		if syncWarnings, syncErr := s.syncMergedPak(ctx, game, plan.To); syncErr != nil {
			warn(Scope{Op: OpModCopy}, fmt.Sprintf("could not sync merged pak: %v", syncErr))
		} else {
			for _, w := range syncWarnings {
				warn(Scope{Op: OpModCopy}, w)
			}
		}
	}
	return result, nil
}

// copyDestination is what the destination already has for a copied mod:
// its ref's version (or its row's, when only a row names it), and its row.
type copyDestination struct {
	Version string
	row     *domain.InstalledMod
}

// copyExisting reports what profile to already has for e, or nil when it
// has neither a ref nor a row for it.
func (s *Service) copyExisting(ctx context.Context, gameID string, to *domain.Profile, e CopyModEntry) *copyDestination {
	var dest *copyDestination
	if ref := to.FindRef(e.SourceID, e.ModID); ref != nil {
		dest = &copyDestination{Version: ref.Version}
	}
	if row, err := s.GetInstalledMod(ctx, e.SourceID, e.ModID, gameID, to.Name); err == nil {
		if dest == nil {
			dest = &copyDestination{Version: row.Version}
		}
		dest.row = row
	}
	return dest
}

// deployCopiedMod links mod's cached files into the game directory for
// profile. A replaced row whose files are live (prior) is moved to mod's
// version with Replace when its own cache entry is still there to tell its
// files apart, and otherwise removed first; a same-version prior is
// re-linked by Install over its own files.
func (s *Service) deployCopiedMod(ctx context.Context, game *domain.Game, installer *Installer, prior *domain.InstalledMod, mod *domain.Mod, profile string) error {
	if prior != nil && prior.Version != mod.Version {
		if s.GetGameCache(game).Exists(game.ID, prior.SourceID, prior.ID, prior.Version) {
			return installer.Replace(ctx, game, &prior.Mod, mod, profile)
		}
		if err := installer.Uninstall(ctx, game, &prior.Mod, profile); err != nil {
			return err
		}
	}
	return installer.Install(ctx, game, mod, profile)
}

// putRef writes ref into profileName's document whole - version, file IDs,
// lock and disabled markers - replacing every ref for the same mod in place
// or appending it when the profile lists none. It is a mod copy's write
// (#437): unlike UpsertMod, the ref it writes is the user's explicit copy
// of another profile's, markers included, so a locked destination ref is
// replaced rather than refused.
func (pm *ProfileManager) putRef(ctx context.Context, gameID, profileName string, ref domain.ModReference) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	profile, err := pm.Get(ctx, gameID, profileName)
	if err != nil {
		return err
	}
	found := false
	for i := range profile.Mods {
		if profile.Mods[i].SourceID == ref.SourceID && profile.Mods[i].ModID == ref.ModID {
			profile.Mods[i] = ref
			found = true
		}
	}
	if !found {
		profile.Mods = append(profile.Mods, ref)
	}
	return pm.save(profile)
}
