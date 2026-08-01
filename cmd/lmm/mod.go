package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/spf13/cobra"
)

var (
	modSource    string
	modProfile   string
	modSetAuto   bool
	modSetNotify bool
	modSetPin    bool
)

var modCmd = &cobra.Command{
	Use:   "mod",
	Short: "Manage mod settings",
	Long:  `Commands for managing individual mod settings.`,
}

var modSetUpdateCmd = &cobra.Command{
	Use:   "set-update <mod-id>",
	Short: "Set update policy for a mod",
	Long: `Set the update policy for an installed mod.

Policies:
  --auto    Automatically apply updates when checking
  --notify  Show available updates, require manual approval (default)
  --pin     Mute update checks for this mod (to hold an exact version, use 'lmm mod lock')

Examples:
  lmm mod set-update 12345 --game skyrim-se --auto
  lmm mod set-update 12345 --game skyrim-se --pin
  lmm mod set-update 12345 --game skyrim-se --notify`,
	Args: cobra.ExactArgs(1),
	RunE: runModSetUpdate,
}

var modLockCmd = &cobra.Command{
	Use:   "lock <mod-id> [version]",
	Short: "Lock a mod at its current or a specific version",
	Long: `Lock an installed mod so 'lmm update' refuses to change it.

Locking is a metadata write, not a deploy: if the locked version differs
from what is currently installed, run 'lmm profile apply' (or 'lmm deploy')
afterward to converge the game directory.

With no version argument, locks the mod at its currently recorded version.
With a version argument, the version is resolved and validated against the
source before the lock is written.

Requires a source that can resolve versions. A version-less source cannot
be locked to a specific version this way - run 'lmm mod set-update' with
--pin to mute its update checks instead.

Examples:
  lmm mod lock 12345 --game skyrim-se
  lmm mod lock 12345 1.2.3 --game skyrim-se`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runModLock,
}

var modUnlockCmd = &cobra.Command{
	Use:   "unlock <mod-id>",
	Short: "Unlock a mod, restoring normal update behavior",
	Long: `Clear a mod's lock marker.

The mod's recorded version is left untouched - only the lock itself is
cleared. 'lmm update' will consider the mod again, subject to its update
policy (see 'lmm mod set-update').

Examples:
  lmm mod unlock 12345 --game skyrim-se`,
	Args: cobra.ExactArgs(1),
	RunE: runModUnlock,
}

var modEnableCmd = &cobra.Command{
	Use:   "enable <mod-id>",
	Short: "Enable a disabled mod",
	Long: `Enable a mod that was previously disabled.

This deploys the mod files from the cache to the game directory.
The mod must already be installed and in the cache.

Examples:
  lmm mod enable 12345 --game skyrim-se
  lmm mod enable 12345 --game skyrim-se --profile survival`,
	Args: cobra.ExactArgs(1),
	RunE: runModEnable,
}

var modDisableCmd = &cobra.Command{
	Use:   "disable <mod-id>",
	Short: "Disable a mod without uninstalling",
	Long: `Disable a mod by removing its files from the game directory.

The mod remains installed and cached, so it can be re-enabled later
without downloading again.

Examples:
  lmm mod disable 12345 --game skyrim-se
  lmm mod disable 12345 --game skyrim-se --profile survival`,
	Args: cobra.ExactArgs(1),
	RunE: runModDisable,
}

var modFilesCmd = &cobra.Command{
	Use:   "files <mod-id>",
	Short: "List files deployed by a mod",
	Long: `Show all files that a mod has deployed to the game directory.

This helps identify which files a mod owns, useful for debugging
conflicts or understanding mod contents.

Examples:
  lmm mod files 12345 --game skyrim-se
  lmm mod files 12345 --game skyrim-se --profile survival`,
	Args: cobra.ExactArgs(1),
	RunE: runModFiles,
}

var modShowCmd = &cobra.Command{
	Use:   "show <mod-id>",
	Short: "Show mod details",
	Long: `Fetch and display mod details from the source (description, summary, image URL).

Does not require the mod to be installed. Use --json for scriptable output.

Examples:
  lmm mod show 12345 --game skyrim-se
  lmm mod show 12345 --game skyrim-se --json`,
	Args: cobra.ExactArgs(1),
	RunE: runModShow,
}

func init() {
	modCmd.PersistentFlags().StringVarP(&modSource, "source", "s", "", "mod source (default: the sole configured source; prompts when several are configured)")
	modCmd.PersistentFlags().StringVarP(&modProfile, "profile", "p", "", "profile (default: active profile)")

	modSetUpdateCmd.Flags().BoolVar(&modSetAuto, "auto", false, "enable auto-update")
	modSetUpdateCmd.Flags().BoolVar(&modSetNotify, "notify", false, "notify only (default)")
	modSetUpdateCmd.Flags().BoolVar(&modSetPin, "pin", false, "mute update checks for this mod (to hold an exact version, use 'lmm mod lock')")

	modCmd.AddCommand(modSetUpdateCmd)
	modCmd.AddCommand(modLockCmd)
	modCmd.AddCommand(modUnlockCmd)
	modCmd.AddCommand(modEnableCmd)
	modCmd.AddCommand(modDisableCmd)
	modCmd.AddCommand(modFilesCmd)
	modCmd.AddCommand(modShowCmd)
	rootCmd.AddCommand(modCmd)
}

func runModSetUpdate(cmd *cobra.Command, args []string) error {
	// Validate flags before initialising the service so bad CLI usage fails fast.
	if err := validateModSetUpdatePolicyFlags(); err != nil {
		return err
	}
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doModSetUpdate(service, game, args[0])
	})
}

func validateModSetUpdatePolicyFlags() error {
	policyCount := 0
	if modSetAuto {
		policyCount++
	}
	if modSetNotify {
		policyCount++
	}
	if modSetPin {
		policyCount++
	}
	if policyCount == 0 {
		return fmt.Errorf("specify a policy: --auto, --notify, or --pin")
	}
	if policyCount > 1 {
		return fmt.Errorf("specify only one policy: --auto, --notify, or --pin")
	}
	return nil
}

func doModSetUpdate(service *core.Service, game *domain.Game, modID string) error {
	var err error
	modSource, err = resolveSource(service, game, modSource, false)
	if err != nil {
		return err
	}

	profileName, err := resolveProfile(service, game.ID, modProfile)
	if err != nil {
		return err
	}

	// Get the mod to verify it exists and get its name
	mod, err := service.GetInstalledMod(modSource, modID, game.ID, profileName)
	if err != nil {
		return fmt.Errorf("mod not found: %s", modID)
	}

	// Determine the new policy
	var policy domain.UpdatePolicy
	var policyStr string
	switch {
	case modSetAuto:
		policy = domain.UpdateAuto
		policyStr = "auto"
	case modSetPin:
		policy = domain.UpdatePinned
		policyStr = "pinned"
	default:
		policy = domain.UpdateNotify
		policyStr = "notify"
	}

	// Update the policy
	if err := service.SetModUpdatePolicy(modSource, modID, game.ID, profileName, policy); err != nil {
		return fmt.Errorf("failed to update policy: %w", err)
	}

	fmt.Printf("%s %s update policy: %s", colorGreen("✓"), mod.Name, policyStr)
	if modSetPin {
		fmt.Printf(" (v%s)", mod.Version)
	}
	fmt.Println()

	return nil
}

func runModLock(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		version := ""
		if len(args) > 1 {
			version = args[1]
		}
		return doModLock(ctx, service, game, args[0], version)
	})
}

// doModLock locks modID's profile ref at version (or, when version is
// empty, at whatever version is currently recorded). Task 2's sequencing
// constraint: SetModLock persists any version string verbatim with no
// upstream validation of its own, so a version argument MUST be resolved
// against the source via ResolveModVersion (#96) before SetModLock is ever
// called - ResolveModVersion's own errors (ErrVersionNotFound listing
// available versions, ErrNotSupported for a dynamic version-less source) are
// surfaced verbatim.
func doModLock(ctx context.Context, service *core.Service, game *domain.Game, modID, version string) error {
	var err error
	modSource, err = resolveSource(service, game, modSource, false)
	if err != nil {
		return err
	}

	profileName, err := resolveProfile(service, game.ID, modProfile)
	if err != nil {
		return err
	}

	mod, err := service.GetInstalledMod(modSource, modID, game.ID, profileName)
	if err != nil {
		return fmt.Errorf("mod not found: %s", modID)
	}

	// Static capability gate: a source that cannot carry per-file version
	// info has nothing for ResolveModVersion to resolve against, and
	// SetModLock's own "any string goes" persistence makes that a silent
	// footgun rather than a clean rejection - so this is checked before any
	// upstream call.
	caps, err := service.SourceCapabilities(modSource)
	if err != nil {
		return err
	}
	if !caps.Versions {
		// #142 round 5: set-update is profile-scoped too (SetModUpdatePolicy
		// takes profileName, same as SetModLock) - name -s/-p here, same as
		// every other lock/unlock/set-update remedy, so a copy-paste can't
		// land on the wrong profile/an ambiguous source.
		return fmt.Errorf("source %q cannot resolve versions; to freeze this mod use 'lmm mod set-update -s %s -p %s %s --pin'", modSource, modSource, profileName, modID)
	}

	pm := service.NewProfileManager()

	// target is the version displayed and written to the lock: the
	// resolved version argument, or - when none was given - whatever the
	// profile ref is currently recorded at (SetModLock leaves Version
	// untouched in that case).
	target := version
	if version != "" {
		// sourceMappedMod (verify.go): mod.Mod's GameID is the LMM game ID
		// (installed rows persist normalized IDs), but Service.GetModFiles -
		// which ResolveModVersion calls into - forwards straight to the
		// source with no game-ID translation, unlike Service.GetMod. Sources
		// like NexusMods address games by their own domain, so an unmapped
		// GameID would silently query the wrong upstream game whenever this
		// game's per-source mapping differs from its LMM ID.
		if _, err := service.ResolveModVersion(ctx, modSource, sourceMappedMod(game, &mod.Mod), version); err != nil {
			return err
		}
	} else {
		profile, err := pm.Get(game.ID, profileName)
		if err != nil {
			return fmt.Errorf("loading profile: %w", err)
		}
		// FindRef (domain/profile.go): the canonical ref lookup this PR
		// itself introduced - target simply stays "" when the ref is
		// absent, same as before; SetModLock's own not-found error handles
		// that case below, so no new error path is added here.
		if ref := profile.FindRef(modSource, modID); ref != nil {
			target = ref.Version
		}
	}

	if err := pm.SetModLock(game.ID, profileName, modSource, modID, version); err != nil {
		return err
	}

	fmt.Printf("%s %s locked at v%s\n", colorGreen("✓"), mod.Name, target)
	// Locking is a metadata write, not a deploy (design decision): when the
	// target differs from what is actually installed, the game directory
	// won't match the lock until convergence, so say so.
	if target != mod.Version {
		fmt.Printf("Installed version is v%s — run 'lmm profile apply' (or 'lmm deploy') to converge.\n", mod.Version)
	}

	return nil
}

func runModUnlock(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doModUnlock(service, game, args[0])
	})
}

// doModUnlock clears modID's lock marker. Version is left exactly as-is -
// core.ProfileManager.ClearModLock already guarantees this; unlocking is
// policy-neutral (#97 decision), so the update policy reported here is
// whatever it already was.
func doModUnlock(service *core.Service, game *domain.Game, modID string) error {
	var err error
	modSource, err = resolveSource(service, game, modSource, false)
	if err != nil {
		return err
	}

	profileName, err := resolveProfile(service, game.ID, modProfile)
	if err != nil {
		return err
	}

	mod, err := service.GetInstalledMod(modSource, modID, game.ID, profileName)
	if err != nil {
		return fmt.Errorf("mod not found: %s", modID)
	}

	pm := service.NewProfileManager()
	if err := pm.ClearModLock(game.ID, profileName, modSource, modID); err != nil {
		return err
	}

	fmt.Printf("%s %s unlocked (update policy: %s)\n", colorGreen("✓"), mod.Name, policyToString(mod.UpdatePolicy))
	return nil
}

func runModEnable(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doModEnable(ctx, service, game, args[0])
	})
}

func doModEnable(ctx context.Context, service *core.Service, game *domain.Game, modID string) error {
	// Resolve source: use flag if set, otherwise first configured source
	var err error
	modSource, err = resolveSource(service, game, modSource, false)
	if err != nil {
		return err
	}

	profileName, err := resolveProfile(service, game.ID, modProfile)
	if err != nil {
		return err
	}

	// Get the mod to verify it exists and get its name for display
	mod, err := service.GetInstalledMod(modSource, modID, game.ID, profileName)
	if err != nil {
		return fmt.Errorf("mod not found: %s", modID)
	}

	result, err := service.EnableMod(ctx, game, profileName, modSource, modID)
	if err != nil {
		// EnableMod's error-path convention returns any diagnostics
		// accumulated before the fatal error alongside it (mirrors
		// UninstallMod's own convention - see uninstall.go's
		// printUninstallDiagnostics); print them now, or they'd otherwise be
		// lost even though they already happened. result is nil on every
		// EnableMod error path today, but is guarded here anyway, for parity
		// with doModDisable below and because EnableResult.Notes is kept
		// specifically so a future EnableMod diagnostic wouldn't need
		// another signature change (see its doc comment in flows.go).
		if result != nil {
			printModNotes(result.Notes)
		}
		return err
	}
	printModNotes(result.Notes)

	if !result.Changed {
		fmt.Printf("%s is already enabled.\n", mod.Name)
		return nil
	}

	fmt.Printf("%s Enabled: %s\n", colorGreen("✓"), mod.Name)
	return nil
}

func runModDisable(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doModDisable(ctx, service, game, args[0])
	})
}

func doModDisable(ctx context.Context, service *core.Service, game *domain.Game, modID string) error {
	// Resolve source: use flag if set, otherwise first configured source
	var err error
	modSource, err = resolveSource(service, game, modSource, false)
	if err != nil {
		return err
	}

	profileName, err := resolveProfile(service, game.ID, modProfile)
	if err != nil {
		return err
	}

	// Get the mod to verify it exists and get its name for display
	mod, err := service.GetInstalledMod(modSource, modID, game.ID, profileName)
	if err != nil {
		return fmt.Errorf("mod not found: %s", modID)
	}

	result, err := service.DisableMod(ctx, game, profileName, modSource, modID)
	if err != nil {
		// DisableMod's error-path convention returns any diagnostics
		// accumulated before the fatal error alongside it (see DisableMod's
		// doc comment in flows.go, and mirrors UninstallMod's own convention
		// - see uninstall.go's printUninstallDiagnostics); print them now,
		// or they'd otherwise be lost even though they already happened
		// (e.g. an undeploy-failure Note recorded, then a later
		// SetModEnabled failure). result is nil only when DisableMod failed
		// before it could allocate the result struct.
		if result != nil {
			printModNotes(result.Notes)
		}
		return err
	}
	printModNotes(result.Notes)

	if !result.Changed {
		fmt.Printf("%s is already disabled.\n", mod.Name)
		return nil
	}

	fmt.Printf("%s Disabled: %s (files removed from game, kept in cache)\n", colorGreen("✓"), mod.Name)
	return nil
}

// printModNotes prints notes (EnableResult.Notes/DisableResult.Notes) to
// stdout, only under --verbose — the historical display contract
// UninstallResult's doc comment documents (each entry already carries its
// prefix word baked in, e.g. DisableResult's restored pre-5a "Warning:
// failed to undeploy some files: %v"). Safe to call with a nil/empty slice —
// but NOT with a nil *EnableResult/*DisableResult itself; callers on the
// error path (doModEnable/doModDisable above) must check that pointer for
// nil before passing its .Notes field, since it panics on a nil receiver
// otherwise (EnableMod/DisableMod both return a nil result on some error
// paths - see their own doc comments in flows.go).
func printModNotes(notes []string) {
	if !verbose {
		return
	}
	for _, n := range notes {
		fmt.Printf("  %s\n", n)
	}
}

func runModFiles(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, svc *core.Service, game *domain.Game) error {
		return doModFiles(svc, game, args[0])
	})
}

func doModFiles(svc *core.Service, game *domain.Game, modID string) error {
	profileName, err := resolveProfile(svc, game.ID, modProfile)
	if err != nil {
		return err
	}

	modSource, err = resolveSource(svc, game, modSource, false)
	if err != nil {
		return err
	}

	// Get mod info for display
	mod, err := svc.GetInstalledMod(modSource, modID, game.ID, profileName)
	if err != nil {
		return fmt.Errorf("mod not found: %s", modID)
	}

	// Get deployed files from database
	files, err := svc.GetDeployedFilesForMod(game.ID, profileName, modSource, modID)
	if err != nil {
		return fmt.Errorf("getting deployed files: %w", err)
	}

	fmt.Printf("Files deployed by %s (%s):\n\n", mod.Name, modID)

	if len(files) == 0 {
		fmt.Println("  No deployed files tracked.")
		fmt.Println("  (Files are tracked on install; existing mods may need to be redeployed)")
		return nil
	}

	for _, f := range files {
		fmt.Printf("  %s\n", f)
	}
	fmt.Printf("\nTotal: %d file(s)\n", len(files))

	return nil
}

func runModShow(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, svc *core.Service, game *domain.Game) error {
		return doModShow(ctx, svc, game, args[0])
	})
}

// modShowInstalled carries #97/#92's Installed section - closing #92's last
// unshipped row (mod show was policy-blind) - for both the JSON and
// human-readable code paths below, computed exactly once. Update policy
// (SQLite, per-install) and Lock (profile YAML ref, per-profile) are
// orthogonal (#97 design decision) and surfaced side by side.
type modShowInstalled struct {
	Version       string `json:"version"`
	Profile       string `json:"profile"`
	UpdatePolicy  string `json:"update_policy"`
	Locked        bool   `json:"locked"`
	LockedVersion string `json:"locked_version,omitempty"`
}

func doModShow(ctx context.Context, svc *core.Service, game *domain.Game, modID string) error {
	var err error
	modSource, err = resolveSource(svc, game, modSource, false)
	if err != nil {
		return err
	}

	mod, err := svc.GetMod(ctx, modSource, game.ID, modID)
	if err != nil {
		return fmt.Errorf("mod not found: %w", err)
	}

	// #92: mod show works for any mod on the source, installed or not, so a
	// resolveProfile failure is a real problem (mirrors every other mod
	// subcommand's error handling), but "not installed" is the ordinary
	// case - GetInstalledMod failing just means the section below is
	// omitted, not that the whole command errors.
	profileName, err := resolveProfile(svc, game.ID, modProfile)
	if err != nil {
		return err
	}
	var installedInfo *modShowInstalled
	if installed, instErr := svc.GetInstalledMod(modSource, modID, game.ID, profileName); instErr == nil {
		info := modShowInstalled{
			Version:      installed.Version,
			Profile:      profileName,
			UpdatePolicy: policyToString(installed.UpdatePolicy),
		}
		if prof, perr := config.LoadProfile(svc.ConfigDir(), game.ID, profileName); perr == nil {
			if ref := prof.FindRef(modSource, modID); ref != nil && ref.Locked {
				info.Locked = true
				info.LockedVersion = ref.Version
			}
		}
		installedInfo = &info
	}

	if jsonOutput {
		type modShowJSON struct {
			ID           string            `json:"id"`
			Name         string            `json:"name"`
			Version      string            `json:"version"`
			Author       string            `json:"author"`
			Summary      string            `json:"summary"`
			Description  string            `json:"description"`
			SourceURL    string            `json:"source_url,omitempty"`
			PictureURL   string            `json:"picture_url,omitempty"`
			Category     string            `json:"category"`
			Endorsements *int64            `json:"endorsements,omitempty"`
			Installed    *modShowInstalled `json:"installed,omitempty"`
		}
		out := modShowJSON{
			ID:           mod.ID,
			Name:         mod.Name,
			Version:      mod.Version,
			Author:       mod.Author,
			Summary:      mod.Summary,
			Description:  mod.Description,
			SourceURL:    mod.SourceURL,
			PictureURL:   mod.PictureURL,
			Category:     mod.Category,
			Endorsements: mod.Endorsements,
			Installed:    installedInfo,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	// Human-readable output
	fmt.Printf("%s\n", strings.Repeat("=", 60))
	fmt.Printf("%s\n", colorBold(mod.Name))
	fmt.Printf("%s\n", strings.Repeat("=", 60))
	fmt.Printf("ID: %s  Version: %s  Author: %s\n", mod.ID, mod.Version, mod.Author)
	if mod.Category != "" {
		fmt.Printf("Category: %s\n", mod.Category)
	}
	if mod.Endorsements != nil {
		fmt.Printf("Endorsements: %d\n", *mod.Endorsements)
	}
	if mod.SourceURL != "" {
		fmt.Printf("URL: %s\n", mod.SourceURL)
	}
	if mod.PictureURL != "" {
		fmt.Printf("Image: %s\n", mod.PictureURL)
	}
	fmt.Println()

	if mod.Summary != "" {
		fmt.Println("Summary:")
		fmt.Println(strings.TrimSpace(mod.Summary))
		fmt.Println()
	}

	if mod.Description != "" {
		fmt.Println("Description:")
		// Limit length for terminal; description can be long HTML
		desc := strings.TrimSpace(mod.Description)
		const maxDesc = 2000
		if len(desc) > maxDesc {
			desc = desc[:maxDesc] + "\n... (truncated; view on site for full description)"
		}
		fmt.Println(desc)
	}

	if installedInfo != nil {
		fmt.Println()
		fmt.Printf("Installed: v%s (profile: %s)\n", installedInfo.Version, installedInfo.Profile)
		policyDisplay := installedInfo.UpdatePolicy
		if policyDisplay == "pinned" {
			policyDisplay = colorYellow(policyDisplay)
		}
		fmt.Printf("  Update policy: %s\n", policyDisplay)
		if installedInfo.Locked {
			lockLine := "locked at v" + installedInfo.LockedVersion
			// Locking is a metadata write, not a deploy (same #97 design
			// decision doModLock's own convergence hint follows): only say
			// so when the lock's target actually differs from what's
			// installed.
			if installedInfo.LockedVersion != installedInfo.Version {
				lockLine += " — run 'lmm profile apply' to converge"
			}
			fmt.Printf("  Lock: %s\n", colorYellow(lockLine))
		} else {
			fmt.Println("  Lock: none")
		}
	}

	return nil
}
