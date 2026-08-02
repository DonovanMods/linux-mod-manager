package main

import (
	"context"
	"fmt"
	"os"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/spf13/cobra"
)

var (
	editName    string
	editVersion string
	editAuthor  string
	editSource  string
	editID      string
	editProfile string
)

var modEditCmd = &cobra.Command{
	Use:   "edit <current-id>",
	Short: "Edit mod details (name, version, author, source, ID)",
	Long: `Manually edit mod details after import.

Useful for:
- Fixing names/versions on locally imported mods
- Re-linking a local mod to its CurseForge or NexusMods ID
- Adding missing metadata

Providing --source and/or --source-id re-links the mod: whichever of the
two you omit keeps its current value. If the resulting source is
configured for this game and isn't "local", metadata (name, author,
version, summary, URL) is fetched from it automatically and applied to
any field you didn't explicitly override with --name/--author/--version.
Re-linking moves the mod to its new source:id in the database and
profile - it is not merely a display change.

A locked mod (see 'lmm mod lock') refuses --version (other than the
locked version itself) and re-linking: move the lock to the desired
version first, or unlock (re-linking always requires an unlock, since
it would replace the locked profile entry). Metadata-only edits
(--name/--author) are always allowed.

Examples:
  lmm mod edit abc123 --name "Better Mod Name" --version 1.2.3
  lmm mod edit abc123 --source curseforge --source-id 12345
  lmm mod edit abc123 --author "ModAuthor"`,
	Args: cobra.ExactArgs(1),
	RunE: runModEdit,
}

func init() {
	modEditCmd.Flags().StringVar(&editName, "name", "", "new mod name")
	modEditCmd.Flags().StringVar(&editVersion, "version", "", "new version")
	modEditCmd.Flags().StringVar(&editAuthor, "author", "", "new author")
	modEditCmd.Flags().StringVar(&editSource, "source", "", "new source (e.g. curseforge, nexusmods)")
	modEditCmd.Flags().StringVar(&editID, "source-id", "", "new source-specific mod ID")
	modEditCmd.Flags().StringVarP(&editProfile, "profile", "p", "", "profile (default: active profile)")

	modCmd.AddCommand(modEditCmd)
}

func runModEdit(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doModEdit(ctx, service, game, args[0])
	})
}

func doModEdit(ctx context.Context, service *core.Service, game *domain.Game, currentID string) error {
	profileName, err := resolveProfile(service, game.ID, editProfile)
	if err != nil {
		return err
	}

	// Find the mod - search all sources
	var installedMod *domain.InstalledMod
	allMods, err := service.GetInstalledMods(game.ID, profileName)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}
	for i := range allMods {
		if allMods[i].ID == currentID {
			installedMod = &allMods[i]
			break
		}
	}
	if installedMod == nil {
		return fmt.Errorf("mod %s not found in profile %s", currentID, profileName)
	}

	// #146: a LOCKED profile ref converges only via explicit lock/unlock, so
	// refuse any edit that would move its Version or re-link its identity
	// BEFORE any state moves. Without this gate, the re-link path dropped the
	// Locked marker (RemoveMod deletes the locked ref, then UpsertMod appends
	// a fresh ref with zero-value Locked), and the --version path wrote the
	// DB row first and only then hit UpsertMod's ErrModLocked guard - demoted
	// to a verbose-only warning, i.e. success output plus silent
	// DB-vs-profile divergence. Mirrors the install/update gates
	// (internal/core/flows.go LockedRefRefusalError / lockedInstallRefusal):
	// a --version equal to the locked version is a realign, not a move, and
	// stays allowed - the same allowance UpsertMod itself grants. A
	// missing/unreadable profile cannot hold a lock (the gates' tolerant
	// precedent). Metadata-only edits (--name/--author) touch neither
	// Version nor identity and pass through.
	relink := editSource != "" || editID != ""
	if relink || editVersion != "" {
		if prof, err := getProfileManager(service).Get(game.ID, profileName); err == nil {
			if ref := prof.FindRef(installedMod.SourceID, installedMod.ID); ref != nil && ref.Locked {
				if relink {
					return fmt.Errorf("%w: %s is locked at v%s in profile %s - re-linking would replace the locked ref; unlock with 'lmm mod unlock -s %s -p %s %s' first",
						core.ErrModLocked, installedMod.Name, ref.Version, profileName,
						installedMod.SourceID, profileName, installedMod.ID)
				}
				if editVersion != ref.Version {
					return core.LockedRefRefusalError(installedMod.Mod, profileName, ref)
				}
			}
		}
	}

	// Track what changed
	var changes []string

	if editName != "" {
		installedMod.Name = editName
		changes = append(changes, fmt.Sprintf("name -> %s", editName))
	}
	if editVersion != "" {
		installedMod.Version = editVersion
		changes = append(changes, fmt.Sprintf("version -> %s", editVersion))
	}
	if editAuthor != "" {
		installedMod.Author = editAuthor
		changes = append(changes, fmt.Sprintf("author -> %s", editAuthor))
	}

	// Handle re-linking to a new source
	newSourceID := editSource
	newModID := editID

	if newSourceID != "" || newModID != "" {
		if newSourceID == "" {
			newSourceID = installedMod.SourceID
		}
		if newModID == "" {
			newModID = installedMod.ID
		}

		// If re-linking to a non-local source, try to fetch metadata
		if newSourceID != domain.SourceLocal {
			cfGameID, ok := game.SourceIDs[newSourceID]
			if !ok {
				return fmt.Errorf("source %q is not configured for %s", newSourceID, game.Name)
			}

			fmt.Printf("Fetching metadata from %s...\n", newSourceID)
			mod, err := service.GetMod(ctx, newSourceID, cfGameID, newModID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not fetch metadata: %v\n", err)
			} else {
				// Apply fetched metadata (only if not explicitly overridden)
				if editName == "" {
					installedMod.Name = mod.Name
					changes = append(changes, fmt.Sprintf("name -> %s (from %s)", mod.Name, newSourceID))
				}
				if editAuthor == "" && mod.Author != "" {
					installedMod.Author = mod.Author
					changes = append(changes, fmt.Sprintf("author -> %s (from %s)", mod.Author, newSourceID))
				}
				if editVersion == "" && mod.Version != "" {
					installedMod.Version = mod.Version
					changes = append(changes, fmt.Sprintf("version -> %s (from %s)", mod.Version, newSourceID))
				}
				installedMod.Summary = mod.Summary
				installedMod.SourceURL = mod.SourceURL
				installedMod.PictureURL = mod.PictureURL
				installedMod.ManualDownload = false // Now linked, updates may work
			}
		}

		// Re-linking requires deleting old record and creating new one
		oldSourceID := installedMod.SourceID
		oldModID := installedMod.ID

		installedMod.SourceID = newSourceID
		installedMod.ID = newModID

		changes = append(changes, fmt.Sprintf("source -> %s (was %s)", newSourceID, oldSourceID))
		if newModID != oldModID {
			changes = append(changes, fmt.Sprintf("id -> %s (was %s)", newModID, oldModID))
		}

		// Delete old record
		if err := service.DeleteInstalledMod(oldSourceID, oldModID, game.ID, profileName); err != nil {
			return fmt.Errorf("removing old record: %w", err)
		}

		// Update profile reference
		pm := getProfileManager(service)
		if err := pm.RemoveMod(game.ID, profileName, oldSourceID, oldModID); err != nil {
			if verbose {
				fmt.Printf("Warning: could not remove old profile entry: %v\n", err)
			}
		}
		modRef := domain.ModReference{
			SourceID: newSourceID,
			ModID:    newModID,
			Version:  installedMod.Version,
		}
		if err := pm.UpsertMod(game.ID, profileName, modRef); err != nil {
			if verbose {
				fmt.Printf("Warning: could not update profile: %v\n", err)
			}
		}
	}

	if len(changes) == 0 {
		fmt.Println("No changes specified. Use --name, --version, --author, --source, or --source-id.")
		return nil
	}

	// Save updated mod
	if err := service.SaveInstalledMod(installedMod); err != nil {
		return fmt.Errorf("saving changes: %w", err)
	}

	// Update profile version if changed (and not already handled by re-link)
	if editVersion != "" && newSourceID == "" && newModID == "" {
		pm := getProfileManager(service)
		modRef := domain.ModReference{
			SourceID: installedMod.SourceID,
			ModID:    installedMod.ID,
			Version:  installedMod.Version,
		}
		if err := pm.UpsertMod(game.ID, profileName, modRef); err != nil {
			if verbose {
				fmt.Printf("Warning: could not update profile version: %v\n", err)
			}
		}
	}

	// #197 postsmoke seam-audit fix: a --version edit is a direct
	// regeneration trigger; a --source/--source-id relink changes the
	// identity enabledExmodzSources keys off (mod.SourceID + ":" +
	// mod.ID). Sync unconditionally now that changes is non-empty - cheap
	// no-op if nothing merge-relevant actually moved.
	if syncWarnings, syncErr := service.SyncMergedPak(ctx, game, profileName); syncErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not sync merged pak: %v\n", syncErr)
	} else {
		for _, w := range syncWarnings {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
		}
	}

	fmt.Printf("Updated %s:\n", installedMod.Name)
	for _, change := range changes {
		fmt.Printf("  %s\n", change)
	}

	return nil
}
