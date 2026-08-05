package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/spf13/cobra"
)

// gameListJSON is the --json row shape for 'game list': Default is an
// explicit boolean (rather than embedding a marker in ID, as the table view
// does) so a JSON consumer doesn't need to string-match the ID field.
type gameListJSON struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	InstallPath string            `json:"install_path"`
	ModPath     string            `json:"mod_path"`
	DeployMode  string            `json:"deploy_mode"`
	Sources     map[string]string `json:"sources"`
	Default     bool              `json:"default"`
}

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
	games := service.ListGames()
	sort.Slice(games, func(i, j int) bool { return games[i].ID < games[j].ID })

	cfg, err := config.Load(service.ConfigDir())
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	defaultGame := cfg.DefaultGame

	if jsonOutput {
		rows := make([]gameListJSON, len(games))
		for i, g := range games {
			sources := g.SourceIDs
			if sources == nil {
				sources = map[string]string{}
			}
			rows[i] = gameListJSON{
				ID:          g.ID,
				Name:        g.Name,
				InstallPath: g.InstallPath,
				ModPath:     g.ModPath,
				DeployMode:  g.DeployMode.String(),
				Sources:     sources,
				Default:     g.ID == defaultGame,
			}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			return fmt.Errorf("encoding json: %w", err)
		}
		return nil
	}

	if len(games) == 0 {
		fmt.Println("No games configured.")
		fmt.Println("Use 'lmm game add' to configure one interactively, or 'lmm game detect' to scan Steam libraries for known games.")
		return nil
	}

	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(w, "ID\tNAME\tINSTALL PATH\tMOD PATH\tDEPLOY MODE\tSOURCES"); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	if _, err := fmt.Fprintln(w, "--\t----\t------------\t--------\t-----------\t-------"); err != nil {
		return fmt.Errorf("writing separator: %w", err)
	}
	for _, g := range games {
		id := g.ID
		if g.ID == defaultGame {
			id += " (default)"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
			id, g.Name, g.InstallPath, g.ModPath, g.DeployMode.String(), formatGameSources(g.SourceIDs)); err != nil {
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
