package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// VerifyTier selects how much of the verify engine's work runs: VerifyLocal
// is entirely offline (cache/DB state only), VerifyFull additionally
// contacts each installed mod's source (version-record and merged-pak
// checks). Later tasks gate the network-touching phases on this.
type VerifyTier int

// VerifyLocal and VerifyFull are VerifyTier's two values, in strictness
// order: VerifyLocal is the default (offline) tier, VerifyFull the
// network-touching one VerifyOptions.Tier opts into (`lmm verify --full`).
const (
	VerifyLocal VerifyTier = iota
	VerifyFull
)

// verifyTierNames maps each VerifyTier to its wire name. Keep in declaration
// order.
var verifyTierNames = [...]string{
	VerifyLocal: "local",
	VerifyFull:  "full",
}

// String returns the tier's wire name.
func (t VerifyTier) String() string {
	if t >= 0 && int(t) < len(verifyTierNames) && verifyTierNames[t] != "" {
		return verifyTierNames[t]
	}
	return fmt.Sprintf("verify_tier(%d)", int(t))
}

// MarshalText implements encoding.TextMarshaler.
func (t VerifyTier) MarshalText() ([]byte, error) { return []byte(t.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (t *VerifyTier) UnmarshalText(b []byte) error {
	for i, n := range verifyTierNames {
		if n == string(b) {
			*t = VerifyTier(i)
			return nil
		}
	}
	return fmt.Errorf("unknown verify tier %q", b)
}

// VerifyOptions configures a verify run.
type VerifyOptions struct {
	Tier      VerifyTier `json:"tier"`
	Fix       bool       `json:"fix"`
	ModFilter string     `json:"mod_filter,omitempty"`
	// Force runs the tier even when an unchanged installation could be
	// answered from the last run's memo (#336), and replaces that memo with
	// the fresh answer.
	//
	// `lmm verify` always sets it: a user who types the command is asking
	// for a real look at the disk, and the memo's fingerprint cannot see a
	// file rewritten at the same size and mtime. The web UI's Health card
	// sets it for Re-verify and leaves it off for a plain hydrate, which is
	// the case the memo exists for.
	Force bool `json:"force,omitempty"`
}

// VerifyFinding is one reported row - a per-file or per-mod outcome from a
// verify run. FileID/Note are blank when the finding isn't about a single
// file (e.g. a mod-level file-count-mismatch row).
//
// Recorded/Effective/Version mirror the identically-named VerifyEvent extras
// (Recorded/Effective for version_mismatch, Version for missing) - additive
// fields (#224 follow-up) so a caller that only has the final VerifyResult
// (not a progress listener) can still render the recorded/effective
// versions. resolveLast clears them whenever a
// row's Status resolves away from missing/version_mismatch, so a repaired
// row never carries a stale pre-repair value. The CLI's own text/JSON
// rendering is untouched by these fields - it still reads the VerifyEvent
// extras (text mode) or copies VerifyFinding by named field (JSON mode, see
// verifyFileJSON in cmd/lmm/verify.go), never a bare struct copy.
type VerifyFinding struct {
	ModID     string `json:"mod_id,omitempty"`
	ModName   string `json:"mod_name,omitempty"`
	FileID    string `json:"file_id,omitempty"`
	Status    string `json:"status,omitempty"`
	Note      string `json:"note,omitempty"`
	Recorded  string `json:"recorded,omitempty"`
	Effective string `json:"effective,omitempty"`
	Version   string `json:"version,omitempty"`

	// Fixable reports whether a `verify --fix` run would ATTEMPT a repair
	// for this row as it stands (#332). It is set from the same decision
	// points the repairs themselves are gated on - not from a second,
	// drifting table - so a frontend offering a per-finding "Repair"
	// affordance never has to guess, and never has to reimplement the
	// question in JavaScript.
	//
	// The full table, derived from the engine (this file and
	// verify_repair.go):
	//
	//	stale_deployment      always: convergePass removes it under --fix
	//	stale_compile         always, on a plain run: --fix's merged-pak
	//	                      resync (syncMergedPakPass) regenerates it
	//	missing               a non-local source: redownloadModFile
	//	no_checksum           a non-local source: redownload-to-populate
	//	needs_reingest        a non-local source: redownload re-ingests
	//	version_mismatch      a non-local source AND an UNLOCKED ref - a
	//	                      locked ref's Version is the lock's target, and
	//	                      --fix refuses to rewrite it (#97)
	//	everything else       never - ok, skipped, file_count_mismatch,
	//	                      version_unverifiable (nothing to repair it
	//	                      with) and conversion_failed (a conversion is
	//	                      retried only when a merge INPUT changes, which
	//	                      --fix does not itself decide)
	//
	// A row produced BY a --fix run always reports false: its repair has
	// already been attempted, refused, or completed, so "a --fix run would
	// act on this" is no longer true of it. That is what makes the field
	// useful on the verify_fix PLAN document (a Fix=false run), which is
	// where the web UI reads it.
	Fixable bool `json:"fixable,omitzero"`

	// FixableReason is the ENGINE's own reason this finding will not be
	// repaired, or empty when Fixable is true (or when the row is not a
	// problem at all - an "ok" or "skipped" row has nothing to explain).
	// #334: it exists so a frontend offering a per-finding "Repair"
	// affordance can say WHY the button is absent without reimplementing
	// the question - the web UI carried a hand-maintained status->sentence
	// table plus a locked/unlocked guess read off a separate library fetch,
	// which could not see the local-source case at all and had no honest
	// word for it.
	//
	// It is derived at the same decision points Fixable is, from the same
	// inputs, so the two can never disagree about a row. The wordings are
	// sentence fragments in the engine's own voice, meant to be rendered
	// after a lead-in like "Not fixable: ".
	//
	// One deliberate hedge (#325, review M2). A LOCKED ref's missing /
	// no_checksum / needs_reingest rows still report Fixable true with no
	// reason, even though the repair may refuse at attempt time: whether
	// the source can still serve the recorded version's own file is a
	// question only a network call answers, and a reporting-only run must
	// not pay for one. Fixable's own wording is what stays true here ("a
	// --fix run would ATTEMPT a repair" - it does attempt, then declines),
	// and Fixable must NOT be set false on the strength of the lock alone:
	// that would suppress the affordance for a source that CAN serve the
	// version, which is the common case. So the same locked mod can carry a
	// version_mismatch row that says "Not fixable: the ref is locked ..."
	// beside a missing row that offers Repair; the refusal for the second
	// one arrives on the repaired document, as note "locked".
	FixableReason string `json:"fixable_reason,omitzero"`
}

// notFixableLocal is the reason shared by every repair gated on
// redownloadRepairs: a locally imported mod has no source to fetch from, so
// the missing / no_checksum / needs_reingest repairs have nothing to do.
const notFixableLocal = "the mod was imported locally, so there is no source to re-download from"

// redownloadRefusal is redownloadRepairs' reason half: empty when the
// repair applies, the local-source sentence when it does not. Declared
// beside it so VerifyFinding.Fixable and .FixableReason are computed from
// the same expression rather than from two that can drift.
func redownloadRefusal(mod *domain.InstalledMod) string {
	if redownloadRepairs(mod) {
		return ""
	}
	return notFixableLocal
}

// versionMismatchRefusal is versionMismatchRepairs' reason half, on the same
// terms: the two ways the version repair is refused get their own sentence,
// because "there is no source" and "the lock owns this version" are
// different problems with different remedies.
//
// The lock sentence carries no issue number (M2, unit 8 gate review): it is
// rendered verbatim in the web UI's Health card and by `lmm verify --json`,
// and "(#97)" means nothing to the person reading it. The issue behind the
// rule is #97, and this comment is where that belongs.
func versionMismatchRefusal(mod *domain.InstalledMod, ref *domain.ModReference) string {
	if !redownloadRepairs(mod) {
		return notFixableLocal
	}
	if ref != nil && ref.Locked {
		return fmt.Sprintf("the ref is locked at v%s, and --fix will not rewrite what a lock means - unlock it first", ref.Version)
	}
	return ""
}

// VerifyResult is the accumulated outcome of a verify run.
type VerifyResult struct {
	Findings []VerifyFinding `json:"findings"`
	Issues   int             `json:"issues"`
	Warnings int             `json:"warnings"`
	Checked  int             `json:"checked"`   // feeds the CLI's "No files found for mod X" gate
	HasFiles bool            `json:"has_files"` // false = the #217 empty-profile path ran

	// CheckedAt is when the run began, in UTC (#334) - so a surface that
	// renders a stored or cached result ("last verified 2 hours ago") reads
	// it off the document rather than guessing from when it happened to
	// fetch it. Stamped at the START rather than the end because every
	// partial result a cancelled run returns carries it too; a run that
	// stops halfway still checked what it checked, at that moment.
	//
	// omitzero, so a VerifyResult built by hand (a test, a caller
	// assembling one) carries no key at all rather than a zero time.
	CheckedAt time.Time `json:"checked_at,omitzero"`

	// Cached reports that this answer came from #336's memo rather than
	// from a run just now: nothing the memo fingerprints has changed since
	// CheckedAt, so the previous verdict still stands. A surface renders it
	// as "unchanged since <checked_at>" and offers a forced re-run
	// (VerifyOptions.Force) beside it.
	//
	// omitzero: a real run carries no key at all, so every document
	// produced before this field existed is byte-identical to what it is
	// now.
	Cached bool `json:"cached,omitzero"`
}

// VerifyEventKind identifies what a VerifyEvent carries.
type VerifyEventKind int

// The VerifyEventKind values, in emission order within a run: VerifyEvBegin
// opens it, VerifyEvFinding/VerifyEvProgress are the per-mod/per-file ticks,
// VerifyEvRepairDetail is a --fix sub-line under a finding,
// VerifyEvSyncWarning/VerifyEvVerbose are diagnostics (stderr-bound and
// -v-gated respectively). The trailing comment on each names which
// VerifyEvent field the kind's extra data lives in.
const (
	VerifyEvBegin        VerifyEventKind = iota // HasFiles
	VerifyEvFinding                             // Finding + extras; row was appended to Findings
	VerifyEvRepairDetail                        // indented sub-line; Detail pre-formatted, Fixed tone flag
	VerifyEvSyncWarning                         // stderr-bound merged-pak sync warning (Detail)
	VerifyEvVerbose                             // verbose-gated diagnostic (Detail)
	VerifyEvProgress                            // Full-tier network tick (Scope.Index/Total/ModName)
)

// verifyEventKindNames maps each VerifyEventKind to its wire name. Keep in
// declaration order.
var verifyEventKindNames = [...]string{
	VerifyEvBegin: "begin", VerifyEvFinding: "finding", VerifyEvRepairDetail: "repair_detail",
	VerifyEvSyncWarning: "sync_warning", VerifyEvVerbose: "verbose", VerifyEvProgress: "progress",
}

// String returns k's wire name.
func (k VerifyEventKind) String() string {
	if k >= 0 && int(k) < len(verifyEventKindNames) && verifyEventKindNames[k] != "" {
		return verifyEventKindNames[k]
	}
	return fmt.Sprintf("verify_event_kind(%d)", int(k))
}

// MarshalText implements encoding.TextMarshaler.
func (k VerifyEventKind) MarshalText() ([]byte, error) { return []byte(k.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (k *VerifyEventKind) UnmarshalText(b []byte) error {
	for i, n := range verifyEventKindNames {
		if n == string(b) {
			*k = VerifyEventKind(i)
			return nil
		}
	}
	return fmt.Errorf("unknown verify event kind %q", b)
}

// VerifyEvent is emitted via a verify run's EventSink as the engine works,
// so a caller can render incrementally instead of waiting for the final
// VerifyResult. Scope.Op is always OpVerify; Scope.Index/Total/ModName carry
// a VerifyEvProgress tick's position (verifyRun.emitEv fills Op, callers
// fill the rest of Scope where relevant).
type VerifyEvent struct {
	Scope
	Kind     VerifyEventKind `json:"kind"`
	HasFiles bool            `json:"has_files,omitempty"`
	Finding  VerifyFinding   `json:"finding"` // valid for VerifyEvFinding

	// Main-line extras the CLI needs beyond the finding row itself:
	Recorded          string `json:"recorded,omitempty"`           // version_mismatch / missing
	Effective         string `json:"effective,omitempty"`          // version_mismatch
	Version           string `json:"version,omitempty"`            // missing
	ExpectedCount     int    `json:"expected_count,omitempty"`     // file_count_mismatch
	ChecksumPopulated bool   `json:"checksum_populated,omitempty"` // ok main line: a --fix redownload populated a previously-missing checksum (#164)

	// Sub-line payload (VerifyEvRepairDetail):
	Detail string `json:"detail,omitempty"`
	Fixed  bool   `json:"fixed,omitempty"` // this sub-line reports a completed repair
}

// EventType implements Event.
func (VerifyEvent) EventType() string { return "verify" }

// verifyRun carries the state threaded through verify's phase methods: the
// service/game/profile/options being verified, the (nil-safe) event sink,
// and the result being built up. Every phase method appends to result via
// finding/resolveLast so there's a single place that keeps Findings and the
// emitted events in sync.
type verifyRun struct {
	ctx     context.Context
	svc     *Service
	game    *domain.Game
	profile string
	opts    VerifyOptions
	sink    EventSink
	result  *VerifyResult
}

// emitEv stamps e's Scope.Op as OpVerify and forwards it to the sink, if
// any (a nil sink discards, matching EventSink's own contract).
func (r *verifyRun) emitEv(e VerifyEvent) {
	e.Op = OpVerify
	if r.sink != nil {
		r.sink(e)
	}
}

// finding appends f to the result and emits the matching VerifyEvFinding
// event, filling in extras.Kind/Finding. Every finding row MUST be appended
// through this method (not appended to result.Findings directly) so a
// progress listener never observes a row the result doesn't also have.
func (r *verifyRun) finding(f VerifyFinding, extras VerifyEvent) {
	r.result.Findings = append(r.result.Findings, f)
	extras.Kind, extras.Finding = VerifyEvFinding, f
	r.emitEv(extras)
}

// resolveLast rewrites the most recently appended finding's Status/Note in
// place - used by a --fix repair that resolves the row it just reported
// (e.g. "missing" -> "ok" after a successful re-download). When the new
// status leaves the missing/version_mismatch family entirely (repaired, or
// demoted to no_checksum), the row's Recorded/Effective/Version extras are
// cleared too - they described the pre-repair state, so a resolved row
// (e.g. "ok") must not keep reporting it. A row that stays missing (failed
// retry) or stays version_mismatch (refused/failed repair) keeps its
// extras, since it's still the same unresolved condition.
func (r *verifyRun) resolveLast(status, note string) {
	last := &r.result.Findings[len(r.result.Findings)-1]
	// Read BEFORE the two fields are cleared below. A row this run never
	// considered fixable in the first place was REFUSED, not attempted - a
	// locked ref is the case in practice - and its existing reason is the
	// accurate one (M4, unit 8 gate review: "this --fix run already
	// attempted a repair for it" is simply untrue of a refusal, and it
	// replaced the sentence naming the lock and how to lift it. The plain
	// run - what the web UI's Health card reads - kept the good sentence,
	// so the two runs disagreed about the same row).
	refused, refusalReason := !last.Fixable, last.FixableReason
	last.Status, last.Note = status, note
	// Every resolveLast call happens inside a --fix run, AFTER that row's
	// repair was attempted, refused or completed - so whatever the row now
	// says, "a --fix run would act on this" has stopped being true of it
	// (VerifyFinding.Fixable).
	last.Fixable = false
	last.FixableReason = ""
	switch {
	case status != "missing" && status != "version_mismatch":
		// Resolved (or demoted): no reason to give.
	case refused && refusalReason != "":
		last.FixableReason = refusalReason
	default:
		// Still unresolved after a repair was actually attempted: say so,
		// rather than leaving a not-fixable row with no reason at all.
		last.FixableReason = "this --fix run already attempted a repair for it"
	}
	if status != "missing" && status != "version_mismatch" {
		last.Recorded, last.Effective, last.Version = "", "", ""
	}
}

// lockedSkipDetail renders a repair-refusal error as --fix's sub-line: the
// refusal sentence behind a "--fix skipped: " lead-in, with the ErrModLocked
// sentinel trimmed back off. All three file repairs (missing, no_checksum,
// needs_reingest) refuse through the SAME gate (verify_repair.go's #325
// check), so they render it the same way rather than three times over -
// leaving the sentinel on would read "mod is locked: <Name> is locked at
// v1.0 ...", the stutter lockedRefUnlockOnlyMessage exists to avoid.
func lockedSkipDetail(err error) string {
	return "--fix skipped: " + strings.TrimPrefix(err.Error(), ErrModLocked.Error()+": ")
}

// redownloadRepairs reports whether --fix's redownload repair applies to
// mod - the ONE gate the missing / no_checksum / needs_reingest repairs
// share (`r.opts.Fix && mod.SourceID != domain.SourceLocal` at each site):
// a locally imported mod has no source to fetch from, so there is nothing
// to redownload. Declared here so VerifyFinding.Fixable is computed from
// the same expression the repair is gated on rather than a copy of it.
func redownloadRepairs(mod *domain.InstalledMod) bool {
	return mod.SourceID != domain.SourceLocal
}

// versionMismatchRepairs reports whether --fix's version repair
// (repairModVersion) applies to mod with profile ref ref: a non-local
// source, AND an unlocked ref. #97: a locked ref's Version IS the lock's
// target, so rewriting it would silently move what the lock means instead
// of fixing anything - the mismatch stays reported, only the repair is
// refused (versionPass).
func versionMismatchRepairs(mod *domain.InstalledMod, ref *domain.ModReference) bool {
	if !redownloadRepairs(mod) {
		return false
	}
	return ref == nil || !ref.Locked
}

// staleCompileRefusal is the stale_compile row's reason half. Its Fixable
// is !opts.Fix - a plain run reports the staleness that --fix's own
// merged-pak resync (syncMergedPakPass) would regenerate - so on a --fix
// run the row is already past being actionable.
func staleCompileRefusal(fixing bool) string {
	if !fixing {
		return ""
	}
	return "this --fix run already resynced the merged artifact"
}

// verifyGated is the beginOp-gated entry point for a verify run: a --fix run
// mutates (repairs, redownloads, removes stale deployments) so it takes the
// same one-slot mutation semaphore every other mutating flow does; a plain
// (non-Fix) run is read-only and skips the gate entirely, matching every
// other query's freedom to run concurrently with a mutation.
//
// Unexported (final review, Important #3 / #301): VerifyReport is the only
// production entry point (cmd/lmm/verify.go calls it, not this), and core's
// own tests drive verify through it too.
func (s *Service) verifyGated(ctx context.Context, game *domain.Game, profile string, opts VerifyOptions, sink EventSink) (*VerifyResult, error) {
	if opts.Fix {
		release, err := s.beginOp(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	return s.verifyMemoized(ctx, game, profile, opts, sink)
}

// verifyMemoized is verify plus #336's memo: an installation whose
// fingerprint has not moved since the last run is answered from that run
// instead of walking it again.
//
// Mission Control hydrates on every route change, every job completion and
// every profile switch, and each hydrate ran the full tier - a source query
// per mod and a cache stat per file, for state that had not changed. It was
// honest and it was the one place a big install felt slow.
//
// FOUR shapes always run, and none of them is a cache-busting nicety:
//
//   - Force: `lmm verify` typed by a user, and the Health card's Re-verify.
//     A forced run also REPLACES the memo, so the next hydrate is handed
//     the fresh answer rather than re-running.
//   - Fix: a repair changes state; answering it from a memo would report a
//     repair that never happened.
//   - ModFilter: a single-mod run is not the whole-profile answer, and
//     storing it under the same key would let it stand in for one.
//   - A non-nil sink: a memo hit has no run, so it has no progress events
//     to emit. A caller that asked to watch gets something to watch.
//
// A fingerprint that cannot be computed (an unreadable directory, a failed
// DB read) is not a match and not an error: the run simply happens, which
// is the behaviour that existed before the memo.
func (s *Service) verifyMemoized(ctx context.Context, game *domain.Game, profile string, opts VerifyOptions, sink EventSink) (*VerifyResult, error) {
	if opts.Fix || opts.ModFilter != "" || sink != nil {
		return s.verify(ctx, game, profile, opts, sink)
	}

	key := verifyMemoKey(game.ID, profile, opts.Tier)
	fingerprint, err := s.verifyFingerprint(ctx, game, profile)
	if err != nil {
		s.logger().Debug("verify memo disabled for this run", "game", game.ID, "profile", profile, "err", err)
		return s.verify(ctx, game, profile, opts, sink)
	}

	if !opts.Force {
		if hit := s.verifyMemoLookup(key, fingerprint); hit != nil {
			// A copy, with the ORIGINAL CheckedAt: the answer really was
			// computed then, and a surface saying "unchanged since ..."
			// needs that moment, not this one. It is a DEEP copy (the same
			// cloneVerifyResult the store side uses since #366): without
			// it every hit would hand out the stored entry's own backing
			// array. Nothing outside core writes to it today, but "nobody
			// appends to this slice" is a comment, and one allocation per
			// hit removes the class instead (review N6).
			cached := cloneVerifyResult(hit)
			cached.Cached = true
			return cached, nil
		}
	}

	result, err := s.verify(ctx, game, profile, opts, sink)
	if err != nil {
		return result, err // a partial (e.g. cancelled) result is never memoised
	}
	s.verifyMemoStore(key, fingerprint, result)
	return result, nil
}

// verify runs the verify engine for game/profile per opts, reporting
// incremental progress via sink (nil-safe: pass nil to skip events
// entirely). Called only through verifyGated, which applies the --fix
// mutation gate above.
//
// CONTRACT: verify never compares checksum VALUES; it only checks presence.
// A recorded checksum is reported "ok" whatever it says, and only an EMPTY
// one is "no_checksum" (perFileWalk's `f.Checksum == ""` test is the sole
// reader of the column here). That is load-bearing, not incidental: the
// values stored for one file are not all the same fingerprint - a fresh
// download records the archive-level md5, while a cache-warm install records
// checksumFromCache's fold over the entry's members for anything with no
// retained original to re-hash (see its doc comment). A future deep tier
// that compares values must re-ingest or re-derive first, or it will flag
// every cache-warm install; TestService_ApplyInstall_KeepCacheReinstall_-
// VerifyReportsOk pins the contract from the install side.
//
// #224 Task 6 completes the engine: the fix-mode merged-pak resync and the
// deploy-convergence sweep (convergeDeployedFiles) that close out every run,
// including the #217 empty-profile path, which now runs nothing BUT that
// sweep.
func (s *Service) verify(ctx context.Context, game *domain.Game, profile string, opts VerifyOptions, sink EventSink) (*VerifyResult, error) {
	result := &VerifyResult{CheckedAt: time.Now().UTC()}
	r := &verifyRun{ctx: ctx, svc: s, game: game, profile: profile, opts: opts, sink: sink, result: result}

	files, err := s.GetFilesWithChecksums(ctx, game.ID, profile)
	if err != nil {
		return nil, fmt.Errorf("getting files: %w", err)
	}

	result.HasFiles = len(files) > 0
	r.emitEv(VerifyEvent{Kind: VerifyEvBegin, HasFiles: result.HasFiles})

	if !result.HasFiles {
		// #269: an all-external profile has no checksummed files at all, so
		// its presence tier has to run on this branch too - otherwise a
		// profile of nothing but Steam Workshop items would verify as
		// "nothing to check" and never notice an unsubscribed one.
		installedMods, err := s.GetInstalledMods(ctx, game.ID, profile)
		if err != nil {
			return nil, fmt.Errorf("getting installed mods: %w", err)
		}
		if err := r.externalPresencePass(installedMods); err != nil {
			return result, err
		}
		// #217: doVerify still runs a deploy-convergence sweep here even
		// with no checksummed files at all (a game dir can hold stray
		// lmm-deployed files after everything is uninstalled). The
		// checksum/version/count passes all have nothing to do here, so
		// this path is entirely the convergence pass - no sync phase (that
		// only ever reacts to a --fix repair that just ran, and nothing
		// ran here to react to).
		//
		// #359's loader tier runs here too, for externalPresencePass's
		// reason: a loader is a property of the GAME, not of its mods, so
		// an empty profile's loader can still be missing, the wrong
		// version, or never have run - and finding that out before
		// installing anything is exactly when it helps most.
		r.loaderPass(installedMods)
		r.convergencePass()
		return result, nil
	}

	if err := r.fileCountPrePass(files); err != nil {
		// Cancelled mid-pass: return the partial result already
		// accumulated, same contract Task 3's brief specifies.
		return result, err
	}

	// #97 (Task 8 of the original CLI): load the profile once, up front, so
	// the version pass below can look up each ref's lock state
	// (Profile.FindRef) without reloading the profile per mod. A missing/
	// unreadable profile is treated as unlocked - FindRef's nil-receiver-
	// safe behavior on a nil *domain.Profile does the right thing here
	// without an extra guard.
	prof, _ := config.LoadProfile(s.ConfigDir(), game.ID, profile)

	installedMods, err := s.GetInstalledMods(ctx, game.ID, profile)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}

	if err := r.externalPresencePass(installedMods); err != nil {
		// Cancelled mid-pass: return the partial result already
		// accumulated, same contract Task 3's brief specifies.
		return result, err
	}

	r.mergedPakStalenessPass()
	if err := r.conversionOutcomesPass(installedMods); err != nil {
		// Cancelled mid-pass: return the partial result already
		// accumulated, same contract Task 3's brief specifies.
		return result, err
	}

	if err := r.versionPass(installedMods, prof); err != nil {
		// Cancelled mid-pass: return the partial result already
		// accumulated, same contract Task 3's brief specifies.
		return result, err
	}

	if err := r.perFileWalk(files, prof); err != nil {
		// Cancelled mid-pass: return the partial result already
		// accumulated, same contract Task 3's brief specifies.
		return result, err
	}

	// #224 Task 6: --fix's merged-pak resync and the deploy-convergence
	// sweep both close out the run, in that order - ported verbatim from
	// doVerify (originally cmd/lmm/verify.go:811-835). The sync is a --fix-
	// only reaction to repairs the passes above may have just made (a
	// version-mismatch repair or a redownload can change a merge's inputs);
	// convergence then reconciles the game dir against the resulting
	// reality, so it always runs AFTER the sync, in both modes.
	if r.opts.Fix {
		r.syncMergedPakPass()
	}
	// #359: after the per-file walk, so the loader's "did it run?" question
	// is asked about the deployment the passes above have just described.
	r.loaderPass(installedMods)
	r.convergencePass()

	return result, nil
}

// externalPresencePass is #269's verify tier for EXTERNAL mods: the only
// thing lmm can honestly check about a Steam Workshop item is that the
// directory Steam owns is still there and still has something in it.
//
// There is no checksum tier for these (lmm never downloaded the bytes, so
// it has nothing recorded to compare against - which is why versionPass
// skips them too), and there is no --fix: both repairs verify offers,
// redownload and checksum backfill, presuppose an lmm-owned cache entry.
// A missing directory is reported and left for the user to resolve in the
// Steam client, which is the only place it CAN be resolved.
func (r *verifyRun) externalPresencePass(installedMods []domain.InstalledMod) error {
	for i := range installedMods {
		mod := &installedMods[i]
		if !mod.External {
			continue
		}
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if r.opts.ModFilter != "" && mod.ID != r.opts.ModFilter {
			continue
		}
		if externalContentPresent(mod.ExternalPath) {
			continue
		}
		r.result.Issues++
		r.finding(VerifyFinding{
			ModID: mod.ID, ModName: mod.Name, Status: "external_missing",
			Note:          "Steam no longer has this item on disk - it may have been unsubscribed",
			FixableReason: "lmm does not own this item's files, so there is nothing for --fix to redownload - resubscribe in the Steam client, or uninstall it from lmm",
		}, VerifyEvent{})
	}
	return nil
}

// externalContentPresent reports whether path is a directory with at least
// one entry. An empty directory counts as absent: Steam leaves one behind
// after an unsubscribe, and reporting it as present would hide exactly the
// case this pass exists to catch.
func externalContentPresent(path string) bool {
	if path == "" {
		return false
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	return len(entries) > 0
}

// syncMergedPakPass ports cmd/lmm/verify.go's fix-mode merged-pak resync
// call verbatim (originally doVerify lines 811-825): --fix can repair a
// VERSION MISMATCH (repairModVersion moves the cache dir and the recorded
// version) or redownload a file whose content has since changed upstream
// (redownloadModFile) - both are merge-fingerprint inputs with no other
// seam to catch them. SyncMergedPak's own guard already no-ops for a
// non-DeployCompile game, so this dials it unconditionally under --fix.
// Only called when opts.Fix - a plain verify pass makes no changes for a
// sync to react to.
func (r *verifyRun) syncMergedPakPass() {
	syncWarnings, err := r.svc.syncMergedPak(r.ctx, r.game, r.profile)
	if err != nil {
		r.emitEv(VerifyEvent{Kind: VerifyEvSyncWarning, Detail: fmt.Sprintf("could not sync merged pak: %v", err)})
		return
	}
	for _, w := range syncWarnings {
		r.emitEv(VerifyEvent{Kind: VerifyEvSyncWarning, Detail: w})
	}
}

// convergencePass ports cmd/lmm/verify.go's reportConvergencePass verbatim
// (originally cmd/lmm/verify.go:865-916): reconciles the game dir against
// current reality (convergeDeployedFiles) - deployed_files rows no longer
// provided by any installed mod, and dangling cache-rooted symlinks with no
// row at all - and reports every stale/dangling path found. dryRun mirrors
// !opts.Fix: a plain verify pass reports candidates without mutating
// anything (each becomes a stale_deployment warning); --fix acts, and each
// path convergeDeployedFiles actually removed becomes a fixed_stale_
// deployment row instead - resolved, not outstanding, the same convention
// every other successful --fix repair in this engine follows (it does NOT
// add to Warnings). Runs both in the main flow (after perFileWalk/the
// fix-mode sync above) and, for the #217 empty-profile path (HasFiles
// false), as the entirety of that run beyond the Begin event - see verify's
// own doc comment.
//
// A fixed_stale_deployment row needs no event field of its own for this: the
// CLI renders the WHOLE main line green for it purely by keying on
// Finding.Status (unlike, say, a version repair, which prints a plain
// finding line with a separate green sub-line) - Task 7 wires that
// rendering contract up on the CLI side.
//
// Per-item convergence failures (an Undeploy or sweep os.Remove that
// failed) are joined into one error by convergeDeployedFiles rather than
// aborting the whole pass (ConvergeResult's own doc comment) - surfaced
// here the same way every other per-item problem in this engine is: a
// skipped row plus a warning, not a fatal verify failure.
func (r *verifyRun) convergencePass() {
	convResult, convErr := r.svc.convergeDeployedFiles(r.ctx, r.game, r.profile, !r.opts.Fix)
	if convResult != nil {
		for _, cf := range convResult.Removed {
			if r.opts.Fix {
				r.finding(VerifyFinding{ModID: cf.ModID, FileID: cf.Path, Status: "fixed_stale_deployment", Note: cf.Reason}, VerifyEvent{})
				continue
			}
			r.result.Warnings++
			r.finding(VerifyFinding{ModID: cf.ModID, FileID: cf.Path, Status: "stale_deployment", Note: cf.Reason, Fixable: true}, VerifyEvent{})
		}
	}
	if convErr != nil {
		for _, e := range unwrapJoined(convErr) {
			r.result.Warnings++
			r.finding(VerifyFinding{Status: "skipped", Note: fmt.Sprintf("convergence: %v", e)}, VerifyEvent{})
		}
	}
}

// fileCountPrePass ports cmd/lmm/verify.go's per-mod file-count mismatch
// check verbatim (originally doVerify lines 339-415): report when a mod's
// cache entry exists but is empty (0 files) despite the DB recording more
// than zero expected files. Runs before perFileWalk, matching the CLI's
// current phase order.
func (r *verifyRun) fileCountPrePass(files []DeployedFile) error {
	fileCountByMod := make(map[string]int)
	for _, f := range files {
		key := domain.ModKey(f.SourceID, f.ModID)
		if r.opts.ModFilter != "" && f.ModID != r.opts.ModFilter {
			continue
		}
		fileCountByMod[key]++
	}

	gameCache := r.svc.GetGameCache(r.game)
	reportedMismatch := make(map[string]bool)
	for key, expectedCount := range fileCountByMod {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if expectedCount == 0 {
			continue
		}
		sourceID, modID, _ := strings.Cut(key, ":")
		mod, err := r.svc.GetInstalledMod(r.ctx, sourceID, modID, r.game.ID, r.profile)
		if err != nil {
			// A not-installed mod (an orphaned checksum row - perFileWalk
			// below reports this exact case itself, as "skipped") is a
			// normal, silent skip here; reporting it again in this
			// pre-pass would just duplicate that warning. Any OTHER lookup
			// error (a genuine DB failure) is NOT a normal skip and must
			// not be swallowed (epic98 audit Finding 5).
			if !errors.Is(err, domain.ErrModNotFound) {
				r.result.Warnings++
				r.finding(VerifyFinding{ModID: modID, Status: "skipped", Note: err.Error()}, VerifyEvent{})
			}
			continue
		}
		if r.opts.ModFilter != "" && mod.ID != r.opts.ModFilter {
			continue
		}
		// #197 I4 fix: a DeployCompile game's ".exmodz" mod is ingested as
		// validate+retain ONLY - it has zero deployment members of its own
		// by design (the shared merged pak, checked separately, is what
		// actually deploys), so ListFiles == 0 here is correct, healthy
		// state, not a mismatch.
		if r.game.DeployMode == domain.DeployCompile && HasRetainedCompileSource(gameCache, r.game.ID, mod.SourceID, mod.ID, mod.Version, mod.FileIDs) {
			continue
		}
		cacheExists := gameCache.Exists(r.game.ID, mod.SourceID, mod.ID, mod.Version)
		if !cacheExists {
			continue
		}
		cachedFiles, err := gameCache.ListFiles(r.game.ID, mod.SourceID, mod.ID, mod.Version)
		if err != nil {
			// A real filesystem error walking the cache dir (permission
			// denied, I/O error) is not the same as "cache is empty" - the
			// FILE COUNT MISMATCH case just below - and must be surfaced,
			// not silently treated as "nothing to report" (audit Finding 5).
			if !reportedMismatch[key] {
				r.result.Warnings++
				r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, Status: "skipped", Note: err.Error()}, VerifyEvent{})
				reportedMismatch[key] = true
			}
			continue
		}
		actualCount := len(cachedFiles)
		if expectedCount > 0 && actualCount == 0 {
			if !reportedMismatch[key] {
				r.result.Warnings++
				r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, Status: "file_count_mismatch",
					FixableReason: "there is nothing to repair a file-count mismatch with"}, VerifyEvent{ExpectedCount: expectedCount})
				reportedMismatch[key] = true
			}
		}
	}
	return nil
}

// perFileWalk ports cmd/lmm/verify.go's main per-file loop verbatim
// (originally doVerify lines 678-854): for each checksummed file, report
// unknown-mod as "skipped", an absent cache entry as "missing", a
// stored-but-empty checksum as "no_checksum", and anything else as "ok".
// Checked is incremented once per row considered (after the ModFilter
// check, before any of those outcomes). #224 Task 4 adds the MISSING/NO
// CHECKSUM/NEEDS REINGEST --fix redownload repairs inline at each site;
// Task 5 adds the one repair this walk does NOT own (version_mismatch's,
// in versionPass below).
func (r *verifyRun) perFileWalk(files []DeployedFile, prof *domain.Profile) error {
	gameCache := r.svc.GetGameCache(r.game)
	for _, f := range files {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if r.opts.ModFilter != "" && f.ModID != r.opts.ModFilter {
			continue
		}
		r.result.Checked++

		mod, err := r.svc.GetInstalledMod(r.ctx, f.SourceID, f.ModID, r.game.ID, r.profile)
		if err != nil {
			r.result.Warnings++
			r.finding(VerifyFinding{ModID: f.ModID, FileID: f.FileID, Status: "skipped"}, VerifyEvent{})
			continue
		}
		// #325: every repair below redownloads into the RECORDED version's
		// cache slot, so each one has to know whether that record is a lock.
		ref := prof.FindRef(f.SourceID, f.ModID)

		// #221 lazy migration: a convert-eligible pak whose cache entry
		// predates pak retention (deployable pak present, no retained
		// source) needs re-ingesting before it can ever participate in a
		// merge - PakNeedsReingest is the one place that kind/retained
		// detection lives (verify must not reimplement it). Ported from
		// cmd/lmm/verify.go's doVerify (originally lines 696-753), including
		// the --fix re-ingest block below (#224 Task 4).
		need, nerr := r.svc.PakNeedsReingest(r.ctx, r.game, mod, f.FileID)
		if nerr != nil {
			// A real check failure (a Stat/ListFiles error, not "nothing
			// ingested yet") - not counted as a warning and not fatal to
			// the rest of this row's checks below, but not silently
			// dropped either: surfaced as a verbose diagnostic event, same
			// as every other soft diagnostic in this codebase. The exact
			// text (sans the CLI's own "  (verbose) " prefix) is a frozen
			// contract - the CLI renderer depends on it verbatim.
			r.emitEv(VerifyEvent{Kind: VerifyEvVerbose, Detail: fmt.Sprintf("could not check pak-reingest status for %s (%s): %v", mod.Name, f.FileID, nerr)})
		} else if need {
			note := "pak predates conversion support - run 'lmm verify --fix' to re-ingest"
			if mod.SourceID == domain.SourceLocal {
				note = "re-import the archive to enable conversion"
			}
			r.result.Warnings++
			r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "needs_reingest", Note: note,
				Fixable: redownloadRepairs(mod), FixableReason: redownloadRefusal(mod)}, VerifyEvent{})
			// #224 Task 4: --fix re-ingests through the same redownload path
			// as MISSING/NO CHECKSUM below - the widened ingest predicate
			// retains the source this time, and a later sync picks it up.
			// Ported verbatim from doVerify (originally lines 729-751).
			// Unlike MISSING/NO CHECKSUM, only the error is branched on -
			// redownloadModFile's persisted flag is irrelevant here (a
			// re-ingest either retains the source or it doesn't reach this
			// far).
			if r.opts.Fix && mod.SourceID != domain.SourceLocal {
				if _, rerr := r.redownloadModFile(r.ctx, mod, f.FileID, ref); rerr != nil {
					if errors.Is(rerr, ErrModLocked) {
						// #325 (review I2): the SAME distinction the
						// missing branch below makes. The lock gate
						// declined before the download, so nothing failed
						// and a retry declines identically forever -
						// calling it a failure tells the user to retry,
						// and rendering the raw error stutters the
						// ErrModLocked sentinel against the sentence's own
						// "<Name> is locked at ..." head.
						r.resolveLast("needs_reingest", "locked")
						r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: lockedSkipDetail(rerr)})
					} else {
						r.resolveLast("needs_reingest", fmt.Sprintf("re-ingest failed: %v", rerr))
						r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Re-ingest failed: %v", rerr)})
					}
				} else {
					// Same convention as MISSING/NO CHECKSUM's own --fix
					// success path below, and stale_deployment's
					// fixed_stale_deployment: a resolved problem is not left
					// reading as an outstanding one in the SAME run that
					// just fixed it - rewrite the row to a fixed-state
					// status/note and back the count out.
					r.resolveLast("fixed_needs_reingest", "re-ingested with retained source for pak conversion")
					r.result.Warnings--
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: "Re-ingested for pak conversion", Fixed: true})
				}
			}
			continue
		}

		cacheExists := gameCache.Exists(r.game.ID, mod.SourceID, mod.ID, mod.Version)
		if !cacheExists {
			r.result.Issues++
			r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "missing", Version: mod.Version,
				Fixable: redownloadRepairs(mod), FixableReason: redownloadRefusal(mod)}, VerifyEvent{Version: mod.Version})
			// #224 Task 4: ported verbatim from doVerify (originally lines
			// 765-799).
			if r.opts.Fix && mod.SourceID != domain.SourceLocal {
				persisted, err := r.redownloadModFile(r.ctx, mod, f.FileID, ref)
				switch {
				case errors.Is(err, ErrModLocked):
					// #325: refused, not failed - the slot is untouched and
					// the row keeps reporting MISSING. Note is the short,
					// machine-checkable reason versionPass's own lock
					// refusal already uses; the sentence is the text
					// surface (VerifyEvRepairDetail).
					r.resolveLast("missing", "locked")
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: lockedSkipDetail(err)})
				case err != nil:
					r.resolveLast("missing", err.Error())
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Re-download failed: %v", err)})
				case persisted:
					r.resolveLast("ok", "")
					r.result.Issues--
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: "Re-downloaded OK", Fixed: true})
				default:
					// The re-download restored the cache - the MISSING
					// issue is genuinely repaired - but no checksum was
					// available to store, so the row remains a NO CHECKSUM
					// warning (#164: don't report "ok" for a write that
					// never happened).
					r.resolveLast("no_checksum", "re-downloaded, but no checksum was available to store")
					r.result.Issues--
					r.result.Warnings++
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: "Re-downloaded, but no checksum was available to store - NO CHECKSUM remains"})
				}
			}
			continue
		}

		if f.Checksum == "" {
			// #224 Task 4: --fix's redownload-to-populate-checksum repair
			// REPLACES the plain no_checksum row emission below - ported
			// verbatim from doVerify (originally lines 804-846), including
			// the "ok"+ChecksumPopulated main-line emission on success.
			if r.opts.Fix && mod.SourceID != domain.SourceLocal {
				persisted, err := r.redownloadModFile(r.ctx, mod, f.FileID, ref)
				switch {
				case errors.Is(err, ErrModLocked):
					// #325 (review I2): refused, not failed - see the
					// needs_reingest arm above. The warning stands (the
					// checksum is still unpopulated) with the short,
					// machine-checkable note; the sentence is the text
					// surface.
					r.result.Warnings++
					r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "no_checksum", Note: "locked"}, VerifyEvent{})
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: lockedSkipDetail(err)})
				case err != nil:
					r.result.Warnings++
					r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "no_checksum", Note: err.Error()}, VerifyEvent{})
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Re-download to populate checksum failed: %v", err)})
				case persisted:
					r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "ok"}, VerifyEvent{ChecksumPopulated: true})
				default:
					// The download succeeded but produced no checksum to
					// store - nothing was written, so the warning stands
					// with an honest reason (#164: "checksum populated" was
					// a lie here, and the summary lied with it).
					r.result.Warnings++
					r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "no_checksum", Note: "re-downloaded, but no checksum was available to store"}, VerifyEvent{})
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: "Re-downloaded, but no checksum was available to store"})
				}
				continue
			}
			r.result.Warnings++
			r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "no_checksum",
				Fixable: redownloadRepairs(mod), FixableReason: redownloadRefusal(mod)}, VerifyEvent{})
			continue
		}

		// Cache exists and checksum stored - consider OK.
		r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: f.FileID, Status: "ok"}, VerifyEvent{})
	}
	return nil
}

// mergedPakStalenessPass ports cmd/lmm/verify.go's merged-pak staleness
// check verbatim (originally doVerify lines 449-474): for a DeployCompile
// game, compares the profile's merged pak's recorded fingerprint against
// the game's CURRENT enabled-mod set/order/versions/base pak. Entirely
// local/offline - runs regardless of opts.Tier. Independent of ModFilter:
// the merged pak is profile-scoped, not per-mod, so a single-mod verify
// still checks it.
func (r *verifyRun) mergedPakStalenessPass() {
	if r.game.DeployMode != domain.DeployCompile {
		return
	}

	staleUpd, serr := r.svc.CheckMergedPakStaleness(r.ctx, r.game, r.profile)
	if serr != nil {
		r.result.Warnings++
		r.finding(VerifyFinding{Status: "skipped", Note: fmt.Sprintf("could not check merged pak staleness: %v", serr)}, VerifyEvent{})
	}
	r.result.Checked++
	if staleUpd != nil {
		r.result.Warnings++
		r.finding(VerifyFinding{ModID: staleUpd.InstalledMod.ID, ModName: staleUpd.InstalledMod.Name, Status: "stale_compile",
			Note: staleUpd.RecompileReason, Fixable: !r.opts.Fix, FixableReason: staleCompileRefusal(r.opts.Fix)}, VerifyEvent{})
	}
}

// conversionOutcomesPass ports cmd/lmm/verify.go's per-mod pak-conversion
// outcome report verbatim (originally doVerify lines 484-514): reports
// every non-Converted entry recorded on the merged pak's own fingerprint
// (mergedPakOutcomes) - a mod whose pak failed to convert stays
// raw-deployed, and the user needs to know why. Independent of the
// staleness check above: a merge can be perfectly up to date while still
// recording a PRIOR conversion failure for one of its contributing mods. A
// warning, not an issue - deploying raw is a documented, working fallback,
// not corruption.
func (r *verifyRun) conversionOutcomesPass(installedMods []domain.InstalledMod) error {
	if r.game.DeployMode != domain.DeployCompile {
		return nil
	}

	modNames := make(map[string]string, len(installedMods))
	for _, m := range installedMods {
		modNames[domain.ModKey(m.SourceID, m.ID)] = m.Name
	}

	outcomes, ok := r.svc.mergedPakOutcomes(r.ctx, r.game, r.profile)
	if !ok {
		return nil
	}
	for _, entry := range outcomes {
		// The check belongs to the loop that EMITS findings, not to the
		// in-memory name-map build above (final-review Minor 2): a pass with
		// no installed mods still walks the outcomes, and this is the loop a
		// reader expects the cancellation guard to protect.
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if entry.Converted {
			continue
		}
		// The fingerprint entry can outlive the mod it names - if the mod
		// was since uninstalled, modNames has no entry and name would be
		// blank. Fall back to the raw entry.ModID, same as the
		// unknown-mod skip in perFileWalk does when a checksum row's mod
		// can't be found - a stable, non-empty identifier beats silence
		// either way.
		name := modNames[domain.ModKey(entry.SourceID, entry.ModID)]
		if name == "" {
			name = entry.ModID
		}
		r.result.Warnings++
		r.finding(VerifyFinding{ModID: entry.ModID, ModName: name, Status: "conversion_failed", Note: entry.FailReason,
			FixableReason: "a conversion is only retried when a merge input changes - reinstall the mod to retry it"}, VerifyEvent{})
	}
	return nil
}

// versionPass ports cmd/lmm/verify.go's per-mod version-record check
// verbatim (originally doVerify lines 516-676, minus the --fix repair
// branch - Task 5): for each source-backed installed mod with recorded
// FileIDs, compares the recorded Version against what the source currently
// reports for those FileIDs (issue #94's detection half). Gated on
// opts.Tier == VerifyFull - this is the engine's one network-touching
// phase, and the only one skipped entirely under VerifyLocal.
//
// Emits VerifyEvProgress at the top of every mod's iteration (a new event
// not currently rendered by the CLI) and honors ctx cancellation between
// mods: on cancellation the loop stops and returns
// ctx.Err(), leaving the caller (verify) to return the partial result
// already accumulated rather than a phantom "everything checked out"
// result.
func (r *verifyRun) versionPass(installedMods []domain.InstalledMod, prof *domain.Profile) error {
	if r.opts.Tier != VerifyFull {
		return nil
	}

	for i := range installedMods {
		mod := &installedMods[i]
		r.emitEv(VerifyEvent{Kind: VerifyEvProgress, Scope: Scope{Index: i + 1, Total: len(installedMods), ModName: mod.Name}})
		if err := r.ctx.Err(); err != nil {
			return err
		}

		if r.opts.ModFilter != "" && mod.ID != r.opts.ModFilter {
			continue
		}
		// Nothing to check against: local imports and manual downloads have
		// no source to query, and a mod with no recorded file IDs predates
		// even the buggy stamping this check exists to catch.
		// #269: an external mod has no source file list to check a recorded
		// version against - and no lmm-owned cache entry a repair could act
		// on. Its own check is externalPresencePass.
		if mod.SourceID == domain.SourceLocal || mod.ManualDownload || mod.External || len(mod.FileIDs) == 0 {
			continue
		}

		ref := prof.FindRef(mod.SourceID, mod.ID)

		sourceFiles, err := r.svc.GetModFiles(r.ctx, mod.SourceID, SourceMappedMod(r.game, &mod.Mod))
		if err != nil {
			r.result.Warnings++
			r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, Status: "skipped", Note: fmt.Sprintf("could not check version: %v", err)}, VerifyEvent{})
			continue
		}

		var matched []*domain.DownloadableFile
		for _, id := range mod.FileIDs {
			for j := range sourceFiles {
				if sourceFiles[j].ID == id {
					matched = append(matched, &sourceFiles[j])
					break
				}
			}
		}

		if len(matched) == 0 {
			r.result.Warnings++
			r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, Status: "version_unverifiable",
				FixableReason: "there is nothing to check the recorded version against"}, VerifyEvent{})
			continue
		}

		// When every matched file reports an empty Version (custom sources
		// whose mappings carry no per-file versions), the fallback inside
		// EffectiveInstalledVersion returns mod.Version and this comparison
		// passes vacuously - a deliberate quiet OK, not a missed
		// VERSION UNVERIFIABLE: install-time stamping applies the same
		// fallback, so a per-file version mis-stamp cannot exist for
		// versionless sources.
		effective := domain.EffectiveInstalledVersion(mod.Version, matched)
		if effective != mod.Version {
			recorded := mod.Version
			r.result.Issues++
			r.finding(VerifyFinding{
				ModID: mod.ID, ModName: mod.Name, Status: "version_mismatch",
				Recorded: recorded, Effective: effective,
				Fixable:       versionMismatchRepairs(mod, ref),
				FixableReason: versionMismatchRefusal(mod, ref),
			}, VerifyEvent{Recorded: recorded, Effective: effective})
			if r.opts.Fix && mod.SourceID != domain.SourceLocal {
				// #97 (Task 8): a locked ref's Version is the lock's
				// TARGET, not a repairable mistake - rewriting it here
				// would silently move what the lock means instead of
				// fixing anything. The mismatch stays reported/counted
				// above (verify stays honest about state); only the
				// repair itself is refused.
				if ref != nil && ref.Locked {
					// #142 round 5: name the source/profile in both remedies -
					// same "copy-paste acts on the wrong target" fix already
					// applied to the core gates (internal/core/update.go's
					// LockedRefRefusalError) and the sibling-repair warning
					// below - a bare 'lmm mod lock <id> <version>' would
					// resolve against the active profile/an ambiguous source
					// if this mod's lock lives elsewhere.
					// #311 (review I1): the sixth site the issue's own
					// comment names. It was hand-worded and had drifted
					// from the canonical sentence in three ways the
					// unification exists to prevent (an em-dash, an
					// interposed clause, a trailing full stop). It is
					// LockedRefRefusalError's KIND - this gate refuses
					// because the RECORD differs from what the lock names,
					// so moving the lock genuinely unblocks it - and
					// lockedRefRefusalMessage is that constructor's
					// sentence half, i.e. the same builder without an
					// ErrModLocked prefix to trim back off.
					refusal := "--fix skipped: " + lockedRefRefusalMessage(mod.Mod, r.profile, ref)
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: refusal})
					// The Note field already exists on this contract
					// (repair-failure/rename-blocked detail) - this is an
					// additive use of it, not a new field. Kept short
					// ("locked") rather than the full refusal sentence
					// since a --json caller mainly needs the
					// machine-checkable reason; the sentence itself is the
					// text-mode surface. Status stays version_mismatch -
					// the repair was refused, not performed.
					r.resolveLast("version_mismatch", "locked")
					continue
				}
				note, siblingFailures, repairErr := r.repairModVersion(r.ctx, mod, effective)
				// Sibling failures are warnings regardless of how the
				// PRIMARY row's own repair turned out - a failed sibling
				// repair is real, surfaced work left undone, and the
				// warnings counter must reflect it either way, not just
				// when the primary repair also happened to succeed.
				r.result.Warnings += siblingFailures
				if repairErr != nil {
					// The PRIMARY row keeps reporting version_mismatch
					// (status/issues stay as already recorded above), but
					// the repair may have PARTIALLY succeeded: a step-4
					// re-link failure surfaces here after the cache
					// rename and the DB/profile version correction have
					// already landed and are not rolled back (see
					// repairModVersion's doc). The failure reason itself
					// (audit Finding 7) and note can still carry a
					// successful SIBLING repair - see repairModVersion's
					// doc comment - and dropping either here would make
					// them invisible to a --json caller even though they
					// genuinely happened.
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Repair failed: %v", repairErr)})
					repairNote := "repair failed: " + repairErr.Error()
					if note != "" {
						repairNote += "; " + note
					}
					r.resolveLast("version_mismatch", repairNote)
				} else {
					r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: fmt.Sprintf("Repaired: %s → %s", recorded, effective), Fixed: true})
					if note != "" {
						r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: "Note: " + note})
					}
					r.resolveLast("ok", note)
					r.result.Issues--
				}
			}
			continue
		}

		// Recorded version matches what the source reports - OK, but not
		// reported as its own row (same quiet-ok convention as the file
		// loop) - UNLESS the mod is locked and the DB version hasn't yet
		// converged to the lock's target (ref.Version): that's expected
		// drift pending a `profile apply`, not corruption, so it gets its
		// own informational note instead of pure silence. Never counted in
		// issues or warnings - it isn't a problem.
		if ref != nil && ref.Locked && ref.Version != mod.Version {
			convergenceNote := fmt.Sprintf("lock pending convergence (installed v%s, locked v%s)", mod.Version, ref.Version)
			r.finding(VerifyFinding{ModID: mod.ID, ModName: mod.Name, Status: "ok", Note: convergenceNote}, VerifyEvent{})
		}
		r.result.Checked++
	}

	// The LAST mod's own iteration (its repair included) can cancel without
	// ever reaching the head-of-loop check above - without this, that
	// cancellation would render as an ordinary "repair failed: context
	// canceled" finding and versionPass would still return nil (task 18
	// re-review round 2, NEW-3).
	if err := r.ctx.Err(); err != nil {
		return err
	}
	return nil
}
