// Package core: this file is the FOMOD resolve step (#614, design
// docs/plans/2026-11-10-collections-design.md §4): the install step that
// turns an extracted FOMOD installer archive into the tree its choices
// install, before the game's adapter lays that tree out.
//
// WHERE IT RUNS. Download -> extract to staging -> FOMOD resolve -> adapter
// NormalizeArchive -> cache commit, the first three in acquire. It runs in
// core, for every game, because it decides what the mod IS; adapter layout
// (BepInEx routing, d2r's) then applies to the result as it does to any
// archive. It executes internal/fomod's operations itself, copying from the
// extracted tree into a fresh one. It does NOT go through adapter.Layout,
// whose one-destination-per-member map cannot express a FOMOD copying one
// source to two destinations.
//
// WHAT IT INSTALLS WITH. A fomodInput: the choices recorded on the profile
// (or a collection) - a REPLAY - or the answers this very run was given
// (Options.Fomod) - a FRESH answer - or nothing. The policy for what
// internal/fomod reports back:
//
//   - No choices and nothing the user could change (no Unanswered group):
//     install the defaults, and record them.
//   - No choices and something to choose: *FomodChoicesRequiredError, the
//     question document, keyed by mod. Answerable: an install-shaped Apply
//     collects it before writing anything and keeps the download.
//   - Choices that leave a visible group unanswered, select nothing
//     (Unmatched) or break a group's rule (Violations): the same error,
//     carrying what did not fit, with the choices as the preselection - the
//     installer changed under the recording, or the answer was incomplete.
//   - MissingSources and UnsafeMembers: a REPLAY refuses (fidelity: the
//     recorded install is not reproducible from this archive), with a
//     *FomodInstallError the user cannot answer by choosing. A fresh answer
//     or the defaults only warn, as Vortex does: a missing source installs
//     nothing, and an unsafe member is never read.
//
// THE VARIANT. A resolved tree depends on more than the file, so its cache
// entry is a variant of the version (fomodVariant): a hash of the
// installer's config and the canonical choices, never part of the version.
// Recording the canonical choices on the profile is what makes the variant
// derivable from a profile reference without the archive, which is how the
// converge flows (apply, switch, import, snapshot restore) find the entry.
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// fomodVariantScheme names the variant hash's inputs and their encoding. A
// change to what a tree depends on (C5's file lists and patches) changes it,
// so an old entry is never mistaken for a new tree.
const fomodVariantScheme = "lmm-tree-1"

// fomodVariant is the cache variant of the tree c installs: the first 12
// hex digits of a sha256 over the scheme, the installer's config hash and
// the canonical encoding of the choices. Zero choices are the plain tree,
// "". The same function serves the resolve step (over the canonical choices
// it records) and the converge flows (over a profile reference's recorded
// ones), so a recorded install's entry is found without the archive.
func fomodVariant(c domain.FomodChoices) string {
	if c.IsZero() {
		return ""
	}
	steps := c.Steps
	if steps == nil {
		steps = []fomod.StepChoice{}
	}
	enc, err := json.Marshal(steps, json.Deterministic(true))
	if err != nil {
		// A []StepChoice always encodes; keep a stable, distinct answer
		// rather than panic if that ever changes.
		enc = []byte(err.Error())
	}
	h := sha256.New()
	_, _ = io.WriteString(h, fomodVariantScheme+"\n"+c.ConfigSHA256+"\n")
	_, _ = h.Write(enc)
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// refVariant is the variant a profile reference's recorded choices name.
func refVariant(ref *domain.ModReference) string {
	if ref == nil {
		return ""
	}
	return fomodVariant(ref.Fomod)
}

// fomodInput is what the resolve step installs a FOMOD with.
type fomodInput struct {
	// choices are the recorded or answered choices; zero for none.
	choices domain.FomodChoices
	// answered marks choices this run was given (Options.Fomod), a fresh
	// answer: missing sources and unsafe members only warn. Unset, non-zero
	// choices are a REPLAY of a recording, which refuses them.
	answered bool
	// files answers fileDependency conditions; nil means every file is
	// missing.
	files fomod.FileStates
}

// fomodResolved is one archive's resolve outcome.
type fomodResolved struct {
	// variant is the tree's cache variant (fomodVariant of choices).
	variant string
	// choices are the canonical choices made, with the installer's config
	// hash: what the profile records.
	choices domain.FomodChoices
	// warnings are what the install assumed or dropped, for the flow's
	// event sink.
	warnings []string
}

// maxFomodQuestionSteps caps the steps one question document carries. The
// parser's node cap already bounds a config; this keeps a hostile one from
// turning into an enormous --json error.
const maxFomodQuestionSteps = 1000

// resolveFomod runs the resolve step over tree, an archive extracted into
// a pristine staging directory (see this file's doc comment). archiveName
// is the archive's file name (an .omod is refused by name). It returns nil
// for an archive that is not a FOMOD, leaving tree untouched; otherwise tree
// holds exactly what the installer installs, and the outcome names its
// variant and the choices to record.
func (s *Service) resolveFomod(mod *domain.Mod, archiveName, tree string, in fomodInput) (*fomodResolved, error) {
	listed, err := relativeFileMembers(tree)
	if err != nil {
		return nil, fmt.Errorf("listing extracted members: %w", err)
	}
	members := slashMembers(listed)
	inst, err := fomod.Locate(archiveName, members)
	if err != nil {
		return nil, &FomodInstallError{SourceID: mod.SourceID, ModID: mod.ID, Name: mod.Name, Reason: fomodLocateReason(err), err: err}
	}
	if inst == nil {
		return nil, nil
	}

	configPath := filepath.Join(tree, filepath.FromSlash(inst.ModuleConfig))
	data, err := readCapped(configPath, fomod.MaxConfigSize)
	if err != nil {
		return nil, &FomodInstallError{SourceID: mod.SourceID, ModID: mod.ID, Name: mod.Name, Reason: "its installer could not be read: " + err.Error(), err: err}
	}
	sum := sha256.Sum256(data)
	configSHA := hex.EncodeToString(sum[:])
	cfg, err := fomod.Parse(data)
	if err != nil {
		return nil, &FomodInstallError{SourceID: mod.SourceID, ModID: mod.ID, Name: mod.Name, Reason: "its installer could not be read: " + err.Error(), err: err}
	}

	byRel := make(map[string]string, len(members))
	rels := make([]string, 0, len(members))
	for _, m := range members {
		if rel, ok := inst.Rel(m); ok {
			byRel[rel] = m
			rels = append(rels, rel)
		}
	}

	recorded := !in.choices.IsZero()
	req := fomod.Request{Choices: in.choices.Steps, Mode: fomod.ModeDefaults, Files: in.files, Members: rels}
	if recorded {
		req.Mode = fomod.ModeReplay
	}
	res, err := fomod.Evaluate(cfg, req)
	if err != nil {
		var prereq *fomod.PrerequisitesError
		reason := "its installer could not be evaluated: " + err.Error()
		if errors.As(err, &prereq) {
			reason = "its installer's prerequisites are not met: " + strings.Join(prereq.Unmet, "; ")
		}
		return nil, &FomodInstallError{SourceID: mod.SourceID, ModID: mod.ID, Name: mod.Name, Reason: reason, err: err}
	}

	if len(res.Unanswered) > 0 || (recorded && (len(res.Unmatched) > 0 || len(res.Violations) > 0)) {
		return nil, &FomodChoicesRequiredError{Mods: []FomodQuestion{fomodQuestion(mod, inst, cfg, configSHA, res, recorded)}}
	}

	warnings := slices.Clone(res.Warnings)
	if problems := fomodSourceProblems(res); len(problems) > 0 {
		if recorded && !in.answered {
			return nil, &FomodInstallError{SourceID: mod.SourceID, ModID: mod.ID, Name: mod.Name,
				Reason: "its recorded installer choices cannot be reproduced from this archive: " + strings.Join(problems, "; ")}
		}
		warnings = append(warnings, problems...)
	}

	opWarnings, err := executeFomodOps(tree, inst, byRel, res.Ops)
	if err != nil {
		return nil, &FomodInstallError{SourceID: mod.SourceID, ModID: mod.ID, Name: mod.Name, Reason: "installing its chosen files failed: " + err.Error(), err: err}
	}
	choices := domain.FomodChoices{ConfigSHA256: configSHA, Steps: res.Choices}
	if choices.Steps == nil {
		choices.Steps = []fomod.StepChoice{}
	}
	return &fomodResolved{variant: fomodVariant(choices), choices: choices, warnings: append(warnings, opWarnings...)}, nil
}

// fomodLocateReason words Locate's refusals.
func fomodLocateReason(err error) string {
	switch {
	case errors.Is(err, fomod.ErrScripted):
		return "it is a scripted installer (fomod/script.cs or an OMOD), which lmm never runs"
	case errors.Is(err, fomod.ErrAmbiguous):
		return "its archive holds more than one FOMOD installer"
	default:
		return err.Error()
	}
}

// fomodSourceProblems words the archive-side problems of res: sources that
// match nothing, and members dropped as unsafe.
func fomodSourceProblems(res *fomod.Result) []string {
	var out []string
	for _, m := range res.MissingSources {
		kind := "file"
		if m.Folder {
			kind = "folder"
		}
		why := "is not in the archive"
		if m.Reason == fomod.SourceIsDirectory {
			why = "names a directory"
		}
		out = append(out, fmt.Sprintf("the installer's %s %q %s", kind, strings.ReplaceAll(m.Source, `\`, "/"), why))
	}
	for _, m := range res.UnsafeMembers {
		out = append(out, fmt.Sprintf("the archive member %q is not a safe path and was not read", strings.ReplaceAll(m, "\x00", `\0`)))
	}
	return out
}

// readCapped reads path, refusing a file larger than limit.
func readCapped(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck // read-only
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fomod.ErrTooLarge
	}
	return data, nil
}

// executeFomodOps replaces tree's contents with what ops install: each copy
// read from tree (the extracted archive; Op.Source is installer-root
// relative, mapped back to its member through byRel) and written into a
// fresh tree, which then takes tree's place. The archive's own fomod/
// directory is never installed (Vortex does not deploy it either), and a
// destination in lmm's reserved namespace is refused: an installer must not
// be able to forge cache bookkeeping.
func executeFomodOps(tree string, inst *fomod.Installer, byRel map[string]string, ops []fomod.Op) ([]string, error) {
	out := tree + ".fomod"
	if err := os.RemoveAll(out); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}
	var warnings []string
	fail := func(err error) ([]string, error) {
		_ = os.RemoveAll(out)
		return nil, err
	}
	for _, op := range ops {
		dest, err := fomodDestination(op.Destination)
		if err != nil {
			return fail(err)
		}
		switch op.Kind {
		case fomod.OpMkdir:
			if dest == "" {
				continue
			}
			if err := os.MkdirAll(filepath.Join(out, dest), 0o755); err != nil {
				return fail(err)
			}
		case fomod.OpCopy:
			if isFomodDirectory(op.Source) {
				warnings = append(warnings, fmt.Sprintf("the installer's own %q was not installed: lmm never ships an archive's fomod/ directory", op.Source))
				continue
			}
			member, ok := byRel[op.Source]
			if !ok || dest == "" {
				return fail(fmt.Errorf("the installer named %q, which is not an archive member", op.Source))
			}
			src := filepath.Join(tree, filepath.FromSlash(member))
			if err := copyFileStreaming(src, filepath.Join(out, dest)); err != nil {
				return fail(err)
			}
		}
	}
	_ = inst // Op.Source is relative to inst.Root; byRel already maps it.
	if err := os.RemoveAll(tree); err != nil {
		return fail(err)
	}
	if err := os.Rename(out, tree); err != nil {
		return fail(err)
	}
	return warnings, nil
}

// fomodDestination is an op destination as a local path under the mod
// root, or an error for one that is not (internal/fomod refuses those
// already; this is the write side's own check) or that names lmm's reserved
// bookkeeping.
func fomodDestination(dest string) (string, error) {
	if dest == "" {
		return "", nil
	}
	local := filepath.FromSlash(dest)
	if !filepath.IsLocal(local) {
		return "", fmt.Errorf("the installer's destination %q leaves the mod", dest)
	}
	for _, seg := range strings.Split(dest, "/") {
		if strings.HasPrefix(seg, cache.ReservedPrefix) {
			return "", fmt.Errorf("the installer's destination %q is in lmm's reserved namespace (%s*)", dest, cache.ReservedPrefix)
		}
	}
	return local, nil
}

// isFomodDirectory reports an installer-root-relative path inside the
// installer's own fomod/ directory.
func isFomodDirectory(rel string) bool {
	first, _, _ := strings.Cut(strings.ReplaceAll(rel, `\`, "/"), "/")
	return strings.EqualFold(first, "fomod")
}

// fomodQuestion builds the question document for one mod from an
// evaluation: what is visible, what is selected, and what did not fit.
func fomodQuestion(mod *domain.Mod, inst *fomod.Installer, cfg *fomod.Config, configSHA string, res *fomod.Result, recorded bool) FomodQuestion {
	q := FomodQuestion{
		SourceID: mod.SourceID, ModID: mod.ID, Name: mod.Name,
		ConfigSHA256: configSHA, ModuleName: cfg.ModuleName, InstallerRoot: inst.Root,
		Reason: FomodReasonUnanswered, Choices: res.Choices,
	}
	if q.Choices == nil {
		q.Choices = []fomod.StepChoice{}
	}
	if recorded {
		q.Reason = FomodReasonStale
	}
	for i, st := range res.Steps {
		if i == maxFomodQuestionSteps {
			break
		}
		step := FomodQuestionStep{Name: st.Name, Ordinal: st.Ordinal, Groups: []FomodQuestionGroup{}}
		for _, g := range st.Groups {
			group := FomodQuestionGroup{Name: g.Name, Ordinal: g.Ordinal, Type: g.Type.String(), Answered: g.Answered, Options: []FomodQuestionOption{}}
			for _, o := range g.Options {
				group.Options = append(group.Options, FomodQuestionOption{
					Name: o.Name, Ordinal: o.Ordinal, Description: o.Description, Image: o.Image,
					Type: o.Type.String(), Selected: o.Selected,
				})
			}
			step.Groups = append(step.Groups, group)
		}
		q.Steps = append(q.Steps, step)
	}
	if q.Steps == nil {
		q.Steps = []FomodQuestionStep{}
	}
	for _, u := range res.Unanswered {
		q.Unanswered = append(q.Unanswered, FomodGroupRef{Step: u.Step, Group: u.Group})
	}
	for _, u := range res.Unmatched {
		q.Unmatched = append(q.Unmatched, FomodUnmatchedChoice{Step: u.Step, Group: u.Group, Option: u.Option, Reason: u.Reason.String()})
	}
	for _, v := range res.Violations {
		q.Violations = append(q.Violations, FomodRuleViolation{Step: v.Step, Group: v.Group, Type: v.Type.String(), Selected: v.Selected})
	}
	return q
}
