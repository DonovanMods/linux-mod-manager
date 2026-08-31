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
	"net/http"
	"strconv"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

func init() {
	registerPlanKind(planKind{
		Name:         "verify_fix",
		Title:        "Repair",
		PlanOptions:  decodeKindOptions[verifyFixPlanRequest],
		ApplyOptions: decodeKindOptions[verifyFixApplyRequest],
		Plan:         planVerifyFixKind,
		Apply:        applyVerifyFixKind,
		Summarize:    summarizeVerifyFixResult,
		Form: &kindForm{
			PlanOptions:  verifyFixPlanForm,
			ApplyOptions: verifyFixApplyForm,
			Confirm:      confirmVerifyFixPlan,
		},
	})
}

// verifyFixRepairableStatuses are the finding statuses `--fix` actually
// acts on, and therefore the ones that make a repair worth offering at all
// (internal/core/verify.go's four repair branches plus the convergence
// pass). A finding outside this set - version_unverifiable, a file-count
// mismatch, a skipped row - is a report, not a repairable, and offering to
// "fix" it would promise something the engine never attempts.
//
// The list is status-only. Core additionally refuses to repair a
// domain.SourceLocal mod (an imported archive has no remote to re-fetch),
// but VerifyFinding carries no source id, so a local-only profile can still
// be offered a repair that reports the same rows back unchanged. That is
// the honest failure mode of the data the report actually carries: better
// than hiding the action from every profile because one mod in it might be
// local.
//
// KNOWN DRIFT RISK (task-9 review Minor 2, accepted for v2.1.0, not fixed
// here): this duplicates core's status vocabulary by string literal. Core
// exports no constants for these either (they're literals in
// internal/core/verify.go's repair branches), so a new repairable status
// added there silently stops being offered here, with no test or ratchet
// to catch the drift. The real fix - core.VerifyFindingIsRepairable or
// exported status constants both sides read - touches core's public
// surface and belongs in a follow-up issue, not this polish pass.
var verifyFixRepairableStatuses = map[string]bool{
	"missing":          true,
	"no_checksum":      true,
	"needs_reingest":   true,
	"version_mismatch": true,
	"stale_deployment": true,
}

// verifyFixableFindings returns the findings a repair would act on.
func verifyFixableFindings(report *core.VerifyReport) []core.VerifyFinding {
	if report == nil || report.Result == nil {
		return nil
	}
	var fixable []core.VerifyFinding
	for _, finding := range report.Result.Findings {
		if verifyFixRepairableStatuses[finding.Status] {
			fixable = append(fixable, finding)
		}
	}
	return fixable
}

// verifyFixPlanRequest is POST /api/v1/plans/verify_fix's request body:
// nothing yet. The tier is fixed at VerifyLocal for both halves, matching
// what /health itself renders - a page must stay cheap and offline, and a
// repair the user asked for from that page should act on the findings that
// page showed, not on a wider set it never saw.
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
	return core.VerifyOptions{Tier: core.VerifyLocal, Fix: fix}
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

// summarizeVerifyFixResult implements planKind.Summarize for "verify_fix".
// A repaired row rewrites its own status to a fixed_* form (or resolves to
// "ok") and backs its count out, so the counts below are what REMAINS
// outstanding after the repair - which is the number a user actually wants.
//
// "ok" is perFileWalk's baseline for every healthy checksummed file
// (internal/core/verify.go:624), not an outcome a repair produced - a repair
// that resolved a row TO "ok" (a successful redownload, a backfilled
// checksum) is indistinguishable in the final VerifyResult from a file that
// was never broken, so there is no reliable way to single the former out.
// An "ok" row is therefore never listed one-per-file here; it is folded into
// the "Healthy files" count instead (gate review Important 1).
func summarizeVerifyFixResult(result any) []resultFact {
	report, ok := result.(*core.VerifyReport)
	if !ok || report.Result == nil {
		return nil
	}

	res := report.Result
	facts := []resultFact{
		{Label: "Files checked", Value: strconv.Itoa(res.Checked)},
		{Label: "Issues remaining", Value: strconv.Itoa(res.Issues)},
		{Label: "Warnings remaining", Value: strconv.Itoa(res.Warnings)},
	}
	var healthy int
	for _, finding := range res.Findings {
		if finding.Status == "ok" {
			healthy++
			continue
		}
		facts = append(facts, resultFact{Label: findingLabel(finding.Status), Value: verifyFindingText(finding)})
	}
	if healthy > 0 {
		facts = append(facts, resultFact{Label: "Healthy files", Value: strconv.Itoa(healthy)})
	}
	return facts
}

// findingLabel splits repaired rows from the ones still outstanding, so a
// result readout does not list a fixed row and an unfixed one under the
// same word. "ok" is not among these: summarizeVerifyFixResult never calls
// this for an "ok" finding (see its own doc comment).
func findingLabel(status string) string {
	switch status {
	case "fixed_stale_deployment", "fixed_needs_reingest":
		return "Repaired"
	default:
		return "Still reported"
	}
}

// verifyFindingText renders one finding as a line: which mod, which file,
// what state, and whatever the engine had to say about it.
func verifyFindingText(finding core.VerifyFinding) string {
	text := finding.ModName
	if text == "" {
		text = finding.ModID
	}
	if finding.FileID != "" {
		if text != "" {
			text += " - "
		}
		text += finding.FileID
	}
	if text == "" {
		text = "profile"
	}
	text += ": " + finding.Status
	if finding.Recorded != "" && finding.Effective != "" {
		text += fmt.Sprintf(" (recorded %s, effective %s)", finding.Recorded, finding.Effective)
	}
	if finding.Note != "" {
		text += " - " + finding.Note
	}
	return text
}

// verifyFixPlanForm implements kindForm.PlanOptions: the repair is scoped
// by the resolved game+profile alone, so there is nothing to read.
func verifyFixPlanForm(*http.Request) (any, error) {
	return verifyFixPlanRequest{}, nil
}

// verifyFixApplyForm implements kindForm.ApplyOptions: likewise nothing.
func verifyFixApplyForm(*http.Request) (any, error) {
	return verifyFixApplyRequest{}, nil
}

// confirmVerifyFixPlan implements kindForm.Confirm: the dry run's findings,
// split into what a repair would act on and what it would only report
// again.
func confirmVerifyFixPlan(pending, _ any) confirmView {
	p, ok := pending.(*pendingVerifyFix)
	if !ok {
		return confirmView{Submit: "Repair"}
	}

	report := p.Report
	view := confirmView{
		Heading: p.Profile,
		Submit:  "Repair",
		Facts: []resultFact{
			{Label: "Profile", Value: p.Profile},
			{Label: "Files checked", Value: strconv.Itoa(report.Result.Checked)},
			{Label: "Issues", Value: strconv.Itoa(report.Result.Issues)},
			{Label: "Warnings", Value: strconv.Itoa(report.Result.Warnings)},
		},
	}

	fixable := verifyFixableFindings(report)
	var repairable, reported []string
	fixableKeys := make(map[string]bool, len(fixable))
	for _, finding := range fixable {
		repairable = append(repairable, verifyFindingText(finding))
		fixableKeys[finding.ModID+"/"+finding.FileID+"/"+finding.Status] = true
	}
	for _, finding := range report.Result.Findings {
		// A healthy "ok" row is not something a repair "cannot act on and
		// would report again" - it is not a finding a repair would ever act
		// on in the first place (gate review Important 1; same reasoning as
		// summarizeVerifyFixResult).
		if finding.Status == "ok" {
			continue
		}
		if !fixableKeys[finding.ModID+"/"+finding.FileID+"/"+finding.Status] {
			reported = append(reported, verifyFindingText(finding))
		}
	}

	if len(repairable) > 0 {
		view.Lists = append(view.Lists, confirmList{Label: "Findings a repair would act on", Items: repairable})
	}
	if len(reported) > 0 {
		view.Lists = append(view.Lists, confirmList{
			Label: "Findings a repair cannot act on, and would report again",
			Items: reported,
		})
	}
	if len(repairable) == 0 {
		view.Facts = append(view.Facts, resultFact{Label: "Repairs", Value: "nothing here is repairable"})
	}
	return view
}
