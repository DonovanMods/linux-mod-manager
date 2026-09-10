package serve_test

// The version-DISPLAY ratchet for the SPA's own JavaScript.
//
// issue 269's approval note (docs/plans/2026-09-09-steam-workshop-design.md,
// "Approved with notes") makes one rule about one wire field: an EXTERNAL
// mod's domain.Mod.Version holds Steam's 19-digit content id, so "no
// human-facing surface prints a 19-digit number as a version" - the surface
// shows the item's revision date, and only `lmm mod show` and the mod page
// carry the manifest at all, labelled as such underneath.
//
// Since #365 the two PLAN renderers that could not branch at all -
// plan_profile_sync.js and plan_profile_import.js - can: domain.ModReference
// carries additive `external`/`updated_at` that core stamps, so they read
// version.js like every other surface and are no longer registered here.
//
// That rule shipped first as a derived field on one library row, and five
// sibling surfaces went on printing the raw field anyway: the mod panel's meta
// line, the full mod page's, the uninstall confirmation, the Updates card and
// its confirm modal. Every one of them had a good local reason not to have the
// derived field - a different document, a plan rather than a listing, its own
// row literal - which is exactly why the rule now lives in
// spa/app/version.js as a function every surface calls.
//
// A grep ratchet is the right shape here for the same reason
// no_unsafe_dom_test.go's is: the failure it prevents is a NEW surface reading
// the field directly because that is the obvious thing to write, and it costs
// nothing to make that fail the build instead of a browser. Every read below
// is registered with the reason it is safe, so a reader can check the claim
// rather than trust the number.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// versionRead matches a read of any of the four version-bearing wire fields a
// mod document carries. It matches the PROPERTY, not the receiver, so it also
// catches `(...)?.new_version` - the shape a chained find() produces, and the
// one an expression-anchored pattern misses.
//
// The \b is load-bearing twice over: it keeps `.versions` (a source's version
// LIST, a different document entirely) and `.locked_version` out.
//
// It matches property ACCESS only, so `mod["version"]` would slip past. That
// limit is deliberate: this codebase never writes that shape, and widening the
// pattern to string literals would fire on every doc comment that names the
// field - including the ones in this file.
var versionRead = regexp.MustCompile(`\??\.\s*(version|new_version|installed_version|available_version)\b`)

// versionAllowance is one registered raw read: how many, and why that file is
// allowed to bypass version.js.
type versionAllowance struct {
	count int
	why   string
}

// allowedRawVersionReads is the registry. Keys are paths under
// internal/serve/spa/app; version.js is deliberately absent, because it IS
// the sanctioned reader - a read there is the implementation of the rule, and
// TestVersionDisplayGoesThroughTheSharedHelper fails if it stops being the
// only unregistered file.
//
// Adding a raw read means adding a line here with a reason someone can check.
// If the reason is "this row can never be external", say why core makes that
// true; if it cannot be said, the read belongs in version.js#displayVersion.
var allowedRawVersionReads = map[string]map[string]versionAllowance{
	"modrows.js": {
		"version": {1, "lockedNote's fallback, reached only past its own `installed_mod?.external` early return - a locked EXTERNAL row says the bare word 'locked', because its lock target IS the content id."},
	},
	"components/modpanel.js": {
		"version": {3, "One is the CATALOG panel's meta line, whose catalogMod is a domain.Mod - a document with no External field at all, for a mod that is not installed (Workshop search is not Tier 1). The other two are ManagedBySteam's LABELLED 'Steam content id' line: the one place the design asks for the manifest."},
	},
	"components/fullmodpage.js": {
		"version":     {3, "Two are a dependency's domain.ModReference (no External field, and a dependency is never an adopted Workshop item); one compares against the SOURCE's own version list to mark the installed row, and renders nothing."},
		"new_version": {1, "The versions table's update target. That table renders only when the source answers GetModFiles, which the Workshop source does not (source.ErrNotSupported), so it never has an external mod in it."},
	},
	"components/plan_deploy.js": {
		"version": {2, "core.DeployPlanMod.Ref.Version, rendered only under `mod.class !== \"external\"` - the external row's own detail line says what lmm will not do with it instead."},
	},
	"components/plan_uninstall.js": {
		"version": {1, "core.UninstallPlan.Mod.Version, rendered only under `!plan.external`; the external branch omits the span because UninstallPlan carries no timestamp to put a date there instead."},
	},
	"components/plan_purge.js": {
		"version": {1, "core.PurgePlan.Mods is partitionExternal's NON-external half (internal/core/purge.go); the external items are names only, in their own 'Left alone' section, and carry no version."},
	},
	"components/plan_switch.js": {
		"version": {4, "core.SwitchPlan's three buckets. ToDisable and ToEnable are safe outright: PlanProfileSwitch classifies an INSTALLED row, and `if installed && im.External { continue }` counts it under ExternalUnchanged first (internal/core/switch.go). ToInstall is the KNOWN GAP (#365 fixed its two named siblings, plan_profile_sync.js and plan_profile_import.js, by stamping domain.ModReference's additive external/updated_at in core; this bucket and plan_profile_apply.js's hold a ProfileApplyInstall/ref pair rather than a bare slice, so the same stamping is a separate change): a profile ref whose installed row is gone falls past that guard as a bare domain.ModReference, which carries no stamped External flag to branch on."},
	},
	"components/plan_profile_apply.js": {
		"version": {4, "core.ProfileApplyPlan's three buckets, no longer a gap. ToDisable and ToEnable are safe outright: pass 1's `if im.External { plan.ExternalUnchanged++; continue }` classifies an INSTALLED row (internal/core/profile_apply.go), and that is one read. ToInstall's two both sit past installVersion's own `if (entry.external) return displayVersion(entry.ref)` early return - pass 2 builds the external entry from a tracking row and PlanProfileApply stamps that ref's external/updated_at right there (P1a review F5), so the shared helper answers for it - and TestE2E_Workshop_ProfileApplyPlanShowsTheTrackedItemAsTracked drives that branch in a real browser, so the claim is executed rather than merely written down (P1a re-review N4). The fourth is `replaces.version`, an installed row this apply converges away from, which pass 2 never sets on an external entry."},
	},
	"components/plan_install.js": {
		"version": {7, "Source-side file and version lists on core.InstallPlan. PlanInstall refuses a mod already tracked from Steam as its FIRST statement (issue 269's install-exclusivity gate), so an external mod never reaches this renderer."},
	},
	"components/plan_import_archive.js": {
		"version": {1, "The mod an archive import creates. Adopt is the only producer of an external row, and it takes no archive."},
	},
	"components/gameloader.js": {
		"version": {3, "Not a MOD's version at all: this is domain.GameLoader.Version / core.LoaderSpec.Version, the BepInEx build installed in the GAME directory (issue 359). A game carries no External field and is never a Workshop item, so displayVersion has nothing to answer here - it takes a mod. Two reads seed and patch the editor's draft; one is the version input's value."},
	},
}

// TestVersionDisplayGoesThroughTheSharedHelper walks the SPA's modules and
// fails on any read of a version-bearing wire field that is neither
// version.js's own nor registered above with a reason.
func TestVersionDisplayGoesThroughTheSharedHelper(t *testing.T) {
	root := filepath.Join(".", "spa", "app")

	found := map[string]map[string]int{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || filepath.Ext(path) != ".js" {
			return nil
		}
		rel := filepath.ToSlash(mustRel(t, root, path))
		if rel == "version.js" {
			// The implementation of the rule, not a bypass of it.
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			// A doc comment naming the field is documentation, not a read -
			// and this package's comments name them constantly.
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
				continue
			}
			for _, m := range versionRead.FindAllStringSubmatch(line, -1) {
				if found[rel] == nil {
					found[rel] = map[string]int{}
				}
				found[rel][m[1]]++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	for _, rel := range sortedKeys(found) {
		for _, field := range sortedKeys(found[rel]) {
			got := found[rel][field]
			allowance, ok := allowedRawVersionReads[rel][field]
			if !ok {
				t.Errorf("%s reads .%s %d time(s) without going through spa/app/version.js.\n"+
					"  Use displayVersion(mod) / displayUpdateTarget(update) - an EXTERNAL mod's version is\n"+
					"  Steam's 19-digit content id, which no human-facing surface may print as a version\n"+
					"  (issue 269's approval note). If this read genuinely cannot see an external mod, add it to\n"+
					"  allowedRawVersionReads in version_display_test.go with the reason core makes that true.",
					rel, field, got)
				continue
			}
			if got != allowance.count {
				t.Errorf("%s reads .%s %d time(s), registered for %d.\n"+
					"  Registered reason: %s\n"+
					"  Update allowedRawVersionReads deliberately, or route the new read through version.js.",
					rel, field, got, allowance.count, allowance.why)
			}
		}
	}

	// The other direction: a registry entry whose reads are gone is a stale
	// claim about the tree, and a ratchet nobody trims stops being read.
	for _, rel := range sortedKeys(allowedRawVersionReads) {
		for _, field := range sortedKeys(allowedRawVersionReads[rel]) {
			if found[rel][field] == 0 {
				t.Errorf("allowedRawVersionReads[%q][%q] is stale: no such read remains. Delete the entry.", rel, field)
			}
		}
	}
}

// mustRel is filepath.Rel with the test's own failure mode.
func mustRel(t *testing.T, base, path string) string {
	t.Helper()
	rel, err := filepath.Rel(base, path)
	if err != nil {
		t.Fatalf("relativising %s against %s: %v", path, base, err)
	}
	return rel
}

// sortedKeys keeps the failure output stable across runs, whatever order the
// walk and the maps produce.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
