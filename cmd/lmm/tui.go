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
search results, and check for updates - with every mutating action behind a
confirmation prompt.

Use --prototype for a demo mode backed by static fake data:

  lmm tui --prototype --theme amber`,
	RunE: runTUI,
}

func init() {
	tuiCmd.Flags().BoolVar(&tuiOptions.prototype, "prototype", false, "run the side-effect-free fake-data TUI prototype")
	tuiCmd.Flags().StringVar(&tuiOptions.theme, "theme", "wizardry", "TUI theme (wizardry, amber, dos, green)")
	rootCmd.AddCommand(tuiCmd)
}

func runTUI(cmd *cobra.Command, args []string) error {
	if tuiOptions.prototype {
		model, err := tui.NewPrototypeModel(tui.Options{Theme: tuiOptions.theme, Ctx: cmd.Context()})
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
