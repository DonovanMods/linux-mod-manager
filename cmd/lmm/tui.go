package main

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

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

The TUI is currently available as a safe prototype backed by static fake data:

  lmm tui --prototype --theme wizardry`,
	RunE: runTUI,
}

func init() {
	tuiCmd.Flags().BoolVar(&tuiOptions.prototype, "prototype", false, "run the side-effect-free fake-data TUI prototype")
	tuiCmd.Flags().StringVar(&tuiOptions.theme, "theme", "wizardry", "TUI theme (wizardry, amber, dos, green)")
	rootCmd.AddCommand(tuiCmd)
}

func runTUI(cmd *cobra.Command, args []string) error {
	if !tuiOptions.prototype {
		return fmt.Errorf("real TUI mode is not implemented yet; use --prototype")
	}

	model, err := tui.NewPrototypeModel(tui.Options{Theme: tuiOptions.theme, Prototype: true})
	if err != nil {
		return err
	}

	_, err = tea.NewProgram(model, tea.WithContext(cmd.Context())).Run()
	if err != nil {
		return fmt.Errorf("running TUI: %w", err)
	}
	return nil
}
