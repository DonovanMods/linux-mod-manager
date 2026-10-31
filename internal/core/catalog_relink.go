// Package core: this file holds the automatic re-link of a mod whose source
// gave it a new ID (#590).
//
// A source that declares its IDs unstable (source.UnstableIDSource: Icarus,
// whose catalog sync deletes a mod and re-adds it under a new Firestore ID)
// loses an installed mod from its catalog routinely, and the mod is still
// there under its new ID. When the update check found exactly one mod in
// that source with the gone mod's name and author (catalogSuccessors), the
// re-link `lmm mod edit --to-source-id` would make is made on its own, by
// the pass a frontend runs after a check - the same place, and under the
// same rules, as the automatic update pass (#587, mod_auto_update.go):
//
//   - it never queues: the mutation slot is taken now or not at all
//     (BeginAutoRelink), and a check that finds it busy leaves the work to
//     the next one;
//   - it never runs under a running game, whose merged pak a re-link may
//     rebuild;
//   - it only ever touches the game's active profile;
//   - a locked mod is never re-linked: the lock is the user saying "leave
//     it", so it keeps the suggestion and its Re-link button.
//
// Anything less certain - several candidates (a current and an older copy
// among them), a name or author that matches only once folded, a version
// missing or not comparable, an older copy, no candidate, a source whose
// IDs are stable - stays the suggestion it was.
package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// CatalogRelinked is one automatic re-link (#590): the mod that left its
// source's catalog, the ID it was re-linked to, and the sentence every
// frontend prints for it.
type CatalogRelinked struct {
	SourceID  string `json:"source_id"`
	FromModID string `json:"from_mod_id"`
	ToModID   string `json:"to_mod_id"`
	Name      string `json:"name,omitempty"`
	// Message is "<name> moved to a new ID in <source>; re-linked (<old>
	// -> <new>)".
	Message string `json:"message"`
	// Warnings are the re-link's own (RelinkResult.Warnings): a metadata
	// fetch that failed, a merged-pak resync's complaint. omitempty.
	Warnings []string `json:"warnings,omitempty"`
}

// CatalogAutoRelink is the automatic re-link's state on an update check's
// report (UpdateCheckReport.AutoRelink, #590): the re-link running now, and
// the ones already made. `lmm update` sets Relinked for the re-links that
// run made; `lmm serve` sets JobID and Mods while its job runs, and
// Relinked for the re-links it has made since the user's last explicit
// check.
type CatalogAutoRelink struct {
	// JobID is the frontend's job re-linking Mods now.
	JobID string `json:"job_id,omitzero"`
	// Mods are the gone mods being re-linked, as domain.ModKey strings of
	// their old identity.
	Mods []string `json:"mods,omitzero"`
	// Relinked are the re-links made.
	Relinked []CatalogRelinked `json:"relinked,omitzero"`
}

// AutoRelinkFailure is one automatic re-link that was tried and failed.
type AutoRelinkFailure struct {
	// Mod is the gone mod, as a domain.ModKey string.
	Mod     string `json:"mod"`
	Name    string `json:"name,omitempty"`
	ToModID string `json:"to_mod_id"`
	Error   string `json:"error"`
}

// AutoRelinkResult is an automatic re-link pass's outcome.
type AutoRelinkResult struct {
	Relinked []CatalogRelinked   `json:"relinked"`
	Failed   []AutoRelinkFailure `json:"failed,omitempty"`
}

// HasUnstableIDs reports whether sourceID declares that its catalog can give
// the same mod a new ID (source.UnstableIDSource).
func (s *Service) HasUnstableIDs(sourceID string) bool {
	src, err := s.registry.Get(sourceID)
	if err != nil {
		return false
	}
	u, ok := src.(source.UnstableIDSource)
	return ok && u.UnstableIDs()
}

// autoRelinkItem is one planned re-link.
type autoRelinkItem struct {
	sourceID, fromID, toID, name string
}

// AutoRelinkPlan is PlanAutoRelink's answer: the gone mods to re-link.
type AutoRelinkPlan struct {
	// Mods are the gone mods to re-link, as domain.ModKey strings.
	Mods []string

	gameID, profile string
	items           []autoRelinkItem
	snapshot        installedSnapshot
}

// PlanAutoRelink plans the automatic re-link over a check's gone mods
// (UpdateCheckReport.CatalogMissing, successors filled in) for profileName:
// nil when there is nothing it would re-link, or this is not the moment.
// A gone mod qualifies when its source declares unstable IDs, the check
// found exactly one candidate - counted before versions are looked at -
// with strictly the same name and author and a comparable version no older
// than the installed one (catalogSuccessors' autoTarget), its
// ID is not already installed or claimed by another gone mod, the source is
// mapped on the game, and the mod is not locked or external. A profile that is not the game's active
// one plans nothing, and nor does a check made while the game runs.
//
// installed is the set the check ran against; it is the plan's freshness
// precondition, as for PlanAutoUpdate. It reads the machine and the
// database; it changes nothing.
func (s *Service) PlanAutoRelink(ctx context.Context, game *domain.Game, profileName string, installed []domain.InstalledMod, missing []CatalogModRef) (*AutoRelinkPlan, error) {
	plan := &AutoRelinkPlan{gameID: game.ID, profile: profileName}
	var items []autoRelinkItem
	claims := map[string]int{}
	for _, ref := range missing {
		item, ok, err := s.autoRelinkItem(ctx, game, profileName, installed, ref)
		if err != nil {
			return nil, err
		}
		if ok {
			items = append(items, item)
			claims[domain.ModKey(item.sourceID, item.toID)]++
		}
	}
	for _, item := range items {
		// Two gone mods matching one successor: which of them moved is not
		// known, so neither is re-linked.
		if claims[domain.ModKey(item.sourceID, item.toID)] > 1 {
			continue
		}
		plan.items = append(plan.items, item)
		plan.Mods = append(plan.Mods, domain.ModKey(item.sourceID, item.fromID))
	}
	if len(plan.items) == 0 {
		return nil, nil
	}
	if err := s.requireActiveProfile(ctx, game.ID, profileName, VerbUpdateMany); err != nil {
		if errors.Is(err, ErrProfileNotActive) {
			return nil, nil
		}
		return nil, err
	}
	if s.GameRunning(ctx, game).Running {
		return nil, nil
	}
	snapshot, err := s.snapshotOf(game.ID, installed)
	if err != nil {
		return nil, err
	}
	plan.snapshot = snapshot
	return plan, nil
}

// autoRelinkItem decides whether ref is one PlanAutoRelink re-links.
func (s *Service) autoRelinkItem(ctx context.Context, game *domain.Game, profileName string, installed []domain.InstalledMod, ref CatalogModRef) (autoRelinkItem, bool, error) {
	// autoTarget, not the suggestions: it is set only when the check found
	// exactly one candidate, strictly the same name and author, at a known
	// version no older than the installed one (catalogSuccessors).
	if ref.autoTarget == "" || !s.HasUnstableIDs(ref.SourceID) {
		return autoRelinkItem{}, false, nil
	}
	if _, mapped := game.SourceIDs[ref.SourceID]; !mapped {
		return autoRelinkItem{}, false, nil
	}
	next := ref.autoTarget
	var gone, taken *domain.InstalledMod
	for i := range installed {
		switch {
		case installed[i].SourceID == ref.SourceID && installed[i].ID == ref.ModID:
			gone = &installed[i]
		case installed[i].SourceID == ref.SourceID && installed[i].ID == next:
			taken = &installed[i]
		}
	}
	if gone == nil || taken != nil || gone.External {
		return autoRelinkItem{}, false, nil
	}
	locked, _, err := s.lockState(ctx, game.ID, profileName, gone.SourceID, gone.ID)
	if err != nil {
		return autoRelinkItem{}, false, err
	}
	if locked {
		return autoRelinkItem{}, false, nil
	}
	return autoRelinkItem{sourceID: gone.SourceID, fromID: gone.ID, toID: next, name: gone.Name}, true, nil
}

// AutoRelinkRun is a begun automatic re-link: it holds the mutation slot
// from BeginAutoRelink until Apply returns or Abandon is called.
type AutoRelinkRun struct {
	s       *Service
	game    *domain.Game
	plan    *AutoRelinkPlan
	release func()
}

// BeginAutoRelink takes the mutation slot for plan now, or refuses at once:
// ErrOperationInProgress (or OperationInProgressError, another lmm process)
// while a mutation runs, and ErrStalePlan when the installed set has moved
// since the check the plan was made from. Neither changed anything. The
// returned run must be Applied or Abandoned; either releases the slot.
func (s *Service) BeginAutoRelink(ctx context.Context, game *domain.Game, plan *AutoRelinkPlan) (*AutoRelinkRun, error) {
	if plan == nil {
		return nil, errors.New("no automatic re-link planned")
	}
	release, err := s.tryBeginOp(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.checkPlanFresh(ctx, plan.gameID, plan.profile, plan.snapshot); err != nil {
		release()
		return nil, fmt.Errorf("automatic re-link: %w", err)
	}
	return &AutoRelinkRun{s: s, game: game, plan: plan, release: sync.OnceFunc(release)}, nil
}

// Apply makes the planned re-links, each through the re-link `lmm mod edit
// --to-source-id` makes, and releases the slot. Each keeps the version
// installed rather than taking the successor's: those are the files in the
// cache, which carry across to the new ID (carryRelinkCache), so nothing
// reads MISSING afterwards, and a newer version is the update check's to
// offer, as for any other mod. A re-link that fails is reported and the
// rest still run. sink may be nil.
func (r *AutoRelinkRun) Apply(ctx context.Context, sink EventSink) (*AutoRelinkResult, error) {
	defer r.release()
	s := r.s
	result := &AutoRelinkResult{Relinked: []CatalogRelinked{}}
	for _, item := range r.plan.items {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		done, err := s.autoRelinkOne(ctx, r.game, r.plan.profile, item, sink)
		if err != nil {
			if ctx.Err() != nil {
				return result, err
			}
			result.Failed = append(result.Failed, AutoRelinkFailure{
				Mod: domain.ModKey(item.sourceID, item.fromID), Name: item.name, ToModID: item.toID, Error: err.Error(),
			})
			continue
		}
		result.Relinked = append(result.Relinked, done)
	}
	return result, nil
}

// autoRelinkOne re-links one planned item, under the slot Apply holds.
func (s *Service) autoRelinkOne(ctx context.Context, game *domain.Game, profileName string, item autoRelinkItem, sink EventSink) (CatalogRelinked, error) {
	plan, err := s.PlanRelinkMod(ctx, game, profileName, item.sourceID, item.fromID, "", item.toID)
	if err != nil {
		return CatalogRelinked{}, err
	}
	res, err := s.applyRelinkMod(ctx, game, plan, RelinkOptions{keepVersion: true}, sink)
	if res != nil {
		s.takeCaptureWarnings(game.ID, OpModEdit, RelinkWarning, &res.Warnings, sink)
	}
	if err != nil {
		return CatalogRelinked{}, err
	}
	name := cmp.Or(item.name, item.fromID)
	return CatalogRelinked{
		SourceID: item.sourceID, FromModID: item.fromID, ToModID: item.toID, Name: item.name,
		Message:  fmt.Sprintf("%s moved to a new ID in %s; re-linked (%s -> %s)", name, s.sourceDisplayName(item.sourceID), item.fromID, item.toID),
		Warnings: res.Warnings,
	}, nil
}

// Abandon releases the slot of a run that will not be applied. Safe to
// call more than once, and after Apply.
func (r *AutoRelinkRun) Abandon() { r.release() }

// AutoRelink is the whole automatic re-link in one call, for a frontend that
// runs it in line (`lmm update`): PlanAutoRelink, BeginAutoRelink and Apply.
// It answers nil, nil when there is nothing to re-link or this is not the
// moment - another mutation running, a check older than the last change -
// which a later check retries. sink may be nil.
func (s *Service) AutoRelink(ctx context.Context, game *domain.Game, profileName string, installed []domain.InstalledMod, missing []CatalogModRef, sink EventSink) (*AutoRelinkResult, error) {
	plan, err := s.PlanAutoRelink(ctx, game, profileName, installed, missing)
	if err != nil || plan == nil {
		return nil, err
	}
	run, err := s.BeginAutoRelink(ctx, game, plan)
	if err != nil {
		if errors.Is(err, ErrOperationInProgress) || errors.Is(err, ErrStalePlan) || errors.As(err, new(*OperationInProgressError)) {
			return nil, nil
		}
		return nil, err
	}
	return run.Apply(ctx, sink)
}
