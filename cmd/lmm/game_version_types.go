package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
)

// curseForgeSourceID is the source a game's curseforge.game_version_types
// belongs to (#544).
const curseForgeSourceID = "curseforge"

// gameVersionTypesCmd is a command of its own rather than a flag on 'game
// edit' (#563): it only reads, it has its own --json document, and an edit
// command that sometimes edits nothing is harder to script against.
var gameVersionTypesCmd = &cobra.Command{
	Use:   "version-types <game-id>",
	Short: "List the CurseForge game version types a game can accept",
	Long: `List the game version types CurseForge publishes for a game - every
flavor CurseForge files the game's addons under, by name, slug and id -
and mark the ones the game accepts now.

Pass a slug or an id to 'lmm game edit --curseforge-version-type' to choose
which ones the game accepts. The game must use the curseforge source, and
the list needs your CurseForge API key ('lmm auth login curseforge').

Examples:
  lmm game version-types wow-classic
  lmm game version-types wow-classic --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withService(cmd, func(ctx context.Context, service *core.Service) error {
			return doGameVersionTypes(ctx, service, args[0])
		})
	},
}

func init() {
	gameCmd.AddCommand(gameVersionTypesCmd)
}

// doGameVersionTypes renders core.GameVersionTypesReport: the document
// itself under --json, a table otherwise.
func doGameVersionTypes(ctx context.Context, service *core.Service, gameID string) error {
	report, err := service.GameVersionTypes(ctx, gameID, curseForgeSourceID)
	if err != nil {
		if errors.Is(err, domain.ErrAuthRequired) {
			return authPromptError(curseForgeSourceID)
		}
		return err
	}
	if jsonOutput {
		return emitJSON(report)
	}

	game, err := service.GetGame(gameID)
	if err != nil {
		return err
	}
	accepted := game.CurseForgeVersionTypes()

	fmt.Printf("CurseForge game version types for %s %s\n", colorBold(game.Name), colorDim("("+game.ID+")"))
	if len(report.Types) == 0 {
		fmt.Println("  CurseForge publishes none for this game.")
	}
	nameWidth, slugWidth := len("NAME"), len("SLUG")
	for _, t := range report.Types {
		nameWidth, slugWidth = max(nameWidth, len(t.Name)), max(slugWidth, len(t.Slug))
	}
	if len(report.Types) > 0 {
		fmt.Printf("    %-*s  %-*s  %s\n", nameWidth, "NAME", slugWidth, "SLUG", "ID")
	}
	listed := make([]int, 0, len(report.Types))
	for _, t := range report.Types {
		listed = append(listed, t.ID)
		mark := " "
		if slices.Contains(accepted, t.ID) {
			mark = colorGreen("✓")
		}
		fmt.Printf("  %s %-*s  %-*s  %d\n", mark, nameWidth, t.Name, slugWidth, t.Slug, t.ID)
	}
	for _, id := range accepted {
		if !slices.Contains(listed, id) {
			fmt.Printf("  %s %s\n", colorYellow("!"), "accepts "+strconv.Itoa(id)+", which CurseForge no longer lists for this game")
		}
	}
	if len(accepted) == 0 {
		fmt.Println(colorDim("  The game accepts every CurseForge file."))
	}
	fmt.Println(colorDim("  Choose with: lmm game edit " + game.ID + " --curseforge-version-type <slug or id>"))
	return nil
}
