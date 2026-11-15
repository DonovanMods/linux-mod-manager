package main

// #657: a NexusMods collection pinning a file NexusMods no longer offers.
// The plan lists each such mod as needing attention, with its current main
// file; `--use-current <mod>` (or --use-current-all) installs that file
// instead, and `--skip <mod>` leaves the mod out. Without a choice for
// every one, the import (or `profile sync --collection`) refuses, naming
// them and the flags. The same flags serve both commands.

import (
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/spf13/cobra"
)

// gonePinFlags are one command's gone-pin flags.
type gonePinFlags struct {
	useCurrent    []string
	useCurrentAll bool
	skip          []string
}

// register adds the flags to cmd; prefix says when they apply.
func (f *gonePinFlags) register(cmd *cobra.Command, prefix string) {
	cmd.Flags().StringSliceVar(&f.useCurrent, "use-current", nil,
		prefix+"install this mod's current main file in place of a pinned file NexusMods no longer offers (source:mod-id, or the mod id; repeatable)")
	cmd.Flags().BoolVar(&f.useCurrentAll, "use-current-all", false,
		prefix+"install the current main file in place of every pinned file NexusMods no longer offers")
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
	if len(f.skip) > 0 {
		out = append(out, "--skip")
	}
	return out
}

// choices is the flags as core's choices, a bare mod id taken as
// sourceID's.
func (f *gonePinFlags) choices(sourceID string) core.GonePinChoices {
	return core.GonePinChoices{UseCurrent: modKeys(f.useCurrent, sourceID), UseCurrentAll: f.useCurrentAll, Skip: modKeys(f.skip, sourceID)}
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

// hasUndecidedGonePins reports whether c has a gone pin nobody chose for.
func hasUndecidedGonePins(c *core.ConvertedCollection) bool {
	if c == nil {
		return false
	}
	for _, p := range c.GonePins {
		if p.Choice == "" {
			return true
		}
	}
	return false
}

// printGonePins lists c's gone pins as needing attention, each with what
// will happen to it, and the flags that decide an undecided one. inFlag
// names the command's collection flag, for the hint.
func printGonePins(c *core.ConvertedCollection, inFlag string) {
	if len(c.GonePins) == 0 {
		return
	}
	fmt.Printf("\n⚠ Needs your attention - %d mod(s) pin a file NexusMods no longer offers:\n", len(c.GonePins))
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
		current := "NexusMods offers no other file for it"
		if f := p.Current; f != nil {
			current = "current main file: " + firstNonEmptyString(f.Name, "file "+f.FileID)
			if f.Version != "" {
				current += ", version " + f.Version
			}
		}
		fmt.Printf("      %s\n", current)
		switch p.Choice {
		case core.GonePinUseCurrent:
			fmt.Println("      → the current file will be installed instead")
		case core.GonePinSkip:
			fmt.Println("      → skipped")
		default:
			if p.Current != nil {
				fmt.Printf("      choose: --use-current %s (the current file), or --skip %s\n", p.ModID, p.ModID)
			} else {
				fmt.Printf("      choose: --skip %s\n", p.ModID)
			}
		}
	}
	if hasUndecidedGonePins(c) {
		fmt.Printf("  Nothing is imported until each is decided: re-run `%s` with --use-current <mod>, --use-current-all or --skip <mod>.\n", inFlag)
	}
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
