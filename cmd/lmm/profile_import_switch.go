package main

// #666: `lmm profile import` and the switch to the profile it imported.
//
// An import into a profile that is not the game's active one records its
// mods and deploys nothing (#462). --switch asks core to switch to it once
// the import finishes (core.ProfileImportOptions.Switch, reported on the
// result's Switch), so the switch is one document under --json. With
// neither --switch nor --no-switch, an interactive run decides after the
// import, having seen the switch's own plan: it runs `lmm profile switch`'s
// Plan/Apply with "Switch to <profile> now?" for its question.

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"golang.org/x/term"
)

// profileImportStdinIsTerminal reports whether a person can answer the
// import's offer to switch (#666). A seam for tests.
var profileImportStdinIsTerminal = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// checkImportSwitchFlags refuses --switch with what contradicts it.
func checkImportSwitchFlags() error {
	if profileImportSwitch && profileImportNoSwitch {
		return errors.New("--switch and --no-switch contradict each other; give one of them")
	}
	if profileImportSwitch && profileImportNoInstall {
		return errors.New("--switch deploys the imported profile, installing its mods; it cannot be used with --no-install")
	}
	return nil
}

// importSwitchOffered reports whether the import asks, once it is done,
// whether to switch to the imported profile: neither --switch nor
// --no-switch, and a run a person answers - stdin a terminal, no -y, no
// --json. Any other run keeps the import recorded-only, as before #666.
func importSwitchOffered() bool {
	return !profileImportSwitch && !profileImportNoSwitch && !profileImportYes && !jsonOutput && profileImportStdinIsTerminal()
}

// printImportSwitchNotice says, before anything runs, what --switch will do
// once the import finishes: take the active profile's mods down and deploy
// the imported one. Nothing for an import into the active profile.
func printImportSwitchNotice(plan *core.ImportPlan, game *domain.Game) {
	if !profileImportSwitch || !plan.RecordedOnly {
		return
	}
	fmt.Printf("\n--switch: once imported, %s becomes the active profile of %s: %s's deployed mods are taken down and %s is deployed.\n",
		plan.Profile.Name, game.Name, plan.ActiveProfile, plan.Profile.Name)
}

// importSwitchPhase prints the import's announcement of its switch; the
// switch's own steps are switchProgress's. Reports whether e was one.
func importSwitchPhase(p flowLine) bool {
	if p.Phase != core.ImportSwitching {
		return false
	}
	fmt.Printf("\nSwitching to profile: %s\n", p.ModName)
	return true
}

// reportImportSwitch prints what --switch did (result.Switch), and returns
// the error the run ends with: err, or - when the import itself succeeded
// but its switch failed - a failure already reported, so the exit status
// says the switch asked for did not happen cleanly. The import stands
// either way.
func reportImportSwitch(sw *core.ImportSwitch, err error) error {
	if sw == nil {
		return err
	}
	if !jsonOutput {
		printIncomplete(sw.Unresolved)
		for _, w := range sw.Warnings {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
		}
		if sw.Result != nil {
			for _, w := range sw.Result.Warnings {
				fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
			}
		}
		switch {
		case sw.Error == "":
			fmt.Printf("\n✓ Switched to profile: %s\n", sw.To)
		case sw.Switched && sw.Result != nil:
			// Each failure printed as it happened, as `lmm profile switch`
			// prints them.
			fmt.Printf("\n✗ Profile %s is now active, but %d mod(s) failed.\n", sw.To, len(sw.Result.Failed))
		default:
			fmt.Printf("\n✗ Not switched to %s: %s\n", sw.To, sw.Error)
			fmt.Printf("  The import stands; `lmm profile switch %s` switches to it.\n", sw.To)
		}
	}
	if sw.Error != "" && err == nil {
		return fmt.Errorf("switching to the imported profile: %w", ErrReported)
	}
	return err
}

// offerImportSwitch is the interactive offer (importSwitchOffered): once an
// import into a profile that is not the active one has finished, it shows
// the switch's plan - what it takes down, what it deploys - and asks
// "Switch to <profile> now? [Y/n]", defaulting to yes. Declining keeps the
// import as it is.
func offerImportSwitch(ctx context.Context, service *core.Service, game *domain.Game, result *core.ProfileImportResult) error {
	if result == nil || !result.RecordedOnly || result.Refused || result.ProfileName == "" {
		return nil
	}
	fmt.Printf("\n%s is imported; %s is the active profile, whose mods are the ones deployed.\n", result.ProfileName, result.ActiveProfile)
	err := runSwitch(ctx, service, game, result.ProfileName,
		switchRun{offer: true, prompt: fmt.Sprintf("\nSwitch to %s now? [Y/n]: ", result.ProfileName)})
	if errors.Is(err, ErrCancelled) {
		fmt.Printf("Not switched. `lmm profile switch %s` switches to it later.\n", result.ProfileName)
		return nil
	}
	return err
}
