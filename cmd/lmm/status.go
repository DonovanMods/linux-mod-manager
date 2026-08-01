package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current status",
	Long: `Show the current status including configured games, active profiles, and mod counts.

Examples:
  lmm status
  lmm status --game skyrim-se`,
	RunE: runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	return withService(cmd, func(ctx context.Context, service *core.Service) error {
		return doStatus(service)
	})
}

func doStatus(service *core.Service) error {
	games := service.ListGames()

	if len(games) == 0 {
		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(statusJSONOutput{Games: []statusGameJSON{}}); err != nil {
				return fmt.Errorf("encoding json: %w", err)
			}
			return nil
		}
		fmt.Println("No games configured.")
		fmt.Println("\nUse 'lmm game add' to add a game.")
		return nil
	}

	// If a specific game is requested, show details for that game
	if gameID != "" {
		if jsonOutput {
			return showGameStatusJSON(service, gameID)
		}
		return showGameStatus(service, gameID)
	}

	if jsonOutput {
		return outputStatusJSON(service, games)
	}

	// Load config to check for default game
	cfg, _ := config.Load(service.ConfigDir())

	// Show summary of all games
	fmt.Println("Configured Games:")
	fmt.Println()

	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)

	if verbose {
		if _, err := fmt.Fprintln(w, "GAME\tID\tPATH\tLINK\tPROFILES\tMODS†"); err != nil {
			return fmt.Errorf("writing header: %w", err)
		}
		if _, err := fmt.Fprintln(w, "----\t--\t----\t----\t--------\t-----"); err != nil {
			return fmt.Errorf("writing separator: %w", err)
		}
	} else {
		if _, err := fmt.Fprintln(w, "GAME\tPATH\tMODS†\tPROFILES"); err != nil {
			return fmt.Errorf("writing header: %w", err)
		}
		if _, err := fmt.Fprintln(w, "----\t----\t-----\t--------"); err != nil {
			return fmt.Errorf("writing separator: %w", err)
		}
	}

	pm := service.NewProfileManager()

	var totalMods int
	for _, game := range games {
		profiles, _ := pm.List(game.ID)

		// Get mod count from active profile
		var modCount int
		if defaultProfile, err := pm.GetDefault(game.ID); err == nil {
			mods, _ := service.GetInstalledMods(game.ID, defaultProfile.Name)
			modCount = len(mods)
			totalMods += modCount
		}

		// Mark default game
		gameName := game.Name
		if cfg != nil && cfg.DefaultGame == game.ID {
			gameName += " (default)"
		}

		if verbose {
			linkMethod := service.GetGameLinkMethod(game)
			linkStr := linkMethod.String()
			if game.LinkMethodExplicit {
				linkStr += "*" // Indicate per-game override
			}
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%d\n",
				gameName,
				game.ID,
				truncate(game.InstallPath, 30),
				linkStr,
				len(profiles),
				modCount,
			); err != nil {
				return fmt.Errorf("writing row: %w", err)
			}
		} else {
			if _, err := fmt.Fprintf(w, "%s\t%s\t%d\t%d\n",
				gameName,
				truncate(game.InstallPath, 40),
				modCount,
				len(profiles),
			); err != nil {
				return fmt.Errorf("writing row: %w", err)
			}
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("flushing output: %w", err)
	}
	if err := printTable(&buf, 2, nil); err != nil {
		return fmt.Errorf("writing table: %w", err)
	}

	fmt.Println()
	if verbose {
		fmt.Println("* = per-game override")
	}
	fmt.Println("† = mods in active profile")

	fmt.Printf("\nTotal: %d game(s), %d mod(s) installed\n", len(games), totalMods)

	return nil
}

type statusJSONOutput struct {
	Games []statusGameJSON `json:"games"`
}

type statusGameJSON struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	InstallPath string   `json:"install_path"`
	ModPath     string   `json:"mod_path"`
	LinkMethod  string   `json:"link_method"`
	Profiles    []string `json:"profiles"`
	ModCount    int      `json:"mod_count"`
	IsDefault   bool     `json:"is_default,omitempty"`
}

func outputStatusJSON(service *core.Service, games []*domain.Game) error {
	cfg, _ := config.Load(service.ConfigDir())
	pm := service.NewProfileManager()

	out := statusJSONOutput{Games: make([]statusGameJSON, 0, len(games))}
	for _, game := range games {
		profiles, _ := pm.List(game.ID)
		profileNames := make([]string, len(profiles))
		for i, p := range profiles {
			profileNames[i] = p.Name
		}
		var modCount int
		if defaultProfile, err := pm.GetDefault(game.ID); err == nil {
			mods, _ := service.GetInstalledMods(game.ID, defaultProfile.Name)
			modCount = len(mods)
		}
		linkMethod := service.GetGameLinkMethod(game)
		isDefault := cfg != nil && cfg.DefaultGame == game.ID
		out.Games = append(out.Games, statusGameJSON{
			ID:          game.ID,
			Name:        game.Name,
			InstallPath: game.InstallPath,
			ModPath:     game.ModPath,
			LinkMethod:  linkMethod.String(),
			Profiles:    profileNames,
			ModCount:    modCount,
			IsDefault:   isDefault,
		})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func showGameStatusJSON(service *core.Service, gameID string) error {
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}
	pm := service.NewProfileManager()
	profiles, _ := pm.List(gameID)
	profileList := make([]statusProfileJSON, len(profiles))
	for i, p := range profiles {
		profileList[i] = statusProfileJSON{Name: p.Name, ModCount: len(p.Mods), IsDefault: p.IsDefault}
	}
	linkMethod := service.GetGameLinkMethod(game)
	cachePath := service.GetGameCachePath(game)
	out := statusGameDetailJSON{
		ID:                  game.ID,
		Name:                game.Name,
		InstallPath:         game.InstallPath,
		ModPath:             game.ModPath,
		LinkMethod:          linkMethod.String(),
		EffectiveLinkMethod: linkMethod.String(),
		LinkMethodSource:    "global",
		Profiles:            profileList,
		CachePath:           cachePath,
	}
	if game.LinkMethodExplicit {
		out.LinkMethodSource = "game"
	}
	if defaultProfile, err := pm.GetDefault(gameID); err == nil {
		// Mirror the text twin (showGameStatus): the effective method is the
		// active profile's resolution (profile > game > global, #155).
		method, err := service.GetEffectiveLinkMethod(game, defaultProfile.Name)
		if err != nil {
			return err
		}
		out.EffectiveLinkMethod = method.String()
		if defaultProfile.LinkMethodExplicit {
			out.LinkMethodSource = "profile"
		}
		mods, _ := service.GetInstalledMods(gameID, defaultProfile.Name)
		out.ActiveProfile = defaultProfile.Name
		out.InstalledModCount = len(mods)
		var enabled int
		for _, m := range mods {
			if m.Enabled {
				enabled++
			}
		}
		out.EnabledModCount = enabled

		lastDeploy, err := service.GetLastDeployTime(gameID, defaultProfile.Name)
		if err != nil {
			return fmt.Errorf("status: last deploy time: %w", err)
		}
		out.LastDeploy = lastDeploy
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

type statusGameDetailJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	InstallPath string `json:"install_path"`
	ModPath     string `json:"mod_path"`
	// LinkMethod is the GAME-level resolution (game-explicit or global
	// default) - deliberately NOT the profile-effective method, for JSON
	// contract stability (#155): consumers written before per-profile
	// overrides existed read this key as the game's setting, so its meaning
	// stays put and the profile-aware fields below are additive instead.
	LinkMethod string `json:"link_method"`
	// EffectiveLinkMethod is what a deploy into the active profile actually
	// uses (profile > game > global) - the JSON twin of the text output's
	// Link Method line. LinkMethodSource says which level won: "profile",
	// "game", or "global". Both are always present; with no profile
	// override they equal LinkMethod and its level.
	EffectiveLinkMethod string              `json:"effective_link_method"`
	LinkMethodSource    string              `json:"link_method_source"`
	CachePath           string              `json:"cache_path"`
	Profiles            []statusProfileJSON `json:"profiles"`
	ActiveProfile       string              `json:"active_profile,omitempty"`
	InstalledModCount   int                 `json:"installed_mod_count,omitempty"`
	EnabledModCount     int                 `json:"enabled_mod_count,omitempty"`
	// LastDeploy is nil for a profile that has never been deployed. Kept
	// omitempty (task-4-brief.md / lmm-repo-conventions' JSON-contract-
	// additions-are-MINOR precedent): an unset field, not a null or zero
	// time, is what makes this an additive change existing consumers can
	// ignore entirely.
	LastDeploy *time.Time `json:"last_deploy,omitempty"`
}

type statusProfileJSON struct {
	Name      string `json:"name"`
	ModCount  int    `json:"mod_count"`
	IsDefault bool   `json:"is_default"`
}

func showGameStatus(service *core.Service, gameID string) error {
	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	pm := service.NewProfileManager()

	fmt.Printf("Game: %s\n", game.Name)
	fmt.Printf("  ID: %s\n", game.ID)
	fmt.Printf("  Install Path: %s\n", game.InstallPath)
	fmt.Printf("  Mod Path: %s\n", game.ModPath)

	// Show the effective link method for the active profile (the game's
	// default profile - the one deploys target): per-profile > per-game >
	// global default (#81).
	activeProfile, activeErr := pm.GetDefault(gameID)
	switch {
	case activeErr == nil && activeProfile.LinkMethodExplicit:
		fmt.Printf("  Link Method: %s (per-profile)\n", activeProfile.LinkMethod)
	case game.LinkMethodExplicit:
		fmt.Printf("  Link Method: %s (per-game)\n", service.GetGameLinkMethod(game))
	case verbose:
		fmt.Printf("  Link Method: %s (global default)\n", service.GetGameLinkMethod(game))
	}

	// Show cache path
	cachePath := service.GetGameCachePath(game)
	if game.CachePath != "" {
		fmt.Printf("  Cache Path: %s (per-game)\n", game.CachePath)
	} else if verbose {
		fmt.Printf("  Cache Path: %s (global default)\n", cachePath)
	}

	// Show source mappings in verbose mode
	if verbose && len(game.SourceIDs) > 0 {
		fmt.Println("  Sources:")
		for source, sourceGameID := range game.SourceIDs {
			fmt.Printf("    %s: %s\n", source, sourceGameID)
		}
	}

	fmt.Println()

	// Get profiles
	profiles, err := pm.List(gameID)
	if err != nil {
		return fmt.Errorf("listing profiles: %w", err)
	}

	if len(profiles) == 0 {
		fmt.Println("No profiles configured.")
		return nil
	}

	fmt.Println("Profiles:")
	for _, p := range profiles {
		defaultMark := ""
		if p.IsDefault {
			defaultMark = " (active)"
		}
		fmt.Printf("  - %s%s: %d mod(s)\n", p.Name, defaultMark, len(p.Mods))
	}

	// Show installed mods count for active profile
	defaultProfile, err := pm.GetDefault(gameID)
	if err == nil {
		mods, _ := service.GetInstalledMods(gameID, defaultProfile.Name)
		fmt.Printf("\nActive Profile: %s\n", defaultProfile.Name)
		fmt.Printf("  Installed Mods: %d\n", len(mods))

		// Count enabled vs disabled
		var enabled, disabled int
		for _, m := range mods {
			if m.Enabled {
				enabled++
			} else {
				disabled++
			}
		}
		if len(mods) > 0 {
			// Disabled is a routine, expected state (not an error), so it's
			// dimmed rather than red - accent, not alarm.
			fmt.Printf("  Enabled: %s, Disabled: %s\n", colorGreen(strconv.Itoa(enabled)), colorDim(strconv.Itoa(disabled)))
		}

		lastDeploy, err := service.GetLastDeployTime(gameID, defaultProfile.Name)
		if err != nil {
			return fmt.Errorf("status: last deploy time: %w", err)
		}
		fmt.Printf("  Last Deploy: %s\n", formatLastDeploy(lastDeploy))
	}

	return nil
}

// formatLastDeploy renders a game's last-deploy timestamp for the CLI's
// plain-text status output: nil (never deployed) is "never"; otherwise an
// absolute, local "YYYY-MM-DD HH:MM" timestamp. Deliberately NOT the TUI's
// relative-age rendering (lastDeployLabel, internal/tui/app.go) - the CLI's
// output is scriptable/parseable, so a stable absolute format that doesn't
// change between two invocations a second apart is preferable to a coarse
// "3m ago" that would.
func formatLastDeploy(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Local().Format("2006-01-02 15:04")
}
