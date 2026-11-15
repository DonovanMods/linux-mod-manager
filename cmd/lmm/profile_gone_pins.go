package main

// #657: a NexusMods collection pinning a file NexusMods no longer offers.
// The plan lists each such mod as needing attention, each gone file with
// its role and the current file that stands in for it; `--use-current
// <mod>` (or --use-current-all) installs those instead, `--skip-file <mod>`
// leaves a gone optional file out and installs the rest, and `--skip
// <mod>` leaves the mod out. Without a choice for every one, the import
// (or `profile sync --collection`) refuses, naming them and the flags. The
// same flags serve both commands.

import (
	"fmt"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/spf13/cobra"
)

// gonePinFlags are one command's gone-pin flags.
type gonePinFlags struct {
	useCurrent    []string
	useCurrentAll bool
	skipFile      []string
	skip          []string
}

// register adds the flags to cmd; prefix says when they apply.
func (f *gonePinFlags) register(cmd *cobra.Command, prefix string) {
	cmd.Flags().StringSliceVar(&f.useCurrent, "use-current", nil,
		prefix+"install the current file of the same role in place of this mod's pinned file NexusMods no longer offers (source:mod-id, or the mod id; repeatable)")
	cmd.Flags().BoolVar(&f.useCurrentAll, "use-current-all", false,
		prefix+"install the current file of the same role in place of every pinned file NexusMods no longer offers, where one stands in")
	cmd.Flags().StringSliceVar(&f.skipFile, "skip-file", nil,
		prefix+"leave out this mod's gone optional file and install the rest of it (source:mod-id, or the mod id; repeatable)")
	cmd.Flags().StringSliceVar(&f.skip, "skip", nil,
		prefix+"leave out this mod, whose pinned file NexusMods no longer offers (source:mod-id, or the mod id; repeatable)")
}

// used names the flags given.
func (f *gonePinFlags) used() []string {
	var out []string
	if len(f.useCurrent) > 0 {
		out = append(out, "--use-current")
	}
	if f.useCurrentAll {
		out = append(out, "--use-current-all")
	}
	if len(f.skipFile) > 0 {
		out = append(out, "--skip-file")
	}
	if len(f.skip) > 0 {
		out = append(out, "--skip")
	}
	return out
}

// choices is the flags as core's choices, a bare mod id taken as
// sourceID's.
func (f *gonePinFlags) choices(sourceID string) core.GonePinChoices {
	return core.GonePinChoices{UseCurrent: modKeys(f.useCurrent, sourceID), UseCurrentAll: f.useCurrentAll,
		SkipFile: modKeys(f.skipFile, sourceID), Skip: modKeys(f.skip, sourceID)}
}

// modKeys is args as domain.ModKey keys, a bare mod id taken as
// sourceID's; blank arguments are dropped.
func modKeys(args []string, sourceID string) []string {
	var out []string
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		if !strings.Contains(arg, ":") {
			arg = sourceID + ":" + arg
		}
		out = append(out, arg)
	}
	return out
}

// printReoffered lists the mods the profile skipped that the new version
// pins anew at an available file (#657): they need the user's attention,
// and each comes back unless --keep-skipped names it.
func printReoffered(plan *core.CollectionUpdatePlan) {
	if len(plan.Reoffered) == 0 {
		return
	}
	fmt.Printf("\n⚠ Needs your attention - %d skipped mod(s) the collection now pins at an available file:\n", len(plan.Reoffered))
	for _, it := range plan.Reoffered {
		what := "will be added"
		if it.KeepSkipped {
			what = "kept skipped (--keep-skipped)"
		}
		fmt.Printf("  ! %s %s: %s\n", updateItemLabel(it), it.NewVersion, what)
		if it.Note != "" {
			fmt.Printf("      %s\n", it.Note)
		}
		if !it.KeepSkipped {
			fmt.Printf("      keep it skipped with --keep-skipped %s\n", it.ModID)
		}
	}
}

// hasUndecidedGonePins reports whether c has a gone pin nobody chose for
// that the apply must have decided: a conflict's pin only when overridden
// says the user takes the collection's side of it (nil: never).
func hasUndecidedGonePins(c *core.ConvertedCollection, overridden func(key string) bool) bool {
	if c == nil {
		return false
	}
	for _, p := range c.GonePins {
		if p.Choice == "" && (!p.Conflict || (overridden != nil && overridden(p.Key()))) {
			return true
		}
	}
	return false
}

// gonePinDecision says what will happen to a decided pin, or "" while it
// is undecided.
func gonePinDecision(p core.CollectionGonePin) string {
	switch p.Choice {
	case core.GonePinUseCurrent:
		return "the current file of the same role will be installed instead"
	case core.GonePinSkipFile:
		return "the gone file is left out; the rest of the mod is installed"
	case core.GonePinSkip:
		return "skipped"
	}
	return ""
}

// modsPin is "1 mod pins" or "N mods pin".
func modsPin(n int) string {
	if n == 1 {
		return "1 mod pins"
	}
	return fmt.Sprintf("%d mods pin", n)
}

// printGonePins lists c's gone pins as needing attention: each gone file
// with its role and stand-in, what will happen to it, and the flags that
// decide an undecided one. command names the command, for the hint.
func printGonePins(c *core.ConvertedCollection, command string) {
	if len(c.GonePins) == 0 {
		return
	}
	fmt.Printf("\n⚠ Needs your attention - %s a file NexusMods no longer offers:\n", modsPin(len(c.GonePins)))
	for _, p := range c.GonePins {
		name := p.Name
		if name == "" {
			name = p.Key()
		} else {
			name += " (" + p.Key() + ")"
		}
		pinned := "file " + strings.Join(p.FileIDs, ", ")
		if p.Version != "" {
			pinned += ", version " + p.Version
		}
		fmt.Printf("  ! %s: the collection pins %s, which NexusMods no longer offers\n", name, pinned)
		for _, f := range p.Files {
			what := firstNonEmptyString(f.Name, "file "+f.FileID)
			role := "its main file"
			if f.Role == core.GoneFileSecondary {
				role = "an optional file"
			}
			line := fmt.Sprintf("      %s (%s): ", what, role)
			switch {
			case f.Replacement != nil && f.Role == core.GoneFileMain:
				line += "current main file " + describeCurrentFile(f.Replacement)
			case f.Replacement != nil:
				line += "current file " + describeCurrentFile(f.Replacement)
			case slices.Contains(p.NotReplaced, f.FileID) && p.Current != nil:
				line += "not replaced"
			case f.Role == core.GoneFileMain:
				line += "NexusMods offers no other file for it"
			default:
				line += "no current file of the same kind matches it"
			}
			fmt.Println(line)
		}
		if p.Conflict && p.Choice == "" {
			fmt.Printf("      → your version is kept; to take the collection's (--override %s), decide this file too\n", p.ModID)
			continue
		}
		if d := gonePinDecision(p); d != "" {
			fmt.Printf("      → %s\n", d)
			continue
		}
		var choose []string
		if p.CanUseCurrent() {
			choose = append(choose, "--use-current "+p.ModID+" (the current file)")
		}
		if p.CanSkipFile {
			choose = append(choose, "--skip-file "+p.ModID+" (install the rest)")
		}
		choose = append(choose, "--skip "+p.ModID)
		fmt.Printf("      choose: %s\n", strings.Join(choose, ", or "))
	}
	if hasUndecidedGonePins(c, nil) {
		fmt.Printf("  Nothing is imported until each is decided: re-run `%s` with --use-current <mod>, --use-current-all, --skip-file <mod> or --skip <mod>.\n", command)
	}
}

// describeCurrentFile names a current file, with its version.
func describeCurrentFile(f *core.CollectionCurrentFile) string {
	out := firstNonEmptyString(f.Name, "file "+f.FileID)
	if f.Version != "" {
		out += ", version " + f.Version
	}
	return out
}

// firstNonEmptyString is the first of values that is not empty.
func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
