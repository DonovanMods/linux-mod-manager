package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/spf13/cobra"
)

var sourceCmd = &cobra.Command{
	Use:   "source",
	Short: "Manage mod sources",
	Long: `List registered mod sources and validate user-defined source definitions.

Custom sources (directory scans, static manifests, or REST APIs) are
defined as YAML files in the sources/ directory under the config dir
(~/.config/lmm/sources/*.yaml by default) - see the Custom Sources
section of the project README for the file format.`,
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
		// row. Silence the warnings for the duration of this command's own
		// service init; the rows below are the canonical report here.
		prevWarnOut := customSourceWarnOut
		customSourceWarnOut = io.Discard
		defer func() { customSourceWarnOut = prevWarnOut }()

		return withService(cmd, func(ctx context.Context, svc *core.Service) error {
			cfg, err := getServiceConfig()
			if err != nil {
				return err
			}
			defs, loadErrs, err := config.LoadSourceDefinitions(cfg.ConfigDir)
			if err != nil {
				return fmt.Errorf("loading source definitions: %w", err)
			}

			// Reclassify each definition against what actually ended up registered
			// (registerCustomSources may have skipped it on ID collision or
			// construction failure) so the list reflects reality rather than just
			// "a definition with this ID exists".
			var errRows []sourceInfo
			for _, d := range defs {
				registered, err := svc.GetSource(d.ID)
				switch {
				case err == nil && isCustomSource(registered):
					// Registered successfully as this definition's own custom
					// source; its row (built below from svc.ListSources()) will
					// carry the correct TypeLabel() on its own — nothing to
					// record here.
				case err == nil:
					// Something else (a built-in, or another def) already held this ID.
					errRows = append(errRows, sourceInfo{ID: d.ID, Type: "error", Error: "id already in use"})
				default:
					// Nothing registered under this ID: construction must have failed.
					// Re-run it to recover the actual error for display.
					if _, cerr := custom.New(d); cerr != nil {
						errRows = append(errRows, sourceInfo{ID: d.ID, Type: "error", Error: cerr.Error()})
					}
				}
			}

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
				gameCtx, err = svc.GetGame(gameID)
				if err != nil {
					return err
				}
			}

			// srcs is the base row set: every registered source (built-in +
			// custom) by default, or - with a resolvable game and no --all -
			// just that game's configured+registered subset (SourcesForGame).
			// inUseIDs stays nil except in the --all-with-game combination,
			// which is the one case that needs to mark a subset of the FULL
			// list rather than simply restricting to it.
			// ListSources is registry-map order (nondeterministic, and a
			// pre-existing quirk of this command); sort so the full-registry
			// views are stable and consistent with SourcesForGame's sorted
			// scoped view.
			srcs := svc.ListSources()
			sort.Slice(srcs, func(i, j int) bool { return srcs[i].ID() < srcs[j].ID() })
			var inUseIDs map[string]bool
			switch {
			case gameCtx != nil && !sourceAll:
				srcs, err = svc.SourcesForGame(gameCtx.ID)
				if err != nil {
					return err
				}
			case gameCtx != nil:
				scoped, err := svc.SourcesForGame(gameCtx.ID)
				if err != nil {
					return err
				}
				inUseIDs = make(map[string]bool, len(scoped))
				for _, s := range scoped {
					inUseIDs[s.ID()] = true
				}
			}
			showInUseColumn := inUseIDs != nil

			// make(...,0,...), not `var rows []sourceInfo`: a nil slice encodes
			// to JSON `null`, but `source list --json` should always emit an
			// array — empty when there is nothing to report (#52 item 13).
			rows := make([]sourceInfo, 0, len(srcs)+len(errRows)+len(loadErrs))
			for _, src := range srcs {
				rows = append(rows, sourceInfo{
					ID:           src.ID(),
					Name:         src.Name(),
					Type:         source.TypeLabelOf(src),
					Auth:         authState(src),
					Capabilities: capabilitySummary(source.CapabilitiesOf(src)),
					InUse:        inUseIDs[src.ID()],
				})
			}
			// Broken-definition error rows stay visible in every view (design
			// §5): they never registered, so they have no game association to
			// scope by, and hiding them would bury exactly the diagnostics a
			// user debugging their YAML needs.
			rows = append(rows, errRows...)
			for _, le := range loadErrs {
				rows = append(rows, sourceInfo{
					ID:    le.File,
					Type:  "error",
					Error: le.Err.Error(),
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
		def, err := config.LoadSourceDefinitionFile(args[0])
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
func probeSource(ctx context.Context, cmd *cobra.Command, svc *core.Service, def custom.SourceDefinition) error {
	src, err := custom.New(def)
	if err != nil {
		return fmt.Errorf("probe: constructing source: %w", err)
	}
	if a, ok := src.(interface{ SetAPIKey(string) }); ok {
		// envKeyFor(src) rather than envKeyForSourceID(def.ID) directly: today
		// no custom type implements EnvKeyProvider, so both resolve to the
		// same derived LMM_<ID>_API_KEY name and behavior is unchanged - but
		// routing through envKeyFor keeps this call from silently diverging
		// from every other env-key lookup in the codebase if a custom source
		// ever does implement EnvKeyProvider.
		if key := getSourceAPIKey(svc, def.ID, envKeyFor(src)); key != "" {
			a.SetAPIKey(key)
		}
	}

	switch def.Type {
	case custom.TypeDirectory, custom.TypeManifest:
		res, err := src.Search(ctx, source.SearchQuery{PageSize: 1})
		if err != nil {
			return fmt.Errorf("probe: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "probe: ok — %d mod(s) visible\n", res.TotalCount)
	case custom.TypeAPI:
		if def.API.Endpoints.Search != nil {
			res, err := src.Search(ctx, source.SearchQuery{PageSize: 1})
			if err != nil {
				return fmt.Errorf("probe: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "probe: ok — search responded (%d total reported)\n", res.TotalCount)
			return nil
		}
		if sourceProbeID == "" {
			return fmt.Errorf("probe: this definition has no search endpoint; provide a known mod id with --id to probe get_mod")
		}
		mod, err := src.GetMod(ctx, "", sourceProbeID)
		if err != nil {
			return fmt.Errorf("probe: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "probe: ok — get_mod %s returned %q\n", sourceProbeID, mod.Name)
	}
	return nil
}

// isCustomSource reports whether src is a user-defined source (as opposed to
// a built-in like NexusMods/CurseForge): a self-reported type of exactly
// "directory", "manifest", or "api". "built-in" and the "unknown" fallback
// both answer false — conservative on the unknown side so the definitions
// reclassify loop (the only call site) reports a collision/error row rather
// than assuming an unlabeled source is the definition's own. Unreachable in
// practice: LoadSourceDefinitions guarantees ID uniqueness within a load, so
// a registered source matching a definition's ID is either a built-in or
// that definition's own constructed source — never an unrelated third party.
func isCustomSource(src source.ModSource) bool {
	switch source.TypeLabelOf(src) {
	case "directory", "manifest", "api":
		return true
	}
	return false
}

// authState reports a source's authentication status for display.
func authState(src source.ModSource) string {
	if !source.CapabilitiesOf(src).Auth {
		return "n/a"
	}
	if a, ok := src.(interface{ IsAuthenticated() bool }); ok {
		if a.IsAuthenticated() {
			return "yes"
		}
		return "no"
	}
	return "yes"
}

// capabilitySummary renders capabilities as a compact list, e.g. "search,updates".
func capabilitySummary(c source.Capabilities) string {
	out := ""
	add := func(enabled bool, name string) {
		if !enabled {
			return
		}
		if out != "" {
			out += ","
		}
		out += name
	}
	add(c.Search, "search")
	add(c.Dependencies, "deps")
	add(c.Updates, "updates")
	add(c.Auth, "auth")
	return out
}

func init() {
	sourceValidateCmd.Flags().BoolVar(&sourceProbe, "probe", false, "perform a live smoke test after validation")
	sourceValidateCmd.Flags().StringVar(&sourceProbeID, "id", "", "mod id to probe with (api definitions without a search endpoint)")

	sourceListCmd.Flags().BoolVar(&sourceAll, "all", false, "show the full registry (with an IN USE column) instead of scoping to the active game")

	sourceCmd.AddCommand(sourceListCmd)
	sourceCmd.AddCommand(sourceValidateCmd)
	rootCmd.AddCommand(sourceCmd)
}
