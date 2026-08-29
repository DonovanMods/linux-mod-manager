package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/app"
	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/spf13/cobra"
)

var sourceCmd = &cobra.Command{
	Use:   "source",
	Short: "Manage mod sources",
	Long: `List registered mod sources and validate user-defined source definitions.

Custom sources (directory scans, static manifests, or REST APIs) are
defined as YAML files in the sources/ directory under the config dir
(see the FILES section of 'lmm --help' for the config directory) - see
the Custom Sources section of the project README for the file format.`,
}

// sourceInfo is one row of `lmm source list` output.
type sourceInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"` // "built-in", "directory", "manifest", "api", or "error"
	Auth         string `json:"auth"` // "yes", "no", "n/a"
	Capabilities string `json:"capabilities"`
	// InUse marks a row as one of the active game's configured sources.
	// Only ever set (and only ever rendered as a column, in both --json and
	// the text table) in the --all-with-game-resolvable combination (design
	// §5) - additive per the repo's JSON-contract-additions-are-MINOR
	// precedent: omitempty means a scoped or no-game-context response never
	// gains an "in_use" key at all, exactly the pre-Task-4 shape those
	// callers already depend on.
	InUse bool   `json:"in_use,omitempty"`
	Error string `json:"error,omitempty"`
}

var sourceAll bool

var sourceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all mod sources",
	Long: `List built-in and user-defined mod sources, including definitions that failed to load.

With a resolvable game (-g, or a default set via 'lmm game set-default'),
the list scopes to that game's configured sources by default. --all shows
every registered source instead, marking the active game's sources in an
IN USE column. With no game resolvable, --all has no effect: the full
registry is shown either way, exactly as when no game exists at all.
Definitions that failed to load are always shown, in every view.

Examples:
  lmm source list
  lmm source list --all
  lmm source list --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// registerCustomSources' init-time stderr warnings would double up on
		// the error rows rendered below (#52 item 14): the same broken
		// definition would otherwise be reported once on stderr and once as a
		// row. Silence the warnings for this command's own service init; the
		// rows below are the canonical report here.
		return withServiceOpts(cmd, app.Options{WarnWriter: io.Discard}, func(ctx context.Context, svc *core.Service) error {
			// Resolve a game context WITHOUT erroring when absent (design §5:
			// "the command must keep working with zero games configured").
			// requireGame's own error is deliberately swallowed here — and
			// that error covers TWO distinct causes it doesn't distinguish
			// itself: the ordinary "no -g, no default game set" case, AND a
			// failure loading config.yaml while it looks up the default game
			// (requireGame's config.Load err falls through to the same "no
			// game specified" message rather than surfacing separately). Both
			// degrade identically here, to the full-registry view — do not
			// "fix" this into an error for the config-load case; an explicit,
			// invalid -g still surfaces via GetGame below, same as every
			// other command's game resolution.
			var gameCtx *domain.Game
			if requireErr := requireGame(cmd); requireErr == nil {
				g, err := svc.GetGame(gameID)
				if err != nil {
					return err
				}
				gameCtx = g
			}

			// app.SourceInfos owns the row assembly: the registry, the
			// game scoping, and the definitions-vs-reality reclassification
			// that only app can do (it alone sees the definitions on disk and
			// can re-run a failed construction). The IN USE column belongs to
			// the one combination that marks a subset of the FULL list rather
			// than restricting to it.
			infos, err := app.SourceInfos(ctx, svc, gameCtx, sourceAll)
			if err != nil {
				return err
			}
			showInUseColumn := gameCtx != nil && sourceAll

			// make(...,0,...), not `var rows []sourceInfo`: a nil slice encodes
			// to JSON `null`, but `source list --json` should always emit an
			// array — empty when there is nothing to report (#52 item 13).
			rows := make([]sourceInfo, 0, len(infos))
			for _, info := range infos {
				rows = append(rows, sourceInfo{
					ID:           info.ID,
					Name:         info.Name,
					Type:         info.Type,
					Auth:         info.Auth,
					Capabilities: info.Capabilities,
					InUse:        info.InUse,
					Error:        info.ErrorMessage,
				})
			}

			if jsonOutput {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			header := "ID\tNAME\tTYPE\tAUTH\tCAPABILITIES"
			if showInUseColumn {
				header += "\tIN USE"
			}
			fmt.Fprintln(w, header+"\tERROR")
			for _, r := range rows {
				line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s", r.ID, r.Name, r.Type, r.Auth, r.Capabilities)
				if showInUseColumn {
					// Error rows have no game association (see the append
					// above) — leave the column blank rather than claim "no".
					inUse := ""
					if r.Type != "error" {
						inUse = "no"
						if r.InUse {
							inUse = "yes"
						}
					}
					line += "\t" + inUse
				}
				fmt.Fprintln(w, line+"\t"+r.Error)
			}
			return w.Flush()
		})
	},
}

var (
	sourceProbe   bool
	sourceProbeID string
)

var sourceValidateCmd = &cobra.Command{
	Use:   "validate <file>",
	Short: "Validate a source definition file",
	Long: `Parse and validate a user-defined source definition YAML file, reporting any problems.

With --probe, also perform a live smoke test: a directory scan, a
manifest fetch+parse, or an API call. For an api-type definition with no
search endpoint, --id supplies a known mod ID to probe get_mod with.

Examples:
  lmm source validate ~/.config/lmm/sources/my-source.yaml
  lmm source validate ~/.config/lmm/sources/my-source.yaml --probe
  lmm source validate ~/.config/lmm/sources/my-source.yaml --probe --id 12345`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		def, err := app.LoadSourceDefinitionFile(args[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: valid (%s source %q)\n", args[0], def.Type, def.ID)
		if !sourceProbe {
			return nil
		}
		return withService(cmd, func(ctx context.Context, svc *core.Service) error {
			return probeSource(ctx, cmd, svc, def)
		})
	},
}

// probeSource constructs the definition's source and performs one live
// operation against it, so users can smoke-test a definition before relying
// on it (design §8).
func probeSource(ctx context.Context, cmd *cobra.Command, svc *core.Service, def source.SourceDefinition) error {
	summary, err := app.ProbeSource(ctx, svc, def, sourceProbeID)
	if err != nil {
		return fmt.Errorf("probe: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "probe: %s\n", summary)
	return nil
}

func init() {
	sourceValidateCmd.Flags().BoolVar(&sourceProbe, "probe", false, "perform a live smoke test after validation")
	sourceValidateCmd.Flags().StringVar(&sourceProbeID, "id", "", "mod id to probe with (api definitions without a search endpoint)")

	sourceListCmd.Flags().BoolVar(&sourceAll, "all", false, "show the full registry (with an IN USE column) instead of scoping to the active game")

	sourceCmd.AddCommand(sourceListCmd)
	sourceCmd.AddCommand(sourceValidateCmd)
	rootCmd.AddCommand(sourceCmd)
}
