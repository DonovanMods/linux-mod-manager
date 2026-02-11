package main

import (
	"context"
	"fmt"
	"os"

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

When --source and --source-id are provided together, the mod is re-linked
to the specified source. If the source is available, metadata is fetched
automatically.

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
	modEditCmd.Flags().StringVarP(&editProfile, "profile", "p", "", "profile (default: default)")

	modCmd.AddCommand(modEditCmd)
}

func runModEdit(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	currentID := args[0]
	profileName := profileOrDefault(editProfile)

	service, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: closing service: %v\n", err)
		}
	}()

	// Find the mod - search all sources
	var installedMod *domain.InstalledMod
	allMods, err := service.GetInstalledMods(gameID, profileName)
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
			game, err := service.GetGame(gameID)
			if err != nil {
				return fmt.Errorf("getting game: %w", err)
			}

			cfGameID, ok := game.SourceIDs[newSourceID]
			if !ok {
				return fmt.Errorf("source %q is not configured for %s", newSourceID, game.Name)
			}

			ctx := context.Background()
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
		if err := service.DB().DeleteInstalledMod(oldSourceID, oldModID, gameID, profileName); err != nil {
			return fmt.Errorf("removing old record: %w", err)
		}

		// Update profile reference
		pm := getProfileManager(service)
		_ = pm.RemoveMod(gameID, profileName, oldSourceID, oldModID)
		modRef := domain.ModReference{
			SourceID: newSourceID,
			ModID:    newModID,
			Version:  installedMod.Version,
		}
		_ = pm.UpsertMod(gameID, profileName, modRef)
	}

	if len(changes) == 0 {
		fmt.Println("No changes specified. Use --name, --version, --author, --source, or --source-id.")
		return nil
	}

	// Save updated mod
	if err := service.DB().SaveInstalledMod(installedMod); err != nil {
		return fmt.Errorf("saving changes: %w", err)
	}

	fmt.Printf("Updated %s:\n", installedMod.Name)
	for _, change := range changes {
		fmt.Printf("  %s\n", change)
	}

	return nil
}
