package core

import (
	"encoding/json"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// Op names the operation an event belongs to.
type Op string

// The values below name every operation an event's Scope can belong to -
// the value cmd/lmm's -v/JSON event stream and OpProfileApply/OpProfileSync's
// package comments key off. Most are self-explanatory from the command they
// back (OpInstall = `lmm install`, OpDeploy = `lmm profile apply`'s deploy
// phase, OpPurge = `lmm profile purge`, OpSwitch = `lmm profile switch`,
// OpUpdate = `lmm update`, OpRollback = `lmm rollback`, OpImport =
// `lmm import` archive mode, OpMergeRegen = a standalone merged-pak
// recompile, OpVerify = `lmm verify`, OpUpdateCheck = the read-only check
// that precedes OpUpdate, OpDownload = a raw Downloader.Download call with
// no owning flow); the three below have their own comment because their
// name alone doesn't disambiguate them from a sibling Op.
const (
	OpInstall Op = "install"
	OpDeploy  Op = "deploy"
	OpPurge   Op = "purge"
	OpSwitch  Op = "switch"
	// OpProfileApply is `lmm profile apply` - the converge flow whose
	// per-mod lines are worded identically to OpSwitch's, so it emits the
	// same Switch* phase family (see profile_apply.go's package comment).
	OpProfileApply Op = "profile_apply"
	// OpProfileSync is `lmm profile sync` - the DB-to-profile reconciliation
	// flow (v2 Phase 2 Unit J / #290).
	OpProfileSync Op = "profile_sync"
	OpUpdate      Op = "update"
	OpRollback    Op = "rollback"
	OpImport      Op = "import"
	// OpAdopt is `lmm import`'s SCAN mode - bringing untracked mod_path
	// entries under management (v2 Phase 2 Unit K / #291). Distinct from
	// OpImport, which is archive import and profile import.
	OpAdopt       Op = "adopt"
	OpMergeRegen  Op = "merge_regen"
	OpVerify      Op = "verify"
	OpUpdateCheck Op = "update_check"
	OpDownload    Op = "download"
	// OpModEdit is `lmm mod edit` - ApplyRelinkMod's metadata-only and
	// re-link edits (v2 Phase 3 Task 10, #303).
	OpModEdit Op = "mod_edit"
)

// Scope is embedded in every event: which operation, which mod (if any),
// and the mod's position in the batch (1-based; both zero when no batch
// position applies).
type Scope struct {
	Op      Op                   `json:"op"`
	Mod     *domain.ModReference `json:"mod,omitempty"`
	ModName string               `json:"mod_name,omitempty"`
	Index   int                  `json:"index,omitempty"`
	Total   int                  `json:"total,omitempty"`
}

// EventScope implements FlowEvent for every struct that embeds Scope.
func (s Scope) EventScope() Scope { return s }

// Event is anything a Service operation reports while it runs. Events are
// for live display; results are the record (spec §2).
type Event interface {
	EventType() string
}

// EventSink receives events synchronously on the operation's goroutine. A
// nil sink discards. Sinks must be cheap; buffering is the caller's job.
type EventSink func(Event)

// FlowEvent is an Event emitted by a deploy-family flow; every one carries
// the DeployPhase vocabulary the CLI renders from.
type FlowEvent interface {
	Event
	FlowPhase() DeployPhase
	EventScope() Scope
}

// StepEvent is an informational step or note: op-level progress lines,
// verbose-gated notes, "done" markers that terminate a progress line.
type StepEvent struct {
	Scope
	Phase  DeployPhase              `json:"phase"`
	Detail string                   `json:"detail,omitempty"`
	File   *domain.DownloadableFile `json:"file,omitempty"`
}

// DownloadEvent is a byte/percent tick during a download. Downloaded and
// TotalBytes are populated only where the flow has them (InstallDownloading
// and the raw Downloader stream); TotalBytes is 0 when unknown.
type DownloadEvent struct {
	Scope
	Phase      DeployPhase              `json:"phase"`
	File       *domain.DownloadableFile `json:"file,omitempty"`
	Downloaded int64                    `json:"downloaded"`
	TotalBytes int64                    `json:"total_bytes"`
	Percent    float64                  `json:"percent"`
}

// ModEvent is a line about one specific mod's lifecycle — a start notice, a
// skip, an outcome. Detail carries the reason where the phase has one.
type ModEvent struct {
	Scope
	Phase          DeployPhase    `json:"phase"`
	Detail         string         `json:"detail,omitempty"`
	Version        string         `json:"version,omitempty"`
	Class          DeployModClass `json:"class,omitempty"`
	FilesExtracted int            `json:"files_extracted,omitempty"`
}

// HookEvent reports a hook-lifecycle notice (a forced continuation past a
// failed before_all/before_each hook). Stage is the hook name, e.g.
// "install.before_all".
type HookEvent struct {
	Scope
	Phase  DeployPhase `json:"phase"`
	Stage  string      `json:"stage"`
	Detail string      `json:"detail"`
}

// WarningEvent is a diagnostic the user must see. Where a flow also
// collects it into Result.Warnings, the result stays authoritative.
type WarningEvent struct {
	Scope
	Phase   DeployPhase `json:"phase"`
	Message string      `json:"message"`
}

// MergeEvent reports the merged-pak sync at the end of a compile-mode
// deploy. MergedMods is the participant count (not the deploy total).
type MergeEvent struct {
	Scope
	Phase        DeployPhase `json:"phase"`
	MergedMods   int         `json:"merged_mods"`
	Artifact     string      `json:"artifact"`
	RawFallbacks int         `json:"raw_fallbacks"`
}

// UpdateCheckEvent is one per-mod tick while a source checks for updates.
// Index/Total are within SourceID's batch (each source reports its own
// batch; see Updater); GlobalIndex/GlobalTotal are the same tick's position
// across every source checked in this run (Ruling 6, #283) - a two-source
// run reports 1/5..5/5 on GlobalIndex/GlobalTotal even though Index/Total
// restarts at 1 for the second source's batch.
type UpdateCheckEvent struct {
	Scope
	SourceID    string `json:"source_id"`
	GlobalIndex int    `json:"global_index"`
	GlobalTotal int    `json:"global_total"`
}

// EventType implements Event: a StepEvent's wire "type" is "step".
func (StepEvent) EventType() string { return "step" }

// EventType implements Event: a DownloadEvent's wire "type" is "download".
func (DownloadEvent) EventType() string { return "download" }

// EventType implements Event: a ModEvent's wire "type" is "mod".
func (ModEvent) EventType() string { return "mod" }

// EventType implements Event: a HookEvent's wire "type" is "hook".
func (HookEvent) EventType() string { return "hook" }

// EventType implements Event: a WarningEvent's wire "type" is "warning".
func (WarningEvent) EventType() string { return "warning" }

// EventType implements Event: a MergeEvent's wire "type" is "merge".
func (MergeEvent) EventType() string { return "merge" }

// EventType implements Event: an UpdateCheckEvent's wire "type" is "update_check".
func (UpdateCheckEvent) EventType() string { return "update_check" }

// FlowPhase implements FlowEvent by returning the embedded Phase field.
func (e StepEvent) FlowPhase() DeployPhase { return e.Phase }

// FlowPhase implements FlowEvent by returning the embedded Phase field.
func (e DownloadEvent) FlowPhase() DeployPhase { return e.Phase }

// FlowPhase implements FlowEvent by returning the embedded Phase field.
func (e ModEvent) FlowPhase() DeployPhase { return e.Phase }

// FlowPhase implements FlowEvent by returning the embedded Phase field.
func (e HookEvent) FlowPhase() DeployPhase { return e.Phase }

// FlowPhase implements FlowEvent by returning the embedded Phase field.
func (e WarningEvent) FlowPhase() DeployPhase { return e.Phase }

// FlowPhase implements FlowEvent by returning the embedded Phase field.
func (e MergeEvent) FlowPhase() DeployPhase { return e.Phase }

// MarshalEvent renders e in the fixed wire envelope {"type": …, "data": …}.
// This is the SSE payload a future frontend receives.
func MarshalEvent(e Event) ([]byte, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", e.EventType(), err)
	}
	return json.Marshal(struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}{Type: e.EventType(), Data: data})
}

// RecordEvents returns a sink that appends every event to the returned
// slice. For tests.
func RecordEvents() (EventSink, *[]Event) {
	var got []Event
	return func(e Event) { got = append(got, e) }, &got
}
