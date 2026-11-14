package core_test

// The plugin-list choke point's ratchet (#616, design §5 "The sort"): the
// game's Plugins.txt is rewritten in syncMergedPak, the resync every
// deploying flow ends with. This proves "every": from each exported
// Service method that can reach a deploy - an Installer's Install or
// Replace, a linker's Deploy, the profile's overrides - some call path also
// reaches syncMergedPak, unless the method is listed below with why. A new
// flow that deploys plugins and forgets the resync fails here.

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// pluginSyncCalls are the calls that write (or put back) the plugin list.
var pluginSyncCalls = map[string]bool{
	"syncMergedPak":             true,
	"syncPluginListBeforeHooks": true,
	"restorePluginList":         true,
	"resyncAfterDeploy":         true,
}

// guardedPluginSyncs are the plugin-list calls allowed inside a block of
// their function other than its body - an if, a for, a switch, a closure -
// keyed "Recv.func -> callee", each naming its guard and why the list
// still follows every change (#616 review 5b). A new conditional call
// fails TestPluginListSyncCallsAreUnconditional until it is listed here.
var guardedPluginSyncs = map[string]string{
	"Service.applyImportArchive -> syncMergedPak": "if game.DeployMode == DeployCompile: the merged artifact's resync; the plugin step runs unconditionally just above it (syncPluginListBeforeHooks)",
	"Service.purgeProfile -> restorePluginList":   "the restorePlugins closure, run once (if !restored) before uninstall.after_all or after an empty loop",
	"Service.deployProfile -> resyncAfterDeploy":  "if purgedAny on the nothing-to-deploy early return; the main path calls it unconditionally",
}

// TestPluginListSyncCallsAreUnconditional: the ratchet above sees a call
// to syncMergedPak anywhere in a flow, guarded or not, and a guard is how
// archive import's plugin step was missed (its resync is compile-only).
// So every plugin-list call must sit in its function's own body - an if's
// init statement counts, its branches do not - or be listed in
// guardedPluginSyncs with its guard.
func TestPluginListSyncCallsAreUnconditional(t *testing.T) {
	funcs, _ := coreCallGraph(parseCorePackage(t), nil)
	used := map[string]bool{}
	var offenders []string
	for key, fn := range funcs {
		for _, c := range fn.calls {
			if !pluginSyncCalls[c.name] || len(c.blocks) <= 1 {
				continue
			}
			site := key + " -> " + c.name
			if _, ok := guardedPluginSyncs[site]; ok {
				used[site] = true
				continue
			}
			offenders = append(offenders, site)
		}
	}
	sort.Strings(offenders)
	assert.Empty(t, offenders, "a plugin-list call inside a conditional block: make it unconditional, or list it in guardedPluginSyncs naming the guard")
	for site := range guardedPluginSyncs {
		assert.True(t, used[site], "guardedPluginSyncs lists %s, which is no longer a guarded call: drop it", site)
	}
}

// noPluginSyncEntries are the exported Service methods allowed to deploy
// or remove without reaching syncMergedPak (or, for the purge flows,
// restorePluginList, which puts the user's own list back), each with why.
// None is, today.
var noPluginSyncEntries = map[string]string{}

// ownCall reports whether c calls one of package core's own functions or
// Service/verifyRun/ProfileManager methods - a plain call, or a method on
// the receivers those types are always named by (s, r, svc, pm) - so the
// walk follows the flow's own code, not every method that shares a name
// with one (a db.Delete is not Service.Delete...).
func ownCall(c *coreCall) bool {
	if !c.method {
		return true
	}
	switch c.recv {
	case "s", "r", "svc", "pm":
		return true
	}
	return false
}

// reachesCall reports whether some call path from root names target.
func reachesCall(root, target string, funcs map[string]*coreFunc, byName map[string][]string) bool {
	seen := map[string]bool{}
	var walk func(key string) bool
	walk = func(key string) bool {
		fn := funcs[key]
		if fn == nil || seen[key] {
			return false
		}
		seen[key] = true
		for _, c := range fn.calls {
			if c.name == target {
				return true
			}
			if !ownCall(c) {
				continue
			}
			for _, callee := range byName[c.name] {
				if walk(callee) {
					return true
				}
			}
		}
		return false
	}
	return walk(root)
}

// changesDeployment reports whether c changes what is deployed: a deploy
// (deployKind) or a removal - an Installer's Uninstall, a linker's
// Undeploy, the shared purge loop - so a remove-only flow (a disable, an
// uninstall, a copy that leaves a replaced row off) is policed too (#616
// review 5a).
func changesDeployment(fn *coreFunc, c *coreCall) bool {
	if deploy, _ := deployKind(fn, c); deploy {
		return true
	}
	if fn.recv == "Installer" {
		return false
	}
	switch {
	case c.method && (c.name == "Uninstall" || c.name == "uninstallHandingOff" || c.name == "uninstallHandingOffExcept" || c.name == "Undeploy"):
		return true
	case !c.method && c.name == "purgeMods":
		return true
	}
	return false
}

// reachesDeploy reports whether some call path from root reaches a deploy
// or a removal.
func reachesDeploy(root string, funcs map[string]*coreFunc, byName map[string][]string) bool {
	seen := map[string]bool{}
	var walk func(key string) bool
	walk = func(key string) bool {
		fn := funcs[key]
		if fn == nil || seen[key] || fn.recv == "Installer" {
			return false
		}
		seen[key] = true
		for _, c := range fn.calls {
			if changesDeployment(fn, c) {
				return true
			}
			if !ownCall(c) {
				continue
			}
			for _, callee := range byName[c.name] {
				if walk(callee) {
					return true
				}
			}
		}
		return false
	}
	return walk(root)
}

func TestEveryDeployingFlowReachesThePluginListSync(t *testing.T) {
	funcs, byName := coreCallGraph(parseCorePackage(t), nil)
	var offenders, deploying []string
	used := map[string]bool{}
	for key, fn := range funcs {
		name := strings.TrimPrefix(key, "Service.")
		if fn.recv != "Service" || name == "syncMergedPak" || !isExported(name) {
			continue
		}
		if !reachesDeploy(key, funcs, byName) {
			continue
		}
		deploying = append(deploying, name)
		if reachesCall(key, "syncMergedPak", funcs, byName) || reachesCall(key, "restorePluginList", funcs, byName) {
			continue
		}
		if _, ok := noPluginSyncEntries[name]; ok {
			used[name] = true
			continue
		}
		offenders = append(offenders, name)
	}
	sort.Strings(offenders)
	// The walk must see the flows it exists to police.
	t.Logf("deploying flows: %v", deploying)
	for _, known := range []string{
		"DisableMod", "UninstallMod", "ApplyUninstall",
		"ApplyDeploy", "ApplyInstall", "EnableMod", "ApplyProfileSwitch", "ApplyProfileApply", "ApplyProfileSync",
		"ApplyUpdate", "ApplyRollback", "ApplyRelinkMod", "ApplyCopyMod", "ApplyImportArchive", "ApplyAdopt",
		"ApplyUpdateFromArchive", "ApplySnapshotRestore", "ApplyImport", "ApplyFomodReconfigure", "VerifyReport",
	} {
		assert.Contains(t, deploying, known, "the walk no longer sees %s deploy", known)
	}
	assert.Empty(t, offenders, "these flows deploy without reaching syncMergedPak, so the game's Plugins.txt would not follow them: "+
		"end the flow with syncMergedPak, or list it in noPluginSyncEntries with why")
	for name := range noPluginSyncEntries {
		assert.True(t, used[name], "noPluginSyncEntries lists %s, which no longer deploys without the sync: drop it", name)
	}
}

func isExported(name string) bool { return name != "" && strings.ToUpper(name[:1]) == name[:1] }
