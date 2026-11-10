// auto_relink.go is #590's serve half: an update check this server makes
// for the SPA re-links a gone mod of a source with unstable IDs to its one
// successor, as an automatic "catalog_relink" job, so the tray shows it.
//
// It runs exactly where #587's automatic update pass runs (auto_updates.go)
// and under the same rules: only for a request carrying the SPA's CSRF
// token, never queued behind a running job or another lmm's mutation, one
// job per game and profile at a time. core decides what qualifies
// (PlanAutoRelink: one successor, not locked, the active profile, the game
// not running). The server remembers what it re-linked, so the Updates card
// goes on saying so after the job's own re-check no longer lists the mod as
// gone, until the user's explicit "check for updates" (?refresh=1). A
// re-link that failed is held back the same way, so the check the job's
// completion causes does not try it again, and again, for ever.
package serve

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// autoRelinker is the server's automatic re-link state. Its zero value is
// ready to use.
type autoRelinker struct {
	mu sync.Mutex
	// running is the automatic job in flight per autoUpdateKey.
	running map[string]autoUpdateJob
	// relinked is, per autoUpdateKey, the re-links made since the last
	// explicit check.
	relinked map[string][]core.CatalogRelinked
	// failed is, per autoUpdateKey, the gone mods (domain.ModKey) whose
	// re-link failed: held back until an explicit check.
	failed map[string]map[string]bool
}

// status is key's state for the report: the job running, if any, and the
// re-links made. nil when there is neither. Caller holds mu.
func (a *autoRelinker) status(key string) *core.CatalogAutoRelink {
	var out core.CatalogAutoRelink
	if job, ok := a.running[key]; ok {
		out.JobID, out.Mods = string(job.id), job.mods
	}
	out.Relinked = slices.Clone(a.relinked[key])
	if out.JobID == "" && len(out.Relinked) == 0 {
		return nil
	}
	return &out
}

// recordOutcome ends key's automatic job and remembers what it did: every
// re-link made, and - for a job that failed outright - every one planned as
// failed.
func (a *autoRelinker) recordOutcome(key string, planned []string, result *core.AutoRelinkResult, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.running, key)
	if a.relinked == nil {
		a.relinked = map[string][]core.CatalogRelinked{}
	}
	if a.failed == nil {
		a.failed = map[string]map[string]bool{}
	}
	if a.failed[key] == nil {
		a.failed[key] = map[string]bool{}
	}
	if result != nil {
		a.relinked[key] = append(a.relinked[key], result.Relinked...)
		for _, f := range result.Failed {
			a.failed[key][f.Mod] = true
		}
	}
	if err != nil {
		for _, mod := range planned {
			a.failed[key][mod] = true
		}
	}
}

// autoRelinkGone is the automatic re-link over one check's gone mods: it
// starts the job that re-links the ones core plans, and reports it along
// with the re-links already made - or reports only those when there is
// nothing to re-link or this is not the moment (another job running, a
// check older than the last change), which the next check retries.
// installed is the set the check ran against; refresh is the user's
// explicit check, which forgets the record first.
func (s *Server) autoRelinkGone(ctx context.Context, game *domain.Game, profile string, installed []domain.InstalledMod, missing []core.CatalogModRef, refresh bool) *core.CatalogAutoRelink {
	key := autoUpdateKey(game.ID, profile)
	a := &s.autoRelinks
	a.mu.Lock()
	defer a.mu.Unlock()
	if refresh {
		delete(a.relinked, key)
		delete(a.failed, key)
	}
	if _, ok := a.running[key]; ok {
		return a.status(key)
	}
	missing = slices.DeleteFunc(slices.Clone(missing), func(m core.CatalogModRef) bool {
		return a.failed[key][domain.ModKey(m.SourceID, m.ModID)]
	})
	if s.jobs.QueueDepth() > 0 {
		return a.status(key)
	}
	plan, err := s.svc.PlanAutoRelink(ctx, game, profile, installed, missing)
	if err != nil {
		s.log.Debug("serve: planning automatic re-links", "game", game.ID, "profile", profile, "err", err)
		return a.status(key)
	}
	if plan == nil {
		return a.status(key)
	}
	run, err := s.svc.BeginAutoRelink(ctx, game, plan)
	if err != nil {
		s.log.Debug("serve: automatic re-links step aside", "game", game.ID, "profile", profile, "err", err)
		return a.status(key)
	}
	planned := slices.Clone(plan.Mods)
	id, err := s.jobs.startWithOrigin("catalog_relink", jobOriginAuto, func(ctx context.Context, sink core.EventSink) (_ any, err error) {
		var result *core.AutoRelinkResult
		// Deferred, so a panic - which the registry recovers one level up -
		// still ends the job here, as autoApplyRetryable's does.
		defer func() {
			if p := recover(); p != nil {
				a.recordOutcome(key, planned, nil, errors.New("automatic re-link panicked"))
				panic(p)
			}
			a.recordOutcome(key, planned, result, err)
		}()
		result, err = run.Apply(ctx, sink)
		return result, err
	})
	if err != nil {
		run.Abandon()
		s.log.Debug("serve: automatic re-links step aside", "game", game.ID, "profile", profile, "err", err)
		return a.status(key)
	}
	if a.running == nil {
		a.running = map[string]autoUpdateJob{}
	}
	// Entered under mu before the job can end: recordOutcome takes mu too.
	a.running[key] = autoUpdateJob{id: id, mods: planned}
	return a.status(key)
}
