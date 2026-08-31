// kind_verify_fix.go registers the "verify_fix" plan kind - `lmm verify
// --fix` as a Plan -> confirm -> job flow
// (docs/plans/2026-08-30-serve-impl.md Task 9: "health repair (verify fix
// as a job; findings page gains the action)").
//
// Its Plan/Apply pair is not the usual one, and the reason is worth stating
// plainly. Core has no PlanVerify/ApplyVerify: it has ONE entry point,
// Service.VerifyReport, whose VerifyOptions.Fix decides whether the run
// repairs or merely reports - and core takes the mutation gate only under
// Fix ("a --fix run mutates so it takes the same one-slot mutation
// semaphore every other mutating flow does; a plain run is read-only",
// internal/core/verify.go).
//
// That is exactly a plan and an apply. The plan is the dry run: the same
// engine, the same passes, the same findings, with every repair withheld -
// a preview that IS what would be repaired, not a description of it. The
// apply is the identical call with Fix set. So this kind is a real
// Plan/Apply pair over one core method, rather than a Ruling-10 wrapper
// smuggled in as a mutation entry point.
//
// The one property it does NOT share with the other kinds is a freshness
// snapshot: a verify plan carries none, because the verify engine has no
// installedSnapshot precondition to re-check - it re-derives the whole
// world on every pass, so a stale preview simply repairs whatever it finds
// now and reports that. The plan store's own single-use TTL still applies.
package serve

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func init() {
	registerPlanKind(planKind{
		Name:         "verify_fix",
		PlanOptions:  decodeKindOptions[verifyFixPlanRequest],
		ApplyOptions: decodeKindOptions[verifyFixApplyRequest],
		Plan:         planVerifyFixKind,
		Apply:        applyVerifyFixKind,
	})
}

// verifyFixPlanRequest is POST /api/v1/plans/verify_fix's request body:
// nothing yet. The tier is fixed at VerifyFull for both halves (epic live
// review C1), matching what /health itself now renders - a repair the user
// asked for from that page acts on exactly the findings that page showed,
// which now includes a version_mismatch. Running the plan half at a
// DIFFERENT (cheaper) tier than the apply half would let the confirm page
// show one set of findings while the apply repairs a different one -
// version_mismatch's repair mutates the recorded version BEFORE the
// missing-file repair ever looks at the cache, so a plan/apply tier
// mismatch here could resurrect exactly the corruption this fix exists to
// close.
type verifyFixPlanRequest struct{}

// verifyFixApplyRequest is the "options" member POST /api/v1/jobs accepts
// for a verify_fix plan: nothing. Declaring the empty struct rather than
// skipping the decoder makes an attempt to pass options a 400 rather than a
// silent no-op, the same reason deployApplyRequest exists.
type verifyFixApplyRequest struct{}

// pendingVerifyFix is what the plan store holds between Plan and Apply.
// Unlike every other kind it holds no plan object: there is nothing to
// preserve pointer identity for (see this file's doc comment), only the
// game and profile the repair must run against - which are kept here rather
// than re-resolved at apply time so a job cannot drift onto a different
// profile than the one previewed.
type pendingVerifyFix struct {
	Game    *domain.Game
	Profile string
	Report  *core.VerifyReport
}

// verifyFixOptions is the shared option set both halves use; only Fix
// differs, which is the whole point.
func verifyFixOptions(fix bool) core.VerifyOptions {
	return core.VerifyOptions{Tier: core.VerifyFull, Fix: fix}
}

// planVerifyFixKind implements planKind.Plan for "verify_fix": the dry run.
func planVerifyFixKind(ctx context.Context, s *Server, sel selection, _ any) (any, any, error) {
	report, err := s.svc.VerifyReport(ctx, sel.Game, sel.Profile, verifyFixOptions(false), nil)
	if err != nil {
		return nil, nil, err
	}
	return report, &pendingVerifyFix{Game: sel.Game, Profile: sel.Profile, Report: report}, nil
}

// applyVerifyFixKind implements planKind.Apply for "verify_fix": the same
// run with the repairs switched on. ctx is the job's own (jobs.go), so a
// closed tab cannot abandon a half-finished repair.
func applyVerifyFixKind(ctx context.Context, s *Server, pending, _ any, sink core.EventSink) (any, error) {
	p, ok := pending.(*pendingVerifyFix)
	if !ok {
		return nil, fmt.Errorf("verify fix: unexpected pending type %T", pending)
	}
	return s.svc.VerifyReport(ctx, p.Game, p.Profile, verifyFixOptions(true), sink)
}
