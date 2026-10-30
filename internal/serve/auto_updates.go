// auto_updates.go is #587's serve half: every update check this server
// makes - GET /api/v1/updates, whichever tab or page load asked - applies
// the auto-policy updates it found, as an ordinary "updates" job.
//
// The decision is the server's, never a browser's, so two open tabs cannot
// apply the same update twice: the begin-and-start below is one critical
// section per server, a check that finds the pass already running for its
// game and profile reports that job instead of starting another, and a
// check older than the last change is refused as stale by core
// (BeginAutoUpdate). The pass never queues: a running job - any job, since
// an install's download runs outside core's mutation slot - or another lmm
// process's mutation leaves the work to the next check.
//
// The job's own completion re-reads the updates (the SPA re-hydrates after
// every job), so an update still offered after the job - one that failed,
// was skipped, or applied and is somehow offered again - would otherwise be
// retried by the very check that job caused, and again, for ever.
// autoUpdater remembers each target version a job attempted and holds it
// back until the source offers a newer one or the user asks for a fresh
// check (?refresh=1); it stays on the card for the user meanwhile, and the
// job's result is in the tray. One that FAILED is not left there
// unexplained: the report carries it (auto_update.failed), with the reason
// and the job, until it is tried again.
//
// Only the SPA starts the pass. A GET needs no CSRF token, so any site
// could make a browser fetch /api/v1/updates (an <img> will do); the pass
// therefore runs only for a request carrying the token, which the SPA
// sends on every request and a cross-site page cannot set. Without it the
// check answers read-only.
package serve

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// autoUpdater is the server's automatic-update state. Its zero value is
// ready to use.
type autoUpdater struct {
	mu sync.Mutex
	// running is the automatic job in flight per autoUpdateKey.
	running map[string]autoUpdateJob
	// attempted is, per autoUpdateKey and then domain.ModKey, the target
	// version an automatic job already tried.
	attempted map[string]map[string]string
	// failed is, keyed the same way, the attempts among those that failed:
	// what the report says while they are held back.
	failed map[string]map[string]core.ModAutoUpdateFailure
}

// autoUpdateJob is one automatic job in flight.
type autoUpdateJob struct {
	id   jobID
	mods []string
}

// autoUpdateKey is the game/profile pair a pass is for.
func autoUpdateKey(gameID, profile string) string { return gameID + "\x00" + profile }

// retryable returns the updates not held back by a recorded attempt at the
// same target version. refresh - the user's explicit "check for updates" -
// forgets the record first.
func (a *autoUpdater) retryable(key string, updates []domain.Update, refresh bool) []domain.Update {
	a.mu.Lock()
	defer a.mu.Unlock()
	if refresh {
		delete(a.attempted, key)
		delete(a.failed, key)
	}
	attempted := a.attempted[key]
	return slices.DeleteFunc(slices.Clone(updates), func(u domain.Update) bool {
		v, ok := attempted[domain.ModKey(u.InstalledMod.SourceID, u.InstalledMod.ID)]
		return ok && v == u.NewVersion
	})
}

// recordOutcome ends key's automatic job and remembers what it attempted:
// every item it reached - applied, skipped or failed - at the target
// version the check offered, or, for a job that failed outright, every
// item it planned. The failed ones are also kept with their reason and the
// job's id, for the report (failures).
func (a *autoUpdater) recordOutcome(key string, planned []domain.Update, result *core.UpdateBatchResult, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	// The job is entered in running under this lock before it can end (see
	// autoApplyModUpdates), so its id is here to read.
	job := string(a.running[key].id)
	delete(a.running, key)
	if a.attempted == nil {
		a.attempted = map[string]map[string]string{}
	}
	if a.attempted[key] == nil {
		a.attempted[key] = map[string]string{}
	}
	if a.failed == nil {
		a.failed = map[string]map[string]core.ModAutoUpdateFailure{}
	}
	if a.failed[key] == nil {
		a.failed[key] = map[string]core.ModAutoUpdateFailure{}
	}
	reached := map[string]bool{}
	failed := map[string]core.UpdateBatchFailure{}
	if result != nil {
		for _, r := range slices.Concat(result.Applied, result.Skipped) {
			reached[domain.ModKey(r.Mod.SourceID, r.Mod.ModID)] = true
		}
		for _, f := range result.Failed {
			reached[f.Mod] = true
			failed[f.Mod] = f
		}
	}
	for _, u := range planned {
		mod := domain.ModKey(u.InstalledMod.SourceID, u.InstalledMod.ID)
		if err == nil && !reached[mod] {
			continue
		}
		a.attempted[key][mod] = u.NewVersion
		f, ok := failed[mod]
		if err != nil {
			f, ok = core.UpdateBatchFailure{
				Mod: mod, Name: u.InstalledMod.Name,
				FromVersion: u.InstalledMod.Version, ToVersion: u.NewVersion,
				Error: err.Error(),
			}, true
		}
		if ok {
			a.failed[key][mod] = core.ModAutoUpdateFailure{UpdateBatchFailure: f, JobID: job}
		} else {
			delete(a.failed[key], mod)
		}
	}
}

// failures are the recorded failures still held back for key: each failed
// update the check offers again at the version that failed, in the check's
// order. One the source has moved past is not news any more - the next pass
// tries the newer version - and nor is one the user has since taken off
// the auto policy or locked (core.AutoUpdateKeys).
func (a *autoUpdater) failures(key string, updates []domain.Update) []core.ModAutoUpdateFailure {
	auto := core.AutoUpdateKeys(updates)
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []core.ModAutoUpdateFailure
	for _, u := range updates {
		mod := domain.ModKey(u.InstalledMod.SourceID, u.InstalledMod.ID)
		if !slices.Contains(auto, mod) {
			continue
		}
		f, ok := a.failed[key][mod]
		if ok && a.attempted[key][mod] == u.NewVersion {
			out = append(out, f)
		}
	}
	return out
}

// checkModUpdates is the one update check this server makes, for
// game/profile: the external-revision reconcile, the check itself, and the
// automatic pass over what it found (#587), whose state lands on the
// report. GET /api/v1/updates answers with it; anything else that ever
// checks on the server's own initiative calls it too. err is a failure to
// read the installed set, which leaves no report at all; a partial check
// failure is the report's own ErrorMessage, as ever.
//
// auto says the request may start the pass: only one that proved it comes
// from the SPA (its CSRF token) does. Without it the check is read-only.
func (s *Server) checkModUpdates(ctx context.Context, game *domain.Game, profile string, sink core.EventSink, opts core.UpdateCheckOptions, auto bool) (*core.UpdateCheckReport, error) {
	// #538: record the revision Steam has installed for every tracked
	// Workshop item first, so the library and the mod page read the real
	// one. Never waits behind a running job and never fails the request:
	// the check below reads Steam's manifest itself, so its answer is right
	// either way, and the next hydrate reconciles. Logged, not surfaced.
	if _, err := s.svc.ReconcileExternalMods(ctx, game, profile); err != nil {
		s.log.Debug("serve: reconciling external revisions", "game", game.ID, "profile", profile, "err", err)
	}

	installed, err := s.svc.GetInstalledMods(ctx, game.ID, profile)
	if err != nil {
		return nil, err
	}
	report, _ := s.svc.CheckGameUpdateReport(ctx, game, profile, installed, sink, opts)
	if auto {
		report.AutoUpdate = s.autoApplyModUpdates(ctx, game, profile, installed, report.Updates, opts.Refresh)
	}
	return report, nil
}

// autoApplyModUpdates is the automatic pass over one check's updates: it
// starts the job that applies the auto-policy ones and reports it, reports
// the job already doing so, reports the game it is waiting for - or
// returns nil, when there is nothing to apply or this is not the moment
// (another job running, a check older than the last change), which the
// next check retries. installed is the set the check ran against.
func (s *Server) autoApplyModUpdates(ctx context.Context, game *domain.Game, profile string, installed []domain.InstalledMod, updates []domain.Update, refresh bool) *core.ModAutoUpdate {
	key := autoUpdateKey(game.ID, profile)
	a := &s.autoUpdates
	retry := a.retryable(key, updates, refresh)
	return withFailures(s.autoApplyRetryable(ctx, game, profile, installed, retry), a.failures(key, updates))
}

// withFailures puts failed - the held-back failures - on status, or makes
// them the status when the pass is doing nothing else.
func withFailures(status *core.ModAutoUpdate, failed []core.ModAutoUpdateFailure) *core.ModAutoUpdate {
	if len(failed) == 0 {
		return status
	}
	if status == nil {
		status = &core.ModAutoUpdate{State: core.ModAutoUpdateFailed}
		for _, f := range failed {
			status.Mods = append(status.Mods, f.Mod)
		}
	}
	status.Failed = failed
	return status
}

// autoApplyRetryable is autoApplyModUpdates over the updates no recorded
// attempt holds back.
func (s *Server) autoApplyRetryable(ctx context.Context, game *domain.Game, profile string, installed []domain.InstalledMod, updates []domain.Update) *core.ModAutoUpdate {
	key := autoUpdateKey(game.ID, profile)
	a := &s.autoUpdates
	if status := a.inFlight(key); status != nil {
		return status
	}

	plan, err := s.svc.PlanAutoUpdate(ctx, game, profile, installed, updates)
	if err != nil {
		s.log.Debug("serve: planning automatic updates", "game", game.ID, "profile", profile, "err", err)
		return nil
	}
	if plan == nil {
		return nil
	}
	if plan.Running != nil {
		s.log.Debug("serve: automatic updates wait for the game", "game", game.ID, "signal", plan.Running.Signal, "seen", plan.Running.Detail)
		return &core.ModAutoUpdate{
			State:      core.ModAutoUpdateWaitingForGame,
			Mods:       plan.Mods,
			GameName:   game.Name,
			DetectedBy: plan.Running.Signal,
		}
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if job, ok := a.running[key]; ok {
		return &core.ModAutoUpdate{State: core.ModAutoUpdateApplying, Mods: job.mods, JobID: string(job.id)}
	}
	if s.jobs.QueueDepth() > 0 {
		return nil
	}
	run, err := s.svc.BeginAutoUpdate(ctx, game, plan, core.UpdateBatchOptions{})
	if err != nil {
		s.log.Debug("serve: automatic updates step aside", "game", game.ID, "profile", profile, "err", err)
		return nil
	}
	planned := slices.DeleteFunc(slices.Clone(updates), func(u domain.Update) bool {
		return !slices.Contains(plan.Mods, domain.ModKey(u.InstalledMod.SourceID, u.InstalledMod.ID))
	})
	id, err := s.jobs.startWithOrigin("updates", jobOriginAuto, func(ctx context.Context, sink core.EventSink) (_ any, err error) {
		var result *core.UpdateBatchResult
		// Deferred, so a panic - which the registry recovers one level up -
		// still ends the job here: left running, it would report a dead job
		// as applying on every check until a restart.
		defer func() {
			if p := recover(); p != nil {
				a.recordOutcome(key, planned, nil, errors.New("automatic update panicked"))
				panic(p)
			}
			a.recordOutcome(key, planned, result, err)
		}()
		result, err = run.Apply(ctx, sink)
		return result, err
	})
	if err != nil {
		run.Abandon()
		s.log.Debug("serve: automatic updates step aside", "game", game.ID, "profile", profile, "err", err)
		return nil
	}
	if a.running == nil {
		a.running = map[string]autoUpdateJob{}
	}
	a.running[key] = autoUpdateJob{id: id, mods: plan.Mods}
	return &core.ModAutoUpdate{State: core.ModAutoUpdateApplying, Mods: plan.Mods, JobID: string(id)}
}

// inFlight reports the automatic job running for key, if there is one.
func (a *autoUpdater) inFlight(key string) *core.ModAutoUpdate {
	a.mu.Lock()
	defer a.mu.Unlock()
	if job, ok := a.running[key]; ok {
		return &core.ModAutoUpdate{State: core.ModAutoUpdateApplying, Mods: job.mods, JobID: string(job.id)}
	}
	return nil
}
