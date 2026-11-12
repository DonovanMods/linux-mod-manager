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
	if len(c.Hooks) > 0 {
		fmt.Printf("\nHooks (scripts lmm would run):\n")
		for _, h := range c.Hooks {
			command := h.Command
			if command == "" {
				command = "(disables the game's hook)"
			}
			fmt.Printf("    - %s: %s\n", h.Hook, command)
		}
		switch {
		case !c.HooksImportable:
			fmt.Println("  They will not be imported: hooks are never imported from a URL.")
		case profileImportWithHooks:
			fmt.Println("  They will be imported (--with-hooks).")
		default:
			fmt.Println("  They will not be imported; re-run with --with-hooks to import them.")
		}
	}
	fmt.Println()
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
