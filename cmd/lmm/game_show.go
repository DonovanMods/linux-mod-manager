package main

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
)

var gameShowCmd = &cobra.Command{
	Use:   "show <game-id>",
	Short: "Show one game's configuration and its mod-loader status",
	Long: `Print everything lmm knows about one configured game: its paths, its
source mappings, and - for a game that uses a mod loader - what that
loader's state actually is.

The loader section is the reason this command exists. BepInEx on Linux
needs a Steam LAUNCH OPTION, which lmm deliberately does not write for
you: Steam's launch options live in a file that must be edited with the
client closed, in an undocumented format, where a bad write loses every
option for every game in your account. So lmm works out which bootstrap
your game needs - a native Linux build uses BepInEx's run_bepinex.sh, a
Proton game uses the Windows winhttp proxy - and prints the exact string
to paste. 'lmm verify' then tells you whether it worked.

Nothing here launches the game, reads your Steam configuration or touches
a Proton prefix; the runtime and bootstrap are read from files in the
game's own install directory.

Examples:
  lmm game show valheim
  lmm game show valheim --json`,
	Args: cobra.ExactArgs(1),
	RunE: runGameShow,
}

func init() {
	gameCmd.AddCommand(gameShowCmd)
}

func runGameShow(cmd *cobra.Command, args []string) error {
	return withService(cmd, func(ctx context.Context, service *core.Service) error {
		return doGameShow(ctx, service, args[0])
	})
}

// doGameShow renders core.GameDetail - Ruling 15: the document itself under
// --json, the console lines otherwise. Core owns every answer here; this
// function only formats.
func doGameShow(ctx context.Context, service *core.Service, gameID string) error {
	detail, err := service.GameDetail(ctx, gameID)
	if err != nil {
		return err
	}

	if jsonOutput {
		return emitJSON(detail)
	}

	fmt.Printf("%s %s\n", colorBold(detail.Name), colorDim("("+detail.ID+")"))
	if detail.Default {
		fmt.Printf("  %s\n", colorDim("default game"))
	}
	fmt.Printf("  Install path: %s\n", detail.InstallPath)
	fmt.Printf("  Mod path:     %s\n", detail.ModPath)
	if detail.ModPath == detail.InstallPath {
		fmt.Printf("                %s\n", colorDim("mods deploy into the game root"))
	}
	fmt.Printf("  Link method:  %s\n", detail.LinkMethod)
	fmt.Printf("  Deploy mode:  %s\n", detail.DeployMode)
	// #353: the same cell `lmm game list` prints, spelled the same way -
	// an absent key IS the generic-files identity, so it is named rather
	// than left blank.
	fmt.Printf("  Adapter:      %s\n", formatGameAdapter(detail.Adapter))
	fmt.Printf("  Sources:      %s\n", formatGameSources(detail.SourceIDs))

	printLoaderStatus(detail.Loader)
	return nil
}

// printLoaderStatus renders the loader half of `lmm game show`.
//
// A game with no declaration still gets a section when its install directory
// looks like a loader game's, because the useful thing to tell someone who
// is about to set BepInEx up is which build and which launch option their
// game needs - which lmm can answer before anything is declared at all.
//
// A game that declares no loader, carries no marker and has no BepInEx on
// disk gets NO section: core.LoaderStatus.Relevant owns that rule so the
// server-side warning answers it the same way. Printing "Declared: none /
// Runtime: unknown / Bootstrap: unknown" and then advising
// `--loader-bootstrap` for an Unreal game is advice to configure something
// it neither has nor needs.
func printLoaderStatus(status *core.LoaderStatus) {
	if !status.Relevant() {
		return
	}
	fmt.Println()
	fmt.Println(colorBold("Mod loader"))

	if status.Declared == nil {
		// An UNDECLARED game with the preloader actually on disk is the
		// one state where the missing declaration is the whole story
		// (re-review R3): lmm will refuse to deploy a plugin into it, and
		// the fix is one command. Saying "none" and moving on left that
		// user with nothing to act on.
		if status.Installed {
			fmt.Printf("  Declared:     %s\n", colorRed("none - BepInEx is in the game directory but this game does not declare it"))
			fmt.Printf("                %s\n", colorDim(fmt.Sprintf("declare it with `lmm game edit %s --loader bepinex`", status.GameID)))
		} else {
			fmt.Printf("  Declared:     %s\n", colorDim("none"))
		}
	} else {
		version := status.Declared.Version
		if version == "" {
			version = colorDim("(no version declared)")
		}
		fmt.Printf("  Declared:     %s %s\n", status.Declared.Kind, version)
	}
	if status.Installed {
		fmt.Printf("  Installed:    %s\n", colorGreen("yes"))
	} else if status.Declared != nil {
		fmt.Printf("  Installed:    %s\n", colorRed("no - the preloader is not in the game directory"))
	}

	fmt.Printf("  Runtime:      %s\n", loaderValueOrUnknown(status.EffectiveRuntime.String()))
	fmt.Printf("  Bootstrap:    %s\n", loaderValueOrUnknown(status.EffectiveBootstrap.String()))
	if status.LoadedAt != "" {
		fmt.Printf("  Last loaded:  %s\n", status.LoadedAt)
	}

	if status.LaunchOption != "" {
		fmt.Println()
		fmt.Println("  Steam launch options for this game:")
		fmt.Printf("    %s\n", colorBold(status.LaunchOption))
		fmt.Println(colorDim("    (paste this into Steam > Properties > Launch Options; lmm never writes it for you)"))
	}

	for _, w := range status.Warnings {
		fmt.Printf("  %s %s\n", colorYellow("!"), w)
	}
}

// loaderValueOrUnknown renders an unanswered enum as a word rather than an
// empty column - "unknown" is a real answer here, and a blank line reads as
// a rendering bug.
func loaderValueOrUnknown(value string) string {
	if value == "" {
		return colorDim("unknown")
	}
	return value
}

// loaderSpecFromFlags builds the core.LoaderSpec a game-add or game-edit run
// declares, or nil when no --loader flag was given.
//
// The three detail flags are meaningless without --loader naming a kind, so
// they are rejected on their own rather than silently ignored: a user who
// typed `--loader-bootstrap proton` and nothing else has said something
// specific, and doing nothing about it is the wrong answer.
func loaderSpecFromFlags(kind, version, runtime, bootstrap string) (*core.LoaderSpec, error) {
	if kind == "" {
		if version != "" || runtime != "" || bootstrap != "" {
			return nil, fmt.Errorf("--loader-version/--loader-runtime/--loader-bootstrap need --loader <kind> (today: %s)", domain.LoaderKindBepInEx)
		}
		return nil, nil
	}
	return &core.LoaderSpec{Kind: kind, Version: version, Runtime: runtime, Bootstrap: bootstrap}, nil
}
