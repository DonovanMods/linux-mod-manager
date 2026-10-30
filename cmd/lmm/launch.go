package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
)

var (
	launchProfile string
	launchDeploy  bool
	launchStrict  bool
	launchDryRun  bool
)

var launchCmd = &cobra.Command{
	Use:   "launch",
	Short: "Launch the game",
	Long: `Launch the game for its active profile.

lmm uses the first launch method that applies:

  1. launch.command in games.yaml: that program, with its arguments.
  2. A Steam app id: steam steam://rungameid/<id> (through the Flatpak
     Steam for a game in its library, or xdg-open when steam is not
     installed). The id is launch.steam_app_id, else the game's
     steamworkshop source, else the Steam library the game is installed
     in - so a game added from Steam launches with nothing configured.

launch.via in games.yaml picks one method instead.

The game starts as a transient systemd user service (systemd-run --user),
so it does not belong to lmm: closing the terminal or restarting
'lmm serve' leaves it running. With no systemd user manager, lmm starts
it detached instead. lmm returns at once; through Steam, success means
Steam was asked to start the game.

Only the active profile can be launched: -p/--profile naming any other
profile is refused. Make it active first with 'lmm profile switch'.

When the profile has changes that are not deployed - a mod enabled but
not deployed, or deployed but disabled - lmm warns and launches anyway.
--deploy deploys the profile first; --strict refuses instead.

Use --dry-run to print the method and the full command line without
starting anything.

Examples:
  lmm launch --game valheim
  lmm launch --game valheim --deploy
  lmm launch --game valheim --dry-run`,
	Args: cobra.NoArgs,
	RunE: runLaunch,
}

func init() {
	launchCmd.Flags().StringVarP(&launchProfile, "profile", "p", "", "profile; only the active profile can be launched (default: active profile)")
	launchCmd.Flags().BoolVar(&launchDeploy, "deploy", false, "deploy the profile before launching")
	launchCmd.Flags().BoolVar(&launchStrict, "strict", false, "refuse to launch when the profile has undeployed changes")
	launchCmd.Flags().BoolVar(&launchDryRun, "dry-run", false, "print how the game would be launched without starting it")

	rootCmd.AddCommand(launchCmd)
}

func runLaunch(cmd *cobra.Command, _ []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doLaunch(ctx, service, game)
	})
}

func doLaunch(ctx context.Context, service *core.Service, game *domain.Game) error {
	opts := core.LaunchOptions{
		Profile: launchProfile,
		// The CLI warns about undeployed changes and launches anyway;
		// --strict is what refuses.
		Force:         !launchStrict,
		DryRun:        launchDryRun,
		AllowDetached: true,
	}

	if launchDeploy {
		if launchDryRun {
			if !jsonOutput {
				fmt.Println("Would deploy the profile first.")
			}
		} else if err := deployBeforeLaunch(ctx, service, game); err != nil {
			return err
		}
	}

	result, err := service.Launch(ctx, game, opts)
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(result)
	}

	for _, w := range result.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}
	command := shellJoin(result.Command)
	name := game.Name
	if name == "" {
		name = game.ID
	}
	switch {
	case result.DryRun:
		fmt.Printf("Would launch %s (profile %s) via %s:\n  %s\n", name, result.Profile, result.Method, command)
	case result.Requested:
		fmt.Printf("Asked Steam to start %s (profile %s).\n", name, result.Profile)
	default:
		fmt.Printf("Started %s (profile %s).\n", name, result.Profile)
	}
	if !result.DryRun {
		if result.Unit != "" {
			fmt.Printf("Running as systemd user unit %s.\n", result.Unit)
		} else {
			fmt.Println("Started detached: no systemd user manager is available.")
		}
		if verbose {
			fmt.Printf("  %s\n", command)
		}
	}
	return nil
}

// deployBeforeLaunch is --deploy: the active profile's deploy, as `lmm
// deploy` runs it, before the launch.
func deployBeforeLaunch(ctx context.Context, service *core.Service, game *domain.Game) error {
	profile, err := resolveProfile(ctx, service, game.ID, launchProfile)
	if err != nil {
		return err
	}
	opts := core.DeployOptions{SkipHooks: noHooks}
	plan, err := service.PlanDeploy(ctx, game, profile, opts)
	if err != nil {
		return err
	}
	result, err := service.ApplyDeploy(ctx, game, plan, opts, quietSink(nil))
	if err != nil {
		return err
	}
	if !jsonOutput {
		for _, w := range result.Warnings {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
		}
		fmt.Printf("Deployed %d mod(s) to profile %s.\n", result.Deployed, profile)
	}
	return nil
}

// shellJoin is argv as a line a user could paste into a shell.
func shellJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = core.ShellQuoteArg(a)
	}
	return strings.Join(quoted, " ")
}
