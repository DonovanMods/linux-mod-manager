package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
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
	Error        string `json:"error,omitempty"`
}

var sourceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all mod sources",
	Long: `List built-in and user-defined mod sources, including definitions that failed to load.

Examples:
  lmm source list
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

			// make(...,0,...), not `var rows []sourceInfo`: a nil slice encodes
			// to JSON `null`, but `source list --json` should always emit an
			// array — empty when there is nothing to report (#52 item 13).
			srcs := svc.ListSources()
			rows := make([]sourceInfo, 0, len(srcs)+len(errRows)+len(loadErrs))
			for _, src := range srcs {
				rows = append(rows, sourceInfo{
					ID:           src.ID(),
					Name:         src.Name(),
					Type:         source.TypeLabelOf(src),
					Auth:         authState(src),
					Capabilities: capabilitySummary(source.CapabilitiesOf(src)),
				})
			}
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
			fmt.Fprintln(w, "ID\tNAME\tTYPE\tAUTH\tCAPABILITIES\tERROR")
			for _, r := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Name, r.Type, r.Auth, r.Capabilities, r.Error)
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
		if key := getSourceAPIKey(svc, def.ID, envKeyForSourceID(def.ID)); key != "" {
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
// a built-in like NexusMods/CurseForge): any source whose self-reported type
// is not "built-in". Sound at its only call site (the definitions reclassify
// loop) because LoadSourceDefinitions guarantees ID uniqueness within a load,
// so a registered source matching a definition's ID is either a built-in or
// that definition's own constructed source — never an unrelated third party.
func isCustomSource(src source.ModSource) bool {
	return source.TypeLabelOf(src) != "built-in"
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

	sourceCmd.AddCommand(sourceListCmd)
	sourceCmd.AddCommand(sourceValidateCmd)
	rootCmd.AddCommand(sourceCmd)
}
