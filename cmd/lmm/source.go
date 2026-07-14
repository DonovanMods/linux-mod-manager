package main

import (
	"context"
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
	"github.com/spf13/cobra"
)

var sourceCmd = &cobra.Command{
	Use:   "source",
	Short: "Manage mod sources",
	Long:  "List registered mod sources and validate user-defined source definitions.",
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
	Long:  "List built-in and user-defined mod sources, including definitions that failed to load.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return withService(cmd, func(ctx context.Context, svc *core.Service) error {
			cfg, err := getServiceConfig()
			if err != nil {
				return err
			}
			defs, loadErrs, err := config.LoadSourceDefinitions(cfg.ConfigDir)
			if err != nil {
				return fmt.Errorf("loading source definitions: %w", err)
			}

			defTypes := make(map[string]string, len(defs))
			for _, d := range defs {
				defTypes[d.ID] = d.Type
			}

			var rows []sourceInfo
			for _, src := range svc.ListSources() {
				typ, isCustom := defTypes[src.ID()]
				if !isCustom {
					typ = "built-in"
				}
				rows = append(rows, sourceInfo{
					ID:           src.ID(),
					Name:         src.Name(),
					Type:         typ,
					Auth:         authState(src),
					Capabilities: capabilitySummary(source.CapabilitiesOf(src)),
				})
			}
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

var sourceValidateCmd = &cobra.Command{
	Use:   "validate <file>",
	Short: "Validate a source definition file",
	Long:  "Parse and validate a user-defined source definition YAML file, reporting any problems.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		def, err := config.LoadSourceDefinitionFile(args[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s: valid (%s source %q)\n", args[0], def.Type, def.ID)
		return nil
	},
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
	sourceCmd.AddCommand(sourceListCmd)
	sourceCmd.AddCommand(sourceValidateCmd)
	rootCmd.AddCommand(sourceCmd)
}
