package main

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/tui"
)

var tuiOptions struct {
	prototype bool
	theme     string
}

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Launch the interactive terminal UI",
	Long: `Launch the interactive terminal UI.

Shows the configured game's installed mods, profiles, and status using the
same config, database, and game resolution as the CLI commands. Search mod
sources interactively, inspect the source registry, and manage mods in
place - enable/disable, uninstall, deploy, switch profiles, install from
search results, check for updates (with changelogs and rollback), reorder
load order, resolve file conflicts, edit update policies, view deployed
files, purge a profile, switch games, and create/delete/export/import
profiles - with every mutating action behind a confirmation prompt.

Always operates on the game's active profile; there is no -p/--profile
flag (switch profiles from inside the TUI instead).

Press ? at any time for in-app help listing every keybinding, 1-6 to jump
directly to a screen (dashboard, installed mods, search, profiles,
sources, conflicts), and tab/shift-tab (or h/l) to cycle screens. Search
results scroll infinitely rather than paginating: n/p jump the selection
forward/back by a paneful of rows (triggering a refill near the end of
what's loaded), not to the next/previous page.

Colored output respects both --no-color and the NO_COLOR environment
variable, matching the CLI's own color handling.

Use --prototype to run a demo mode backed by static fake data: no game
config, database, or network access is touched, so it works anywhere.

  lmm tui --prototype --theme amber

--theme selects a color scheme: wizardry (default), amber, dos, or green
(green-phosphor and phosphor are accepted as aliases for green).`,
	RunE: runTUI,
}

func init() {
	tuiCmd.Flags().BoolVar(&tuiOptions.prototype, "prototype", false, "run the side-effect-free fake-data TUI prototype")
	tuiCmd.Flags().StringVar(&tuiOptions.theme, "theme", "wizardry", "TUI theme (wizardry, amber, dos, green)")
	rootCmd.AddCommand(tuiCmd)
}

func runTUI(cmd *cobra.Command, args []string) error {
	// colorEnabled() honors both --no-color and NO_COLOR (root.go), so the
	// TUI's own pin (Options.NoColor) stays in lockstep with the CLI's
	// color helpers rather than re-deriving the same two checks here.
	noColorOpt := !colorEnabled()

	if tuiOptions.prototype {
		model, err := tui.NewPrototypeModel(tui.Options{Theme: tuiOptions.theme, Ctx: cmd.Context(), NoColor: noColorOpt})
		if err != nil {
			return err
		}
		return runTUIProgram(cmd.Context(), model)
	}

	return withGameService(cmd, func(ctx context.Context, svc *core.Service, game *domain.Game) error {
		profileName, err := resolveProfile(svc, game.ID, "")
		if err != nil {
			return err
		}

		model, err := tui.NewModel(tui.Options{
			Theme:    tuiOptions.theme,
			Provider: tui.NewCoreProvider(svc, game, profileName),
			Actions:  tui.NewCoreActions(svc, game, profileName),
			Ctx:      ctx,
			NoColor:  noColorOpt,
			GameName: game.Name,
		})
		if err != nil {
			return err
		}
		return runTUIProgram(ctx, model)
	})
}

func runTUIProgram(ctx context.Context, model tui.Model) error {
	if _, err := tea.NewProgram(model, tea.WithContext(ctx), tea.WithAltScreen()).Run(); err != nil {
		return fmt.Errorf("running TUI: %w", err)
	}
	return nil
}
