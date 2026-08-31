package main

import (
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// flowLine is the CLI's flat view of a core flow event. Its field names are
// the ones the eight rendering closures in deploy.go, purge.go, profile.go,
// install.go and update.go were written against, so those closures — which
// encode the byte-exact output contract — stay unchanged while core emits
// typed events. It is presentation-only; nothing outside cmd/lmm sees it.
type flowLine struct {
	Index, Total   int
	ModName        string
	ModID          string
	SourceID       string
	Phase          core.DeployPhase
	Detail         string
	Percent        float64
	Downloaded     int64
	TotalBytes     int64
	File           *domain.DownloadableFile
	ModVersion     string
	FilesExtracted int
	ModClass       core.DeployModClass
	RawFallbacks   int
}

// lineOf projects a flow event onto a flowLine. ok is false for events that
// are not part of the deploy-family vocabulary (update-check, verify).
func lineOf(e core.Event) (flowLine, bool) {
	fe, ok := e.(core.FlowEvent)
	if !ok {
		return flowLine{}, false
	}
	s := fe.EventScope()
	l := flowLine{Index: s.Index, Total: s.Total, ModName: s.ModName, Phase: fe.FlowPhase()}
	if s.Mod != nil {
		l.ModID, l.SourceID = s.Mod.ModID, s.Mod.SourceID
	}
	switch ev := e.(type) {
	case core.StepEvent:
		l.Detail, l.File = ev.Detail, ev.File
	case core.DownloadEvent:
		l.File, l.Downloaded, l.TotalBytes, l.Percent = ev.File, ev.Downloaded, ev.TotalBytes, ev.Percent
	case core.ModEvent:
		l.Detail, l.ModVersion, l.ModClass, l.FilesExtracted = ev.Detail, ev.Version, ev.Class, ev.FilesExtracted
	case core.HookEvent:
		l.Detail = ev.Detail
	case core.WarningEvent:
		l.Detail = ev.Message
	case core.MergeEvent:
		l.Total, l.Detail, l.RawFallbacks = ev.MergedMods, ev.Artifact, ev.RawFallbacks
	}
	return l, true
}
