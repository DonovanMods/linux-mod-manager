package main

// `lmm profile sync --collection` (#613): update a collection profile from
// its origin - the address it was imported from, or its NexusMods
// collection's newest revision. The flag, not the profile, picks this
// branch: a plain `lmm profile sync` keeps reconciling the profile with the
// installed mods, whatever the profile is.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

var (
	profileSyncCollection        bool
	profileSyncRemove            []string
	profileSyncRemoveObsolete    bool
	profileSyncOverride          []string
	profileSyncOverrideConflicts bool
	profileSyncWithOptional      bool
	profileSyncOptional          []string
	profileSyncAnyOverrides      bool
)

func init() {
	f := profileSyncCmd.Flags()
	f.BoolVar(&profileSyncCollection, "collection", false,
		"update a collection profile from its origin: the address it was imported from, or its NexusMods collection's newest revision")
	f.StringSliceVar(&profileSyncRemove, "remove", nil,
		"with --collection, remove this mod the new version drops (source:mod-id, or the mod id; repeatable)")
	f.BoolVar(&profileSyncRemoveObsolete, "remove-obsolete", false,
		"with --collection, remove every mod the new version drops (they are kept by default)")
	f.StringSliceVar(&profileSyncOverride, "override", nil,
		"with --collection, take the collection's side of this conflict: a mod you changed (source:mod-id, or the mod id), plugin:NAME, plugins:order, plugins:rules, load_order:ID or override:PATH, as the plan names it; repeatable")
	f.BoolVar(&profileSyncOverrideConflicts, "override-conflicts", false,
		"with --collection, take the collection's side of every conflict - mods, plugin list, load orders and overrides (yours is kept by default)")
	f.BoolVar(&profileSyncWithOptional, "with-optional", false,
		"with --collection, install a NexusMods collection's optional mods too")
	f.StringSliceVar(&profileSyncOptional, "optional", nil,
		"with --collection, install this optional item of a NexusMods collection (its id as the plan lists it, mod-id:file-id; repeatable)")
	f.BoolVar(&profileSyncAnyOverrides, "allow-any-overrides", false,
		"with --collection, import the new version's overrides that are not text config files, its bundled files that hold code, and its patches to code too")
	addFomodFlags(profileSyncCmd)
}

// collectionSyncFlagsUsed names the --collection-only flags given without
// it, for the refusal.
func collectionSyncFlagsUsed() []string {
	var used []string
	for name, set := range map[string]bool{
		"--remove": len(profileSyncRemove) > 0, "--remove-obsolete": profileSyncRemoveObsolete,
		"--override": len(profileSyncOverride) > 0, "--override-conflicts": profileSyncOverrideConflicts,
		"--with-optional": profileSyncWithOptional, "--optional": len(profileSyncOptional) > 0,
		"--allow-any-overrides": profileSyncAnyOverrides,
		"--fomod-choices":       fomodChoicesPath != "", "--fomod-defaults": fomodDefaults,
	} {
		if set {
			used = append(used, name)
		}
	}
	return used
}

// resolveUpdateKeys turns each --remove/--override argument into the key
// the plan offers: one of keys verbatim (an entry's source:mod-id, or for
// --override a plugin-list or override conflict's key), else a bare mod id
// when exactly one item has it. what names the bucket for errors.
func resolveUpdateKeys(args []string, items []core.CollectionUpdateItem, keys []string, what, flag string) ([]string, error) {
	var out []string
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if slices.Contains(keys, arg) {
			out = append(out, arg)
			continue
		}
		var hits []string
		for _, it := range items {
			if it.Key() == arg || (!strings.Contains(arg, ":") && it.ModID == arg) {
				hits = append(hits, it.Key())
			}
		}
		switch len(hits) {
		case 1:
			out = append(out, hits[0])
		case 0:
			return nil, fmt.Errorf("%s %s: no %s of this update is %q", flag, arg, what, arg)
		default:
			return nil, fmt.Errorf("%s %s: several %s have that id (%s); name one as source:mod-id", flag, arg, what, strings.Join(hits, ", "))
		}
	}
	return out, nil
}

// doCollectionUpdate is `lmm profile sync --collection [name]`.
func doCollectionUpdate(ctx context.Context, service *core.Service, game *domain.Game, args []string) error {
	profileName := profileSyncTarget(ctx, service, game, args)
	plan, err := service.PlanCollectionUpdate(ctx, game, core.CollectionUpdateRequest{
		Profile: profileName, WithOptional: profileSyncWithOptional, Optional: profileSyncOptional,
	})
	if err != nil {
		return err
	}
	opts := core.CollectionUpdateOptions{
		RemoveObsolete: profileSyncRemoveObsolete, OverrideConflicts: profileSyncOverrideConflicts,
		AllowAnyOverrides: profileSyncAnyOverrides,
	}
	if opts.Remove, err = resolveUpdateKeys(profileSyncRemove, plan.Removed, nil, "mod the new version drops", "--remove"); err != nil {
		return err
	}
	if opts.Override, err = resolveUpdateKeys(profileSyncOverride, plan.Conflicts, plan.ConflictKeys(), "conflict", "--override"); err != nil {
		return err
	}

	// Ruling 15: --dry-run --json is the plan document.
	if profileSyncDryRun && jsonOutput {
		return emitJSON(plan)
	}
	if !jsonOutput {
		printCollectionUpdatePlan(plan, game, opts)
	}
	if plan.UpToDate {
		if jsonOutput {
			return emitJSON(&core.CollectionUpdateResult{Profile: plan.Profile, UpToDate: true, Version: plan.Version, NewVersion: plan.NewVersion})
		}
		return nil
	}
	if len(plan.Unsupported) > 0 {
		// The plan always renders; the apply refuses, writing nothing.
		_, err := service.ApplyCollectionUpdate(ctx, game, plan, opts, nil)
		return err
	}
	if profileSyncDryRun {
		fmt.Println("Dry run: nothing was changed.")
		return nil
	}
	if !profileSyncYes {
		if !jsonOutput {
			fmt.Printf("Update profile %s to version %d? [Y/n]: ", plan.Profile, plan.NewVersion)
		}
		input, err := readPromptLine()
		if err != nil {
			return err
		}
		if input != "" && input != "y" && input != "yes" {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok || jsonOutput {
			return
		}
		switch p.Phase {
		case core.ImportInstalling:
			fmt.Println("\nDownloading and installing mods...")
		case core.ImportModInstalling:
			fmt.Printf("  Installing %s:%s...\n", p.SourceID, p.ModID)
		case core.ImportDownloading:
			printProgressLine("\r    Downloading: %.1f%%", p.Percent)
		case core.ImportDownloadDone:
			fmt.Println()
		case core.ImportModFailed:
			fmt.Printf("    Error: %s\n", p.Detail)
		case core.ImportModInstalled:
			fmt.Printf("    ✓ Installed: %s\n", p.ModName)
		case core.ImportNote:
			if verbose {
				fmt.Printf("    %s\n", p.Detail)
			}
		}
	}
	fa, err := newFomodAnswerer(service)
	if err != nil {
		return err
	}
	result, err := applyAnsweringFomod(fa, func(answers map[string]domain.FomodChoices) (*core.CollectionUpdateResult, error) {
		opts.Fomod = answers
		return service.ApplyCollectionUpdate(ctx, game, plan, opts, quietSink(progress))
	}, func(r *core.CollectionUpdateResult) *core.FomodChoicesRequiredError {
		if r == nil || r.Import == nil {
			return nil
		}
		return r.Import.FomodChoicesRequired
	})
	var incomplete *core.ProfileImportIncompleteError
	var incompleteErr error
	if errors.As(err, &incomplete) && result != nil && result.Import != nil && !result.Import.Refused {
		if jsonOutput {
			return err
		}
		incompleteErr, err = err, nil
	}
	if err != nil {
		return err
	}
	if jsonOutput {
		if err := emitJSON(result); err != nil {
			return err
		}
		if result.Import != nil {
			return fomodUnanswered(result.Import.FomodChoicesRequired)
		}
		return nil
	}
	if result.Import != nil && result.Import.FomodChoicesRequired != nil {
		return fomodUnanswered(result.Import.FomodChoicesRequired)
	}
	printCollectionUpdateResult(result)
	return incompleteErr
}

// updateItemLabel names an update entry: its display name and key.
func updateItemLabel(it core.CollectionUpdateItem) string {
	if it.Name != "" {
		return it.Name + " (" + it.Key() + ")"
	}
	return it.Key()
}

// versionMove is "1 → 2" for an entry, or the one side it has.
func versionMove(it core.CollectionUpdateItem) string {
	switch {
	case it.Version != "" && it.NewVersion != "" && it.Version != it.NewVersion:
		return it.Version + " → " + it.NewVersion
	case it.NewVersion != "":
		return it.NewVersion
	}
	return it.Version
}

// printCollectionUpdatePlan prints an update plan: where it came from, the
// versions, every entry by bucket with what will happen to it under the
// flags given, and the new document's gate. Every string was sanitised by
// core.
func printCollectionUpdatePlan(plan *core.CollectionUpdatePlan, game *domain.Game, opts core.CollectionUpdateOptions) {
	fmt.Printf("Collection: %s\n", plan.Name)
	switch plan.Origin {
	case core.CollectionFromURL:
		fmt.Printf("  from %s\n", plan.URL)
	default:
		fmt.Printf("  from %s collection %s\n", plan.SourceID, plan.Slug)
	}
	for _, w := range plan.Warnings {
		fmt.Printf("\n⚠ %s\n", w)
	}
	if plan.UpToDate {
		fmt.Printf("\nProfile %s is up to date with its collection (version %d).\n", plan.Profile, plan.Version)
		return
	}
	fmt.Printf("  version %d → %d, for profile %s of %s\n", plan.Version, plan.NewVersion, plan.Profile, game.Name)
	if plan.InstallInstructions != "" {
		fmt.Printf("\nInstall instructions:\n%s\n", indentLines(plan.InstallInstructions, "  "))
	}
	if plan.NoChanges {
		fmt.Println("\nThe new version changes none of the profile's mods; the update records it.")
	}
	if len(plan.Added) > 0 {
		fmt.Printf("\nWill add (%d):\n", len(plan.Added))
		for _, it := range plan.Added {
			fmt.Printf("  + %s %s\n", updateItemLabel(it), it.NewVersion)
		}
	}
	if len(plan.Changed) > 0 {
		fmt.Printf("\nWill update to the collection's new pin (%d):\n", len(plan.Changed))
		for _, it := range plan.Changed {
			fmt.Printf("  ~ %s %s (%s)\n", updateItemLabel(it), versionMove(it), strings.Join(it.Changes, ", "))
			if it.Note != "" {
				fmt.Printf("      %s\n", it.Note)
			}
		}
	}
	if len(plan.Removed) > 0 {
		remove := map[string]bool{}
		for _, key := range opts.Remove {
			remove[key] = true
		}
		fmt.Printf("\nNo longer in the collection (%d):\n", len(plan.Removed))
		for _, it := range plan.Removed {
			if opts.RemoveObsolete || remove[it.Key()] {
				fmt.Printf("  - %s: will be removed and uninstalled\n", updateItemLabel(it))
			} else {
				fmt.Printf("  · %s: kept, as your own mod\n", updateItemLabel(it))
			}
		}
		if !opts.RemoveObsolete && len(opts.Remove) < len(plan.Removed) {
			fmt.Println("  Remove one with --remove <mod>, or all with --remove-obsolete.")
		}
	}
	if len(plan.Conflicts) > 0 {
		override := map[string]bool{}
		for _, key := range opts.Override {
			override[key] = true
		}
		fmt.Printf("\nConflicts - mods you locked or edited that the collection changes (%d):\n", len(plan.Conflicts))
		for _, it := range plan.Conflicts {
			what := "yours is kept"
			if opts.OverrideConflicts || override[it.Key()] {
				what = "will take the collection's"
			}
			lock := ""
			if it.Locked {
				lock = ", locked"
			}
			fmt.Printf("  ! %s %s (%s%s): %s\n", updateItemLabel(it), versionMove(it), strings.Join(it.Changes, ", "), lock, what)
			if it.Note != "" {
				fmt.Printf("      %s\n", it.Note)
			}
		}
		if !opts.OverrideConflicts && len(opts.Override) < len(plan.Conflicts) {
			fmt.Println("  Take the collection's with --override <mod>, or all with --override-conflicts.")
		}
	}
	overridden := func(key string) bool { return opts.OverrideConflicts || slices.Contains(opts.Override, key) }
	side := func(key string) string {
		if overridden(key) {
			return "will take the collection's"
		}
		return "yours is kept"
	}
	if p := plan.Plugins; p != nil {
		fmt.Printf("\nPlugin order:\n")
		if len(p.Added) > 0 {
			fmt.Printf("  + added: %s\n", strings.Join(p.Added, ", "))
		}
		if len(p.Removed) > 0 {
			fmt.Printf("  - dropped: %s\n", strings.Join(p.Removed, ", "))
		}
		if p.Reordered {
			fmt.Println("  ~ reordered")
		}
		if p.Rules {
			fmt.Println("  ~ rules changed")
		}
		for _, name := range p.Conflicts {
			fmt.Printf("  ! %s: you and the collection both switched it on or off; %s (--override plugin:%s)\n", name, side("plugin:"+name), name)
		}
		if len(p.Moved) > 0 {
			fmt.Printf("  ! you and the collection both reordered %s; %s order is kept (--override plugins:order)\n",
				strings.Join(p.Moved, ", "), map[bool]string{true: "the collection's", false: "your"}[overridden("plugins:order")])
		}
		if p.RulesConflict {
			fmt.Printf("  ! you and the collection both changed the plugin rules; %s (--override plugins:rules)\n", side("plugins:rules"))
		}
	}
	for _, lo := range plan.LoadOrders {
		fmt.Printf("\nLoad order %s:\n", lo.ID)
		if len(lo.Added) > 0 {
			fmt.Printf("  + added: %s\n", strings.Join(lo.Added, ", "))
		}
		if len(lo.Removed) > 0 {
			fmt.Printf("  - dropped: %s\n", strings.Join(lo.Removed, ", "))
		}
		if lo.Reordered {
			fmt.Println("  ~ reordered")
		}
		if len(lo.Moved) > 0 {
			key := "load_order:" + lo.ID
			fmt.Printf("  ! you and the collection both reordered %s; %s order is kept (--override %s)\n",
				strings.Join(lo.Moved, ", "), map[bool]string{true: "the collection's", false: "your"}[overridden(key)], key)
		}
	}
	if len(plan.OverridesAdded)+len(plan.OverridesChanged)+len(plan.OverridesRemoved)+len(plan.OverrideConflicts) > 0 {
		fmt.Printf("\nConfig overrides in the game directory:\n")
		for _, path := range plan.OverridesAdded {
			fmt.Printf("  + %s\n", path)
		}
		for _, path := range plan.OverridesChanged {
			fmt.Printf("  ~ %s\n", path)
		}
		for _, path := range plan.OverridesRemoved {
			fmt.Printf("  - %s (no longer in the collection)\n", path)
		}
		for _, path := range plan.OverrideConflicts {
			fmt.Printf("  ! %s: you and the collection both changed it; %s (--override override:%s)\n", path, side("override:"+path), path)
		}
	}
	if len(plan.RestrictedOverrides) > 0 {
		fmt.Printf("\nOverrides held back (not text config files, or loader configuration):\n")
		for _, o := range plan.RestrictedOverrides {
			fmt.Printf("  - %s: %s\n", o.Path, o.Reason)
		}
		if opts.AllowAnyOverrides {
			fmt.Println("  They will be imported (--allow-any-overrides).")
		} else {
			fmt.Println("  They will be skipped; re-run with --allow-any-overrides to import them.")
		}
	}
	// #610: a patch to code is refused, not dropped.
	if plan.RestrictedPatches > 0 {
		fmt.Printf("\nPatches that change code, or apply to a file no import admitted them for:\n")
		for _, it := range plan.TreeInputs {
			for _, p := range it.Patches {
				if p.Restricted != "" {
					fmt.Printf("  - %s:%s: %s - %s\n", it.SourceID, it.ModID, p.Path, p.Restricted)
				}
			}
		}
		if opts.AllowAnyOverrides {
			fmt.Printf("  %d patch(es) will be applied (--allow-any-overrides).\n", plan.RestrictedPatches)
		} else {
			fmt.Printf("  %d patch(es): the update is refused unless you re-run with --allow-any-overrides.\n", plan.RestrictedPatches)
		}
	}
	if len(plan.Hooks) > 0 {
		fmt.Printf("\nHooks the new version carries (never applied by an update):\n")
		for _, h := range plan.Hooks {
			fmt.Printf("  - %s: %s\n", h.Hook, h.Command)
		}
	}
	for _, e := range plan.ExternalAdded {
		line := "\nNew item to get by hand: " + e.Name
		if e.URL != "" {
			line += " <" + e.URL + ">"
		}
		fmt.Println(line)
	}
	for _, e := range plan.ExternalRemoved {
		fmt.Printf("\nNo longer in the collection, to get by hand: %s (remove it yourself if you installed it)\n", e.Name)
	}
	if plan.Source != nil {
		printSourceCollection(plan.Source)
	}
	if len(plan.Unsupported) > 0 {
		fmt.Printf("\n✗ The new version needs features this version of lmm cannot import:\n")
		for _, f := range plan.Unsupported {
			fmt.Printf("    - %s\n", f.Feature)
		}
	}
	if plan.RecordedOnly {
		fmt.Printf("\n%s is not the active profile (%s is): the update records and downloads, and deploys nothing.\n", plan.Profile, plan.ActiveProfile)
	}
	fmt.Println()
}

// printCollectionUpdateResult prints what the update did.
func printCollectionUpdateResult(r *core.CollectionUpdateResult) {
	fmt.Printf("\n✓ Updated profile %s to version %d: %d added, %d updated", r.Profile, r.NewVersion, r.Added, r.Changed)
	if r.Overridden > 0 {
		fmt.Printf(", %d taken the collection's way", r.Overridden)
	}
	fmt.Println(".")
	if len(r.Removed) > 0 {
		fmt.Printf("  Removed: %s\n", strings.Join(r.Removed, ", "))
	}
	if r.Kept > 0 {
		fmt.Printf("  Kept %d mod(s) the collection no longer lists, as your own.\n", r.Kept)
	}
	if r.Conflicts > 0 {
		fmt.Printf("  Kept your version of %d mod(s) the collection changed.\n", r.Conflicts)
	}
	if imp := r.Import; imp != nil {
		for _, n := range imp.Notes {
			if !strings.HasPrefix(n, "Warning:") {
				fmt.Printf("  %s\n", n)
			}
		}
		for _, w := range imp.Warnings {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
		}
		if len(imp.OverridesSkipped) > 0 {
			fmt.Printf("  %d override(s) were skipped; re-run with --allow-any-overrides to include them.\n", len(imp.OverridesSkipped))
		}
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}
}
