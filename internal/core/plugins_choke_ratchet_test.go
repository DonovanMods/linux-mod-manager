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

// noPluginSyncEntries are the exported Service methods allowed to deploy
// without reaching syncMergedPak, each with why. None is, today. (The
// purge flows deploy nothing; they end with restorePluginList, which puts
// the user's own list back.)
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

// reachesDeploy reports whether some call path from root reaches a deploy.
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
			if deploy, _ := deployKind(fn, c); deploy {
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
		if reachesCall(key, "syncMergedPak", funcs, byName) {
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
