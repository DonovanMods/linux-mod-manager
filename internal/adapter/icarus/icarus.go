// Package icarus is the game adapter for Icarus (#412, story U2 of #353):
// the compile, .EXMODZ and merge rules that decide what Icarus does with
// mod content, as opposed to internal/source/icarus, which is only where
// the bytes come from.
//
// The two were one package until U2. That was the mistake #353 exists to
// correct: source.MergeCompiler made compilation a property of the SOURCE,
// so an Icarus .pak downloaded from NexusMods could not compile while the
// identical file served by Project Daedalus could. The split runs along a
// line that was already there - the Firestore ModSource on one side, the
// format code on the other - and everything below it moved VERBATIM, which
// is what lets the merge, golden, pakconvert and exmodz tests come along
// unedited and go on being the compile path's regression net.
//
// The two packages share the name `icarus` deliberately (design OQ2): they
// really are about the same game, and they live in different namespaces -
// `sources:` selects one, `adapter:` the other.
package icarus

import (
	"context"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
)

// Icarus is the adapter. It carries no state: every rule below is a pure
// function of its arguments and of the installed game's own data.pak, which
// is why one value serves the whole process.
type Icarus struct{}

// New constructs the Icarus adapter. It takes no arguments on purpose -
// nothing here needs a client, a key or a path - so internal/app registers
// it with one line.
func New() *Icarus { return &Icarus{} }

var (
	_ adapter.GameAdapter   = (*Icarus)(nil)
	_ adapter.MergeCompiler = (*Icarus)(nil)
)

// AdapterID is the `adapter:` value in games.yaml that selects this
// adapter, and the value `deploy_mode: compile` migrates to for a game that
// names none (design §2, OQ1).
const AdapterID = "icarus"

// ID implements adapter.GameAdapter.
func (s *Icarus) ID() string { return AdapterID }

// Label implements adapter.GameAdapter.
func (s *Icarus) Label() string { return "Icarus (compiled mod tables)" }

// NormalizeArchive is the IDENTITY for Icarus, and deliberately so
// (design §3 U2, decision 16). .EXMODZ's wrapper strip - the #237 fix that
// took a released bug to find - is a rule about the BUNDLE FORMAT's
// payload, read at merge time by ParseExmodz; NormalizeArchive is a rule
// about an ARCHIVE's layout on disk, applied at ingest. Conflating them
// would move a correctness fix out from under the tests that pin it.
//
// An Icarus mod therefore lands in its cache entry exactly as it always
// has, which is what keeps this unit's "no golden is re-recorded" claim
// true for compile games as well as generic ones.
func (s *Icarus) NormalizeArchive(_ adapter.NormalizeRequest) (adapter.Layout, error) {
	return adapter.Layout{}, nil
}

// ValidateSource implements adapter.MergeCompiler by delegating to the
// package-level ValidateSource function.
func (s *Icarus) ValidateSource(sourceFilePath string) error {
	return ValidateSource(sourceFilePath)
}

// MergeCompile implements adapter.MergeCompiler by delegating to the
// package-level MergeCompile function. ctx is unused: merging is pure local
// file I/O against the installed game's own pak (#175/#197), with nothing
// to cancel.
func (s *Icarus) MergeCompile(ctx context.Context, basePakPath string, sources []MergeSource, outputPakPath string) ([]string, []adapter.MergeFailure, error) {
	return MergeCompile(ctx, basePakPath, sources, outputPakPath)
}
