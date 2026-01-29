package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var conflictsProfile string

var conflictsCmd = &cobra.Command{
	Use:   "conflicts",
	Short: "Show all file conflicts in the current profile",
	Long: `Display all file conflicts in the current profile.

A conflict occurs when multiple mods want to deploy the same file path.
The mod listed as "owner" is the one whose file is currently deployed.

Note: File tracking requires mods to be installed/deployed with lmm version 0.9.0+.
Older mods may need to be redeployed to track their files.

Examples:
  lmm conflicts --game skyrim-se
  lmm conflicts --game skyrim-se --profile survival`,
	RunE: runConflicts,
}

func init() {
	conflictsCmd.Flags().StringVarP(&conflictsProfile, "profile", "p", "", "profile (default: default)")

	rootCmd.AddCommand(conflictsCmd)
}

func runConflicts(cmd *cobra.Command, args []string) error {
	if err := requireGame(cmd); err != nil {
		return err
	}

	profileName := profileOrDefault(conflictsProfile)

	svc, err := initService()
	if err != nil {
		return fmt.Errorf("initializing service: %w", err)
	}
	defer func() { _ = svc.Close() }()

	// Get all installed mods
	mods, err := svc.GetInstalledMods(gameID, profileName)
	if err != nil {
		return fmt.Errorf("getting installed mods: %w", err)
	}

	if len(mods) == 0 {
		fmt.Println("No installed mods.")
		return nil
	}

	// Build map of mod ID to name for display
	modNames := make(map[string]string)
	for _, m := range mods {
		key := m.SourceID + ":" + m.ID
		modNames[key] = m.Name
	}

	// Collect all file paths and which mods want them
	// Map: relative_path -> list of mod keys that have this file
	fileToMods := make(map[string][]string)

	for _, m := range mods {
		files, err := svc.DB().GetDeployedFilesForMod(gameID, profileName, m.SourceID, m.ID)
		if err != nil {
			continue
		}
		key := m.SourceID + ":" + m.ID
		for _, f := range files {
			fileToMods[f] = append(fileToMods[f], key)
		}
	}

	// Find files with multiple mods (conflicts)
	type conflictInfo struct {
		path     string
		ownerKey string
		others   []string
	}

	var conflicts []conflictInfo
	for path, keys := range fileToMods {
		if len(keys) > 1 {
			// Get current owner from database
			owner, err := svc.DB().GetFileOwner(gameID, profileName, path)
			if err != nil || owner == nil {
				continue
			}
			ownerKey := owner.SourceID + ":" + owner.ModID

			// Other mods that wanted this file
			var others []string
			for _, k := range keys {
				if k != ownerKey {
					others = append(others, k)
				}
			}

			if len(others) > 0 {
				conflicts = append(conflicts, conflictInfo{
					path:     path,
					ownerKey: ownerKey,
					others:   others,
				})
			}
		}
	}

	if len(conflicts) == 0 {
		fmt.Println("No conflicts found.")
		return nil
	}

	fmt.Printf("Found %d conflicting file(s):\n\n", len(conflicts))

	for _, c := range conflicts {
		ownerName := modNames[c.ownerKey]
		if ownerName == "" {
			ownerName = c.ownerKey
		}

		fmt.Printf("  %s\n", c.path)
		fmt.Printf("    Owner: %s\n", ownerName)
		fmt.Printf("    Also in: ")
		for i, k := range c.others {
			name := modNames[k]
			if name == "" {
				name = k
			}
			if i > 0 {
				fmt.Print(", ")
			}
			fmt.Print(name)
		}
		fmt.Println()
		fmt.Println()
	}

	return nil
}
