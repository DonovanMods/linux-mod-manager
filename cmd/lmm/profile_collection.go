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
		if from != "" {
			fmt.Printf("  from %s\n", from)
		}
		// #622: the address it was fetched from, without credentials or
		// query (core redacted it).
		if o.ImportURL != "" {
			fmt.Printf("  fetched from %s\n", o.ImportURL)
		}
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
	if len(c.Requires) > 0 {
		fmt.Printf("  requires: %s\n", strings.Join(c.Requires, ", "))
	}
	printCollectionInstallerChoices(c)
	printCollectionPlugins(c.Plugins, game)
	printCollectionLoadOrders(c.LoadOrders, game)
	if c.Source != nil {
		printSourceCollection(c.Source)
	}
	if len(c.Unsupported) > 0 {
		fmt.Printf("\n✗ This collection needs features this version of lmm cannot import:\n")
		for _, f := range c.Unsupported {
			if len(f.Items) > 0 {
				fmt.Printf("    - %s (%s)\n", f.Feature, strings.Join(f.Items, ", "))
				continue
			}
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
	case c.MatchedBy == "game_id":
		return "game " + c.GameID + ", named by the export's game_id"
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

// printCollectionInstallerChoices lists the FOMOD installer choices a
// collection records (#615): they are replayed as written, and an installer
// that no longer accepts them - or an archive other than the one the
// collection recorded them against (#609) - is asked about when the mod
// installs.
func printCollectionInstallerChoices(c *core.CollectionImport) {
	var items []core.CollectionImportItem
	for _, item := range c.Items {
		if len(item.InstallerChoices) > 0 {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return
	}
	fmt.Printf("\nInstaller (FOMOD) choices the collection records - replayed as written; an installer that no longer accepts them, or an archive other than the one they were recorded against, asks:\n")
	for _, item := range items {
		fmt.Printf("    - %s\n", collectionItemLabel(item))
		for _, line := range item.InstallerChoices {
			fmt.Printf("        %s\n", line)
		}
	}
}

// sourceItemMarks is the line mark of each source-collection item state.
var sourceItemMarks = map[core.CollectionItemState]string{
	core.CollectionItemInstalled:       "✓",
	core.CollectionItemWillInstall:     "↓",
	core.CollectionItemManualDownload:  "⇣",
	core.CollectionItemExternal:        "↗",
	core.CollectionItemOptionalSkipped: "○",
	core.CollectionItemUnsupported:     "✗",
}

// sourceItemStates is the order the states are summarised in.
var sourceItemStates = []core.CollectionItemState{
	core.CollectionItemInstalled, core.CollectionItemWillInstall, core.CollectionItemManualDownload,
	core.CollectionItemExternal, core.CollectionItemOptionalSkipped, core.CollectionItemUnsupported,
}

// printSourceCollection prints a converted source collection (#607): the
// recipe's state, each item with what the import does with it, the
// curator's tools and the conversion's notes.
func printSourceCollection(c *core.ConvertedCollection) {
	if c.ManifestError != "" {
		fmt.Printf("\n✗ The collection's recipe could not be read, so lmm cannot tell how to install it:\n  %s\n", c.ManifestError)
	}
	var counts []string
	for _, st := range sourceItemStates {
		if n := c.Counts[st]; n > 0 {
			counts = append(counts, fmt.Sprintf("%d %s", n, strings.ReplaceAll(string(st), "_", " ")))
		}
	}
	fmt.Printf("\n%d item(s) at revision %d: %s\n", len(c.Items), c.Revision, strings.Join(counts, ", "))
	for _, it := range c.Items {
		line := fmt.Sprintf("  %s %s", sourceItemMarks[it.State], it.Name)
		if it.Version != "" {
			line += " " + it.Version
		}
		var tags []string
		if it.Optional {
			tags = append(tags, "optional")
		}
		if it.FileCategory != "" && it.FileCategory != "MAIN" {
			tags = append(tags, strings.ToLower(strings.ReplaceAll(it.FileCategory, "_", " "))+" file")
		}
		if it.ModType != "" {
			tags = append(tags, "type "+it.ModType)
		}
		if it.Installer {
			tags = append(tags, "installer choices")
		}
		if len(it.Features) > 0 {
			tags = append(tags, "needs "+strings.Join(it.Features, ", "))
		}
		if len(tags) > 0 {
			line += " (" + strings.Join(tags, "; ") + ")"
		}
		fmt.Println(line)
		if it.Note != "" {
			fmt.Printf("      %s\n", it.Note)
		}
		if (it.State == core.CollectionItemManualDownload || it.State == core.CollectionItemExternal) && it.URL != "" {
			fmt.Printf("      %s\n", it.URL)
		}
		if it.Instructions != "" {
			fmt.Println(indentLines(it.Instructions, "      "))
		}
	}
	if c.Counts[core.CollectionItemOptionalSkipped] > 0 && !c.WithOptional {
		fmt.Printf("  Optional items are left out; --with-optional installs them.\n")
	}
	if len(c.Tools) > 0 {
		fmt.Printf("\nThe curator's tool shortcuts (listed only; lmm never creates or runs them):\n")
		for _, t := range c.Tools {
			fmt.Printf("    - %s: %s\n", t.Name, t.Exe)
		}
	}
	for _, note := range c.Notes {
		fmt.Printf("  · %s\n", note)
	}
}

// printCollectionPlugins summarises the plugin list a collection import
// saves on the profile (#611): its size, the plugins it leaves inactive,
// its rules, and whether lmm applies it. The full list is the --json
// plan's profile.plugins.
func printCollectionPlugins(p *core.CollectionPluginSummary, game *domain.Game) {
	if p == nil {
		return
	}
	line := fmt.Sprintf("Plugin order: %d plugin(s), %d active", p.Plugins, p.Active)
	if p.Rules > 0 || len(p.Groups) > 0 {
		line += fmt.Sprintf("; %d plugin rule(s)", p.Rules)
		if len(p.Groups) > 0 {
			line += ", groups: " + strings.Join(p.Groups, ", ")
		}
	}
	fmt.Printf("\n%s\n", line)
	if len(p.Inactive) > 0 {
		fmt.Printf("  Inactive: %s\n", strings.Join(p.Inactive, ", "))
	}
	if p.Managed {
		fmt.Printf("  Saved on the profile and written to %s's plugin list at every deploy.\n", game.Name)
	} else {
		fmt.Printf("  Recorded on the profile only: lmm does not manage %s's plugin list.\n", game.Name)
	}
}
