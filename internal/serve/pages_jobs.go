package serve

import (
	"bytes"
	"fmt"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// jobPageRefreshSeconds is how often the /jobs/{id} page reloads ITSELF
// while its job is still running, and only with JavaScript disabled (the
// meta refresh sits inside a <noscript> in the head, which is where HTML5
// permits it). With JavaScript on, Task 10's enhancement subscribes to the
// SSE stream instead and the page never reloads.
//
// It is paired with, never a replacement for, the always-present manual
// Refresh link: an automatic reload is a WCAG 2.2.1 timing concern, and the
// link is the accessible way to advance the page under the user's own
// control. A finished job renders neither the meta refresh nor a reason to
// reload.
const jobPageRefreshSeconds = 5

// jobPageData is "/jobs/{id}"'s template data.
type jobPageData struct {
	pageChrome

	// NotFound is set when the id names no job the registry still holds -
	// it never existed, or it aged out of retention. The page renders an
	// explanation instead of a job, with a 404 status.
	NotFound bool

	// Status is the same job status document GET /api/v1/jobs/{id}
	// answers with; Kind is that kind's human title ("Deploy").
	Status jobStatus
	Kind   string

	// Running is Status.State == jobRunning, hoisted out so the template
	// does not have to compare against a package constant it cannot name.
	Running bool

	// Facts is the finished job's result readout, from the kind's own
	// Summarize (nil while running, or when the job failed).
	Facts []resultFact

	// ErrorDetails is the failure envelope's "details" payload rendered as
	// JSON for display - the typed data (conflicting paths, warnings) that
	// the one-line error message leaves out. Empty when the job did not
	// fail, or failed with an error carrying no details.
	ErrorDetails string

	// Events is the job's retained event history as display lines - what a
	// user with JavaScript off sees in place of the live stream.
	Events []jobEventLine
}

// jobEventLine is one row of the job page's event log: the event's own
// type name, its deploy-phase vocabulary term, the mod it concerns, and
// whatever human detail the event carries.
type jobEventLine struct {
	Type   string
	Phase  string
	Mod    string
	Detail string
}

// handleJobPage renders "/jobs/{id}" - the live job page
// (docs/plans/2026-08-30-serve-design.md §HTTP surface). It works entirely
// without JavaScript: the current state, the events so far, and either the
// result's key facts or the failure envelope, plus a refresh affordance
// while the job is still going.
func (s *Server) handleJobPage(w http.ResponseWriter, r *http.Request) {
	id := jobID(r.PathValue("id"))
	data := jobPageData{pageChrome: s.chrome(r, "Job", nil)}

	j, ok := s.jobs.job(id)
	if !ok {
		data.NotFound = true
		s.renderStatus(w, http.StatusNotFound, jobTemplate, data)
		return
	}

	status := j.status()
	data.Status = status
	data.Kind = jobKindTitle(status.Kind)
	data.Title = data.Kind + " job"
	data.Running = status.State == jobRunning
	data.Events = jobEventLines(j.replay())
	if data.Running {
		data.Refresh = jobPageRefreshSeconds
	}
	if status.State == jobSucceeded {
		if kind, ok := lookupPlanKind(status.Kind); ok {
			data.Facts = kind.Summarize(status.Result)
		}
	}
	if status.Error != nil && status.Error.Details != nil {
		data.ErrorDetails = s.renderJobErrorDetails(status.Error.Details)
	}

	s.render(w, jobTemplate, data)
}

// jobKindTitle is the registered kind's human title, falling back to the
// stored kind name for a job whose kind is no longer registered (only
// possible across a code change, never within one run).
func jobKindTitle(kind string) string {
	if k, ok := lookupPlanKind(kind); ok {
		return k.Title
	}
	return kind
}

// renderJobErrorDetails encodes a failure envelope's typed details for
// display, using the same encoder the wire uses so the page and the API can
// never show different data. An encode failure degrades to a short note
// rather than failing the whole page - the message and the state above it
// are the important part.
func (s *Server) renderJobErrorDetails(details any) string {
	var buf bytes.Buffer
	if err := core.EncodeJSON(&buf, details); err != nil {
		s.log.Debug("serve: rendering job error details failed", "err", err)
		return "(details could not be rendered)"
	}
	return buf.String()
}

// jobEventLines renders a job's retained events as display rows. Phase and
// mod come from core.FlowEvent, which every deploy-family event implements;
// the detail is per-type, because "what this event has to say" is the one
// thing the interface deliberately does not generalise.
func jobEventLines(events []core.Event) []jobEventLine {
	lines := make([]jobEventLine, 0, len(events))
	for _, e := range events {
		line := jobEventLine{Type: e.EventType()}
		if flow, ok := e.(core.FlowEvent); ok {
			line.Phase = flow.FlowPhase().String()
			line.Mod = flow.EventScope().ModName
		}
		switch ev := e.(type) {
		case core.StepEvent:
			line.Detail = ev.Detail
		case core.ModEvent:
			line.Detail = ev.Detail
		case core.HookEvent:
			line.Detail = fmt.Sprintf("%s: %s", ev.Stage, ev.Detail)
		case core.WarningEvent:
			line.Detail = ev.Message
		case core.DownloadEvent:
			line.Detail = fmt.Sprintf("%.0f%%", ev.Percent)
		case core.MergeEvent:
			line.Detail = fmt.Sprintf("%s (%d mods)", ev.Artifact, ev.MergedMods)
		case core.UpdateCheckEvent:
			line.Mod = ev.EventScope().ModName
			line.Detail = ev.SourceID
		}
		lines = append(lines, line)
	}
	return lines
}
