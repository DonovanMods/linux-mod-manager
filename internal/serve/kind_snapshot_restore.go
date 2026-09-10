// kind_snapshot_restore.go registers the "snapshot_restore" plan kind -
// `lmm snapshot restore` as a Plan -> confirm -> job flow (#350).
//
// This is the one snapshot operation that earns a job: it is destructive,
// it runs four stages (purge, originals, profile, converge+deploy), each
// with its own live phases, and it can take minutes when a recorded version
// has to be downloaded again. Create and delete are single writes and go
// through api_snapshots.go instead.
//
// The snapshot is named in the BODY, not the path: a restore acts on a
// snapshot you picked from a list, the same relationship `profile_import`
// and `switch` have with the profile they name. The profile it restores is
// the SNAPSHOT's own (core.PlanSnapshotRestore reads it from the document),
// never the ?profile= selection - restoring "the snapshot I took under
// survival" while looking at another profile must not silently retarget it.
package serve

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func init() {
	registerPlanKind(planKind{
		Name:         "snapshot_restore",
		PlanOptions:  decodeKindOptions[snapshotRestorePlanRequest],
		ApplyOptions: decodeKindOptions[snapshotRestoreApplyRequest],
		Plan:         planSnapshotRestoreKind,
		Apply:        applySnapshotRestoreKind,
	})
}

// snapshotRestorePlanRequest is POST /api/v1/plans/snapshot_restore's
// request body: which snapshot. Required - there is no sensible default,
// and "the newest one" would be a guess about intent.
type snapshotRestorePlanRequest struct {
	Snapshot string `json:"snapshot"`
}

// validate refuses a request with no snapshot named, at the boundary,
// rather than letting core answer a lookup for "".
func (r *snapshotRestorePlanRequest) validate() error {
	if r.Snapshot == "" {
		return errors.New(`"snapshot" is required`)
	}
	return nil
}

// snapshotRestoreApplyRequest is the "options" member POST /api/v1/jobs
// accepts for a restore plan - core.SnapshotRestoreOptions' three fields.
type snapshotRestoreApplyRequest struct {
	SkipHooks bool `json:"skip_hooks,omitzero"`
	Force     bool `json:"force,omitzero"`
	// NoSafetySnapshot suppresses the snapshot of the CURRENT state a
	// restore takes first. Named in the negative, as the CLI flag is,
	// because the safety copy is the DEFAULT: a restore's whole purpose is
	// to discard the present state, so having a way back from it is not
	// something a caller should have to remember to ask for.
	NoSafetySnapshot bool `json:"no_safety_snapshot,omitzero"`
}

// restoreOptions renders the request as the core options struct.
func (r snapshotRestoreApplyRequest) restoreOptions() core.SnapshotRestoreOptions {
	return core.SnapshotRestoreOptions{
		SkipHooks:        r.SkipHooks,
		Force:            r.Force,
		NoSafetySnapshot: r.NoSafetySnapshot,
	}
}

// pendingSnapshotRestore is what the plan store holds between Plan and
// Apply: the plan object itself (pointer identity preserved, so its
// unexported freshness snapshot and the snapshot document it was computed
// from both survive to the apply) and the game.
type pendingSnapshotRestore struct {
	Game *domain.Game
	Plan *core.SnapshotRestorePlan
}

// planSnapshotRestoreKind implements planKind.Plan for "snapshot_restore".
func planSnapshotRestoreKind(ctx context.Context, s *Server, sel selection, opts any) (any, any, error) {
	req, ok := opts.(snapshotRestorePlanRequest)
	if !ok {
		return nil, nil, fmt.Errorf("snapshot restore plan: unexpected options type %T", opts)
	}

	plan, err := s.svc.PlanSnapshotRestore(ctx, sel.Game, req.Snapshot)
	if err != nil {
		// A name that could never BE a snapshot is bad input (400);
		// ErrSnapshotNotFound needs no wrapping here - planErrorStatus
		// knows that sentinel and answers 404 for it directly.
		if errors.Is(err, core.ErrInvalidSnapshotName) {
			return nil, nil, fmt.Errorf("%w: %w", errBadPlanRequest, err)
		}
		return nil, nil, err
	}
	return plan, &pendingSnapshotRestore{Game: sel.Game, Plan: plan}, nil
}

// applySnapshotRestoreKind implements planKind.Apply for
// "snapshot_restore".
func applySnapshotRestoreKind(ctx context.Context, s *Server, pending, opts any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingSnapshotRestore)
	if !ok {
		return nil, fmt.Errorf("snapshot restore apply: unexpected pending type %T", pending)
	}
	req, ok := opts.(snapshotRestoreApplyRequest)
	if !ok {
		return nil, fmt.Errorf("snapshot restore apply: unexpected options type %T", opts)
	}
	return s.svc.ApplySnapshotRestore(ctx, p.Game, p.Plan, req.restoreOptions(), sink)
}
