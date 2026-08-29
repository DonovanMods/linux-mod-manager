package main

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

var gameListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured games",
	Long: `List every game configured in games.yaml: ID, name, install path, mod
path, deploy mode, and configured sources. The default game (see 'lmm game
show-default') is marked "(default)" next to its ID.

Examples:
  lmm game list
  lmm game list --json`,
	Args: cobra.NoArgs,
	RunE: runGameList,
}

func init() {
	gameCmd.AddCommand(gameListCmd)
}

func runGameList(cmd *cobra.Command, args []string) error {
	return withService(cmd, func(ctx context.Context, service *core.Service) error {
		return doGameList(cmd, service)
	})
}

func doGameList(cmd *cobra.Command, service *core.Service) error {
	// core.ListGameEntries returns the games ordered by ID with the default
	// one marked; the only error it can report is the default-game lookup's.
	games, err := service.ListGameEntries(cmd.Context())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if jsonOutput {
		// A top-level array, as it has always been; emitJSON encodes an empty
		// registry as [] and a game's absent source map as {}, never null.
		return emitJSON(games)
	}

	if len(games) == 0 {
		fmt.Println("No games configured.")
		fmt.Println("Use 'lmm game add' to configure one interactively, or 'lmm game detect' to scan Steam libraries for known games.")
		return nil
	}

	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tNAME\tINSTALL PATH\tMOD PATH\tDEPLOY MODE\tCONVERT PAKS\tSOURCES"); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	if _, err := fmt.Fprintln(w, "--\t----\t------------\t--------\t-----------\t-----------\t-------"); err != nil {
		return fmt.Errorf("writing separator: %w", err)
	}
	for _, g := range games {
		id := g.ID
		if g.Default {
			id += " (default)"
		}
		convertPaksStr := ""
		if g.DeployMode == domain.DeployCompile {
			convertPaksStr = "off"
			if g.Game.ConvertPaks {
				convertPaksStr = "on"
			}
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			id, g.Name, g.InstallPath, g.ModPath, g.DeployMode.String(), convertPaksStr, formatGameSources(g.SourceIDs)); err != nil {
			return fmt.Errorf("writing row: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flushing output: %w", err)
	}

	return printTable(&buf, 2, nil)
}

// formatGameSources renders a game's SourceIDs map as a compact,
// deterministically-ordered "key:value,key:value" string for table display
// ("-" when the game has no sources configured).
func formatGameSources(sources map[string]string) string {
	if len(sources) == 0 {
		return "-"
	}
	keys := make([]string, 0, len(sources))
	for k := range sources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+":"+sources[k])
	}
	return strings.Join(parts, ",")
}
