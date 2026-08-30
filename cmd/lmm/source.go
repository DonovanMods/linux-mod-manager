package main

import (
	"context"
	"fmt"
	"io"
	"strings"
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

// authDisplay rebuilds the pre-#301 "yes"/"no"/"n/a" display string from
// app.AuthState (final review, Important #1 / #301: source list --json and
// the text table both stay byte-identical by formatting from the data, not
// carrying pre-formatted text on the wire anymore).
func authDisplay(a app.AuthState) string {
	switch a {
	case app.AuthNone:
		return "n/a"
	case app.AuthRequired:
		return "no"
	case app.AuthAuthenticated:
		return "yes"
	default:
		return ""
	}
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
			//
			// Error precedence, restored (task-3 review, undisclosed
			// deviation): pre-#301 this command loaded the source
			// definitions before resolving the game, so an unreadable
			// sources/ directory was reported ahead of an invalid explicit
			// -g. app.SourceInfos now does both internally, in that same
			// order, but only once gameCtx is already resolved - so with
			// BOTH broken, the game error would win instead. This
			// precedence-only pre-check restores the old winner; the real
			// row assembly below still comes from app.SourceInfos.
			if _, _, err := app.LoadSourceDefinitions(svc.ConfigDir()); err != nil {
				return fmt.Errorf("loading source definitions: %w", err)
			}

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

			if jsonOutput {
				// A top-level array, as it has always been (#52 item 13: an
				// empty registry emits [], never null - emitJSON encodes a
				// nil slice as []). Indented like every other document since
				// v2 (Ruling 3); this was the one command emitting compact
				// JSON.
				return emitJSON(infos)
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			header := "ID\tNAME\tTYPE\tAUTH\tCAPABILITIES"
			if showInUseColumn {
				header += "\tIN USE"
			}
			fmt.Fprintln(w, header+"\tERROR")
			for _, info := range infos {
				// An error row carries no auth/capability data (it never
				// registered), so both columns stay blank here - the display
				// strings are derived from the wire-typed data, never carried
				// on the wire (final review, Important #1 / #301).
				var auth, caps string
				if info.Type != "error" {
					auth = authDisplay(info.Auth)
					caps = strings.Join(info.Capabilities, ",")
				}
				line := fmt.Sprintf("%s\t%s\t%s\t%s\t%s", info.ID, info.Name, info.Type, auth, caps)
				if showInUseColumn {
					// Error rows have no game association - leave the column
					// blank rather than claim "no".
					inUse := ""
					if info.Type != "error" {
						inUse = "no"
						if info.InUse {
							inUse = "yes"
						}
					}
					line += "\t" + inUse
				}
				// Deliberately unchecked, like the header write above: a
				// tabwriter buffers, so any write failure surfaces at Flush
				// below, which IS checked.
				_, _ = fmt.Fprintln(w, line+"\t"+info.ErrorMessage)
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
		// #309: an invalid definition keeps failing the command (non-zero
		// exit) under both plain and --json output - --json wraps the
		// failure in sourceValidationError so the envelope's "details"
		// carries the report (id/type empty: there was no definition to
		// read them from).
		report, def, err := app.ValidateSourceFile(args[0])
		if err != nil {
			return &sourceValidationError{err: err, report: report}
		}

		if !jsonOutput {
			//nolint:errcheck // best-effort console write
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s: valid (%s source %q)\n", report.Path, report.Type, report.ID)
		}
		if !sourceProbe {
			if jsonOutput {
				return emitJSON(report)
			}
			return nil
		}
		return withService(cmd, func(ctx context.Context, svc *core.Service) error {
			return probeSource(ctx, cmd, svc, def, report)
		})
	},
}

// probeSource constructs the definition's source and performs one live
// operation against it, so users can smoke-test a definition before relying
// on it (design §8). report already carries the definition's own
// validation result (Valid true - a probe never runs against an invalid
// one); this fills in report.Probe and, under --json, emits the whole
// report as either the single success document or (a live-check failure)
// the error envelope's details.
func probeSource(ctx context.Context, cmd *cobra.Command, svc *core.Service, def source.SourceDefinition, report *app.SourceValidationReport) error {
	summary, err := app.ProbeSource(ctx, svc, def, sourceProbeID)
	if err != nil {
		wrapped := fmt.Errorf("probe: %w", err)
		report.Probe = &app.SourceProbeResult{Error: err.Error()}
		if jsonOutput {
			return &sourceValidationError{err: wrapped, report: report}
		}
		return wrapped
	}
	report.Probe = &app.SourceProbeResult{OK: true, Summary: summary}
	if jsonOutput {
		return emitJSON(report)
	}
	//nolint:errcheck // best-effort console write
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "probe: %s\n", report.Probe.Summary)
	return nil
}

// sourceValidationError reports a `lmm source validate --json` failure - an
// invalid definition, or (with --probe) a live probe failure against an
// otherwise-valid one - for the --json error envelope: Details() is the
// SourceValidationReport itself, so a caller sees exactly which part
// failed instead of a bare message. Follows the core.ConflictError /
// gameDetectPartialError pattern (jsonout.go).
type sourceValidationError struct {
	err    error
	report *app.SourceValidationReport
}

// Error returns the wrapped failure's own message - the definition's
// load/validate error, or "probe: <cause>" - identical to what the plain
// path prints via Execute's "Error: %v".
func (e *sourceValidationError) Error() string { return e.err.Error() }

// Unwrap exposes the wrapped failure for errors.Is/errors.As.
func (e *sourceValidationError) Unwrap() error { return e.err }

// Details returns the SourceValidationReport for the --json error
// envelope's "details" field.
func (e *sourceValidationError) Details() any { return e.report }

func init() {
	sourceValidateCmd.Flags().BoolVar(&sourceProbe, "probe", false, "perform a live smoke test after validation")
	sourceValidateCmd.Flags().StringVar(&sourceProbeID, "id", "", "mod id to probe with (api definitions without a search endpoint)")

	sourceListCmd.Flags().BoolVar(&sourceAll, "all", false, "show the full registry (with an IN USE column) instead of scoping to the active game")

	sourceCmd.AddCommand(sourceListCmd)
	sourceCmd.AddCommand(sourceValidateCmd)
	rootCmd.AddCommand(sourceCmd)
}
