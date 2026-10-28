package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
)

var gameListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured games",
	Long: `List every game configured in games.yaml: ID, name, install path, mod
path, adapter, deploy mode, and configured sources. The default game (see 'lmm game
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
	w := newDisplayWidthTableWriter(&buf)
	if _, err := fmt.Fprintln(w, "ID\tNAME\tINSTALL PATH\tMOD PATH\tADAPTER\tDEPLOY MODE\tCONVERT PAKS\tSOURCES"); err != nil {
		return fmt.Errorf("writing header: %w", err)
	}
	if _, err := fmt.Fprintln(w, "--\t----\t------------\t--------\t-------\t-----------\t-----------\t-------"); err != nil {
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
		modPath := g.ModPath
		if g.ModPathError != "" {
			modPath += " (needs repair)"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			id, g.Name, g.InstallPath, modPath, formatGameAdapter(g), g.DeployMode.String(), convertPaksStr, formatGameSources(g.SourceIDs)); err != nil {
			return fmt.Errorf("writing row: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flushing output: %w", err)
	}

	if err := printTable(&buf, 2, nil); err != nil {
		return err
	}
	printModPathProblems(games)
	return nil
}

// printModPathProblems follows a game table with the repair for each game
// whose mod_path needs attention (#427): the table cell can only say
// "(needs repair)", and a flag with no next step is the complaint #427 was
// filed about. On stderr, in the load-time warnings' format, so the table on
// stdout stays exactly one table.
func printModPathProblems(games []core.GameListEntry) {
	for _, g := range games {
		warnModPath(g.ID, g.ModPathError)
	}
}

// warnModPath prints one game's mod_path problem, if it has one.
func warnModPath(gameID, problem string) {
	if problem != "" {
		fmt.Fprintf(os.Stderr, "warning: %s: %s\n", gameID, problem)
	}
}

// formatGameAdapter renders a game's adapter for a table cell or a detail
// line (#353).
//
// It reads EffectiveAdapter, the adapter the game actually resolves to
// (#426), rather than the configured key: a `deploy_mode: compile` game
// compiles through icarus and a game with BepInEx lays its archives out
// through bepinex whether or not games.yaml says so, and this column exists
// to say which adapter a game uses. An absent one IS the generic-files
// identity, so the cell names it rather than leaving a blank.
//
// The exception is a game every flow refuses (AdapterError, #413 re-review
// L2): it uses no adapter, so the cell names the configured one and says it
// is refused, rather than reading generic-files for a game nothing deploys
// to. `lmm game show` prints the refusal itself.
func formatGameAdapter(entry core.GameListEntry) string {
	if entry.AdapterError != "" {
		return formatAdapterName(entry.Adapter) + " (refused)"
	}
	return formatAdapterName(entry.EffectiveAdapter)
}

// formatAdapterName renders one adapter name, the empty one as the
// identity it means.
func formatAdapterName(name string) string {
	if name == "" {
		return "generic-files"
	}
	return name
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
