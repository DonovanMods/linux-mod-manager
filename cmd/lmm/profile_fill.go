package main

// profile_fill.go is `lmm profile fill` (#608): install a collection
// profile's pending pins from archives downloaded by hand, matched by the
// size and md5 the collection records. Core does the matching and the
// install (PlanProfileFill/ApplyProfileFill); this file parses, prints and
// confirms.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
)

var profileFillCmd = &cobra.Command{
	Use:   "fill <profile> <dir-or-archive>...",
	Short: "Install a collection profile's pending mods from downloaded archives",
	Long: `Install the pending mods of a profile imported from a NexusMods collection
from archives you downloaded yourself.

A free NexusMods account is not served downloads through the API, so a
collection import saves the profile with every mod pending. Download each
file from its page (the import lists them), then point this command at the
folder you saved them to - usually ~/Downloads. Each archive is matched to
a pending mod by the size and md5 checksum the collection records for its
file, and the matches are installed as if downloaded: with the installer
(FOMOD) choices and mod type the collection records, and, for a mod the
curator wants on its newest file, set to update automatically.

Only the folder's top level is read and links in it are never followed; a
file is read only when its size is a pending file's. Your files are copied,
never moved or deleted. Run it again as you download more: each run installs
what it finds and says what is still pending.

Examples:
  lmm profile fill "Essential Mods" ~/Downloads --game skyrim-se
  lmm profile fill "Essential Mods" ~/Downloads/skse64_2_02_06.7z --game skyrim-se
  lmm profile fill "Essential Mods" ~/Downloads --game skyrim-se --dry-run`,
	Args: cobra.MinimumNArgs(2),
	RunE: runProfileFill,
}

var (
	profileFillYes    bool
	profileFillDryRun bool
)

func init() {
	profileCmd.AddCommand(profileFillCmd)
	profileFillCmd.Flags().BoolVarP(&profileFillYes, "yes", "y", false, "auto-confirm installing the matched mods")
	profileFillCmd.Flags().BoolVar(&profileFillDryRun, "dry-run", false, "print what the fill would install without changing anything")
	addFomodFlags(profileFillCmd)
}

func runProfileFill(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doProfileFill(ctx, service, game, args[0], args[1:])
	})
}

func doProfileFill(ctx context.Context, service *core.Service, game *domain.Game, profileName string, paths []string) error {
	plan, err := service.PlanProfileFill(ctx, game, core.ProfileFillRequest{Profile: profileName, Paths: paths})
	if err != nil {
		return err
	}
	// Ruling 15: --dry-run --json is the plan document.
	if profileFillDryRun && jsonOutput {
		return emitJSON(plan)
	}
	if !jsonOutput {
		printFillPlan(plan)
	}
	if profileFillDryRun {
		return nil
	}
	if len(plan.Matched) == 0 {
		if jsonOutput {
			return emitJSON(&core.ProfileFillResult{Profile: plan.Profile, Remaining: len(plan.Pending)})
		}
		fmt.Println("\nNothing to install.")
		return nil
	}
	if !profileFillYes {
		if !jsonOutput {
			verb := "Install"
			if plan.RecordedOnly {
				verb = "Record"
			}
			fmt.Printf("\n%s %d mod(s) from these files? [Y/n]: ", verb, len(plan.Matched))
		}
		input, err := readPromptLine()
		if err != nil {
			return err
		}
		if input != "" && input != "y" && input != "yes" {
			return ErrCancelled
		}
	}

	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.ImportInstalling:
			fmt.Println("\nInstalling from the matched files...")
		case core.ImportModInstalling:
			fmt.Printf("  Installing %s:%s...\n", p.SourceID, p.ModID)
		case core.ImportModFailed:
			fmt.Printf("    Error: %s\n", p.Detail)
		case core.ImportModInstalled:
			if plan.RecordedOnly {
				fmt.Printf("    ✓ Recorded: %s\n", p.ModName)
			} else {
				fmt.Printf("    ✓ Installed: %s\n", p.ModName)
			}
		case core.ImportNote:
			if verbose {
				fmt.Printf("    %s\n", p.Detail)
			}
		}
	}
	fa, err := newFomodAnswerer(service)
	if err != nil {
		return err
	}
	result, err := applyAnsweringFomod(fa, func(answers map[string]domain.FomodChoices) (*core.ProfileFillResult, error) {
		return service.ApplyProfileFill(ctx, game, plan, core.ProfileFillOptions{Fomod: answers}, quietSink(progress))
	}, func(r *core.ProfileFillResult) *core.FomodChoicesRequiredError {
		if r == nil {
			return nil
		}
		return r.FomodChoicesRequired
	})
	var incomplete *core.ProfileFillIncompleteError
	if errors.As(err, &incomplete) && !jsonOutput {
		printFillResult(result)
		if q := fomodUnanswered(result.FomodChoicesRequired); q != nil {
			return q
		}
		return err
	}
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(result)
	}
	printFillResult(result)
	return nil
}

// fillReasonText words a pending pin's reason for a person.
func fillReasonText(r core.FillPendingReason) string {
	switch r {
	case core.FillPartial:
		return "only some of its files were found"
	case core.FillNoChecksum:
		return "the collection records no checksum for its file"
	case core.FillDisabled:
		return "switched off in the profile"
	}
	return "not found"
}

// fillIgnoredText words why a file was not used.
func fillIgnoredText(r core.FillIgnoredReason) string {
	switch r {
	case core.FillIgnoredChecksumMismatch:
		return "the size of a pending file, but not its checksum"
	case core.FillIgnoredDuplicate:
		return "a copy of a file already matched"
	case core.FillIgnoredHashLimit:
		return "not read: too many files of this size did not match"
	case core.FillIgnoredSymlink:
		return "a link, which is never followed"
	case core.FillIgnoredNotRegular:
		return "not a regular file"
	case core.FillIgnoredUnreadable:
		return "could not be read"
	}
	return "matches no pending file"
}

func printFillPlan(plan *core.ProfileFillPlan) {
	fmt.Printf("Profile: %s", plan.Profile)
	if c := plan.Collection; c != nil {
		fmt.Printf(" (collection %s, revision %d)", c.Slug, c.Revision)
	}
	fmt.Println()
	fmt.Printf("Looked at %d file(s), read %d.\n", plan.Scanned, plan.Hashed)
	for _, n := range plan.Notes {
		fmt.Printf("  %s\n", n)
	}
	if len(plan.Matched) > 0 {
		fmt.Printf("\n  ✓ %d to install:\n", len(plan.Matched))
		for _, m := range plan.Matched {
			var archives []string
			for _, f := range m.Files {
				archives = append(archives, f.Archive)
			}
			fmt.Printf("    - %s ← %s", fillName(m.Name, m.SourceID, m.ModID), strings.Join(archives, ", "))
			var extra []string
			if m.ModType != "" {
				extra = append(extra, "type "+m.ModType)
			}
			if m.Installer {
				extra = append(extra, "recorded installer choices")
			}
			if m.AutoUpdate {
				extra = append(extra, "updates automatically")
			}
			if len(extra) > 0 {
				fmt.Printf(" (%s)", strings.Join(extra, "; "))
			}
			fmt.Println()
		}
	}
	if len(plan.Pending) > 0 {
		fmt.Printf("\n  ⇣ %d still pending:\n", len(plan.Pending))
		for _, p := range plan.Pending {
			fmt.Printf("    - %s: %s\n", fillName(p.Name, p.SourceID, p.ModID), fillReasonText(p.Reason))
		}
	}
	var unmatched int
	var listed []core.FillIgnored
	for _, ig := range plan.Ignored {
		if ig.Reason == core.FillIgnoredNoMatch {
			unmatched++
			continue
		}
		listed = append(listed, ig)
	}
	if len(listed) > 0 || (verbose && unmatched > 0) {
		fmt.Printf("\n  Not used:\n")
		for _, ig := range plan.Ignored {
			if ig.Reason == core.FillIgnoredNoMatch && !verbose {
				continue
			}
			fmt.Printf("    - %s: %s\n", ig.Name, fillIgnoredText(ig.Reason))
		}
	}
	if unmatched > 0 && !verbose {
		fmt.Printf("  %d other file(s) match no pending file (-v lists them).\n", unmatched)
	}
	if plan.RecordedOnly && len(plan.Matched) > 0 {
		fmt.Printf("\n%s is not the active profile (%s is), so the fill records these mods in it and deploys nothing; `lmm profile switch %s` deploys them.\n",
			plan.Profile, plan.ActiveProfile, plan.Profile)
	}
}

func fillName(name, sourceID, modID string) string {
	if name == "" {
		return sourceID + ":" + modID
	}
	return fmt.Sprintf("%s (%s:%s)", name, sourceID, modID)
}

func printFillResult(result *core.ProfileFillResult) {
	if result == nil {
		return
	}
	for _, n := range result.Notes {
		fmt.Printf("  %s\n", n)
	}
	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}
	fmt.Printf("\n--- Summary ---\n")
	if result.Refused {
		fmt.Println("Nothing was installed: no mod was written.")
		for _, f := range result.Failures {
			printRefusedItem(f.Name, f.SourceID, f.ModID, f.Reason, f.Refusal)
		}
		return
	}
	if result.RecordedOnly {
		fmt.Printf("Recorded: %d\n", result.Recorded)
	} else {
		fmt.Printf("Installed: %d\n", result.Installed)
	}
	if result.Failed > 0 {
		fmt.Printf("Failed: %d\n", result.Failed)
	}
	fmt.Printf("Still pending: %d\n", result.Remaining)
}
