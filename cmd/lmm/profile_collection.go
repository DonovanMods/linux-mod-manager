package main

import (
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// printCollectionPlan prints what a collection import plan (#621) says
// beyond the ordinary import's buckets: what the collection is and who made
// it, which game it was matched to, and everything in it the user should
// see before anything is written - the features lmm lacks, the hooks, the
// overrides, the items to fetch by hand and the sources this machine lacks.
// Every string in it was sanitised by core (#594).
func printCollectionPlan(c *core.CollectionImport, game *domain.Game) {
	title := c.Name
	if c.Version > 0 {
		title = fmt.Sprintf("%s (version %d)", title, c.Version)
	}
	fmt.Printf("Collection: %s\n", title)
	if c.Author != "" {
		fmt.Printf("  by %s\n", c.Author)
	}
	if o := c.Origin; o != nil {
		from := o.URL
		if from == "" {
			from = strings.TrimSpace(o.SourceID + " " + o.Slug)
		}
		if o.Author != "" {
			from = strings.TrimSpace(from + ", curated by " + o.Author)
		}
		fmt.Printf("  from %s\n", from)
	}
	fmt.Printf("  for %s (%s)\n", game.Name, collectionMatchLine(c))
	if c.Description != "" {
		fmt.Printf("\n%s\n", indentLines(c.Description, "  "))
	}
	if c.InstallInstructions != "" {
		fmt.Printf("\nInstall instructions:\n%s\n", indentLines(c.InstallInstructions, "  "))
	}
	for _, w := range c.Warnings {
		fmt.Printf("\n⚠ %s\n", w)
	}
	if len(c.Unsupported) > 0 {
		fmt.Printf("\n✗ This collection needs features this version of lmm cannot import:\n")
		for _, f := range c.Unsupported {
			fmt.Printf("    - %s\n", f.Feature)
		}
	}
	var unavailable []core.CollectionImportItem
	for _, item := range c.Items {
		if item.Unavailable != "" {
			unavailable = append(unavailable, item)
		}
	}
	if len(unavailable) > 0 {
		fmt.Printf("\n⚠ %d mod(s) come from a source lmm does not have here:\n", len(unavailable))
		for _, item := range unavailable {
			fmt.Printf("    - %s: %s\n", collectionItemLabel(item), item.Unavailable)
		}
	}
	if len(c.External) > 0 {
		fmt.Printf("\n%d item(s) lmm does not fetch - get them by hand:\n", len(c.External))
		for _, e := range c.External {
			line := "    - " + e.Name
			if e.URL != "" {
				line += " <" + e.URL + ">"
			}
			fmt.Println(line)
			if e.Instructions != "" {
				fmt.Println(indentLines(e.Instructions, "        "))
			}
		}
	}
	if len(c.Overrides) > 0 {
		fmt.Printf("\nConfig overrides written into the game directory:\n")
		for _, path := range c.Overrides {
			fmt.Printf("    - %s\n", path)
		}
	}
	fmt.Println()
}

// printImportGates prints what an import's gates hold back (#621): every
// hook the document carries, and every override that is not a text config
// file, each with what will happen to it. For every document import,
// version 0 included. Hook commands and override paths reach here only
// after core refused any holding a control or formatting character.
func printImportGates(plan *core.ImportPlan) {
	if len(plan.Hooks) > 0 {
		fmt.Printf("\nHooks (scripts lmm would run):\n")
		for _, h := range plan.Hooks {
			command := h.Command
			if command == "" {
				command = "(disables the game's hook)"
			}
			fmt.Printf("    - %s: %s\n", h.Hook, command)
		}
		switch {
		case plan.Collection != nil && !plan.Collection.HooksImportable:
			fmt.Println("  They will not be imported: hooks are never imported from a URL.")
		case profileImportWithHooks:
			fmt.Println("  They will be imported (--with-hooks).")
		default:
			fmt.Println("  They will not be imported; re-run with --with-hooks to import them.")
		}
	}
	// Every override is listed, held back or not (#621 review N1): a
	// collection's plan lists them in its own section, so this is the
	// older format's list.
	if plan.Collection == nil && len(plan.Overrides) > 0 {
		fmt.Printf("\nConfig overrides written into the game directory:\n")
		for _, path := range plan.Overrides {
			fmt.Printf("    - %s\n", path)
		}
	}
	if len(plan.RestrictedOverrides) > 0 {
		fmt.Printf("\nOverrides held back (not text config files, or loader configuration):\n")
		for _, o := range plan.RestrictedOverrides {
			fmt.Printf("    - %s: %s\n", o.Path, o.Reason)
		}
		if profileImportAnyOverrides {
			fmt.Println("  They will be imported (--allow-any-overrides).")
		} else {
			fmt.Println("  They will be skipped; re-run with --allow-any-overrides to import them.")
		}
	}
}

// printImportGatesResult says, after the import, what its gates held
// back - plainly, and naming the flag that would include it.
func printImportGatesResult(plan *core.ImportPlan, result *core.ProfileImportResult) {
	if result == nil || result.ProfileName == "" {
		return
	}
	if n := result.HooksWithheld; n > 0 {
		if plan.Collection != nil && !plan.Collection.HooksImportable {
			fmt.Printf("\n%d hook(s) were not imported: hooks are never imported from a URL.\n", n)
		} else {
			fmt.Printf("\n%d hook(s) were not imported; re-import with --with-hooks to import them.\n", n)
		}
	}
	if skipped := result.OverridesSkipped; len(skipped) > 0 {
		fmt.Printf("\n%d override(s) were skipped (not text config files, or loader configuration); re-import with --allow-any-overrides to include them:\n", len(skipped))
		for _, o := range skipped {
			fmt.Printf("    - %s\n", o.Path)
		}
	}
}

// collectionMatchLine says how a collection was matched to the local game.
func collectionMatchLine(c *core.CollectionImport) string {
	switch {
	case c.MatchedBy == "game":
		return "--game " + c.GameID
	case c.MatchedBy == "steam_app_id":
		return "game " + c.GameID + ", matched by Steam app id " + c.Game.SteamAppID
	case strings.HasPrefix(c.MatchedBy, "source:"):
		src := strings.TrimPrefix(c.MatchedBy, "source:")
		return "game " + c.GameID + ", matched by its " + src + " id " + c.Game.Sources[src]
	}
	return "game " + c.GameID
}

// collectionItemLabel names a collection item: its display name, else its
// source and id.
func collectionItemLabel(item core.CollectionImportItem) string {
	id := item.SourceID + ":" + item.ModID
	if item.Name != "" {
		return item.Name + " (" + id + ")"
	}
	return id
}

// indentLines prefixes every line of text.
func indentLines(text, prefix string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}
