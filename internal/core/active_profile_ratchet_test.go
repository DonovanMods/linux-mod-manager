package core_test

// The structural half of #462. A game has one game directory, and it holds
// the ACTIVE profile's mods (#445), so every flow that writes into it - a
// deploy, or a removal - has to know which profile it is acting for before
// it does. The tests in inactive_profile_flows_test.go prove the flows they
// list; this ratchet makes a new flow that forgets the question fail the
// build: from every exported Service method, no call path reaches a deploy
// (an Installer's Install or Replace, a linker's Deploy, the profile's
// config overrides) or a removal (an Installer's Uninstall, a linker's
// Undeploy) without an active-profile check on the way - or the method is
// named below, with why it may.

import (
	"go/ast"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// activeProfileGates are the calls that decide which profile a flow acts
// for:
//
//   - refuseInactive / requireActiveProfile: a deploy-direction flow is
//     refused for a profile that is not active;
//   - profileScope / importScope: a flow that runs recorded-only for such a
//     profile (uninstall, disable, profile import, the merged-artifact
//     resync) branches on it;
//   - liveProfile: the primitive both read, which a flow that decides for
//     itself (purge, switch's rollback, game edit) calls directly;
//   - refuseLiveDeploy / liveRelinker: verify --fix's repairs, which deploy
//     the verified profile's own mods, or only the live profile's rows.
var activeProfileGates = map[string]bool{
	"refuseInactive":       true,
	"requireActiveProfile": true,
	"profileScope":         true,
	"importScope":          true,
	"liveProfile":          true,
	"refuseLiveDeploy":     true,
	"liveRelinker":         true,
}

// activeProfileExempt are the exported Service methods allowed to reach the
// game directory with no active-profile check on the way, each with why.
// None is, today: the flows that legitimately run for a profile that is not
// active - a switch or a restore, which make it active; purge, uninstall,
// disable and profile import, which run recorded-only; verify --fix's
// convergence, which judges only that profile's records - all ask one of
// the gates above first and branch on the answer.
var activeProfileExempt = map[string]string{}

// recordedOnlyFlows are the exported Service methods that legitimately act
// for a profile that is not active - each with why - instead of refusing
// it. TestTheRecordedOnlyFlowsBranchOnTheActiveProfile holds each to asking
// which profile is active before it does.
var recordedOnlyFlows = map[string]string{
	"PlanPurge":            "a purge of another profile clears only what it recorded (#445)",
	"ApplyPurge":           "a purge of another profile clears only what it recorded (#445)",
	"PurgeProfile":         "a purge of another profile clears only what it recorded (#445)",
	"PlanUninstall":        "an uninstall from another profile removes only what it alone recorded (#462)",
	"ApplyUninstall":       "an uninstall from another profile removes only what it alone recorded (#462)",
	"UninstallMod":         "an uninstall from another profile removes only what it alone recorded (#462)",
	"DisableMod":           "a disable in another profile marks its document and removes only what it alone recorded (#462)",
	"PlanImport":           "an import into another profile records it and deploys nothing (#462)",
	"ApplyImport":          "an import into another profile records it and deploys nothing (#462)",
	"PlanProfileSwitch":    "a switch is how another profile becomes the active one",
	"ApplyProfileSwitch":   "a switch is how another profile becomes the active one",
	"PlanSnapshotRestore":  "a restore makes the snapshot's profile active, purging the one active now (#462 G2-4)",
	"ApplySnapshotRestore": "a restore makes the snapshot's profile active, purging the one active now (#462 G2-4)",
	"PlanProfileSync":      "a sync edits another profile's document only (#444)",
	"ApplyRelinkMod":       "a relink of another profile's mod rewrites its records; the merged-artifact resync is skipped (#462)",
	"VerifyReport":         "verify --fix on another profile converges only its records, and deploys nothing (#462)",
}

// isInstallerReceiver reports whether a method call's receiver names an
// Installer (installer, holder.installer, i.installer, ...).
func isInstallerReceiver(recv string) bool {
	return strings.Contains(strings.ToLower(recv), "installer")
}

// liveDirSite reports whether c writes into the game directory: a deploy
// (deployKind) or a removal.
func liveDirSite(fn *coreFunc, c *coreCall) bool {
	if deploy, _ := deployKind(fn, c); deploy {
		return true
	}
	if fn.recv == "Installer" || !c.method {
		return false
	}
	switch c.name {
	case "Uninstall", "uninstall":
		return isInstallerReceiver(c.recv)
	case "Undeploy":
		return true // a linker, bypassing the Installer
	}
	return false
}

// TestEveryLiveDirectoryWriteAsksWhichProfileIsActive: see the file comment.
func TestEveryLiveDirectoryWriteAsksWhichProfileIsActive(t *testing.T) {
	funcs, byName := coreCallGraph(parseCorePackage(t), activeProfileGates)

	// The walk must see the writes it exists to police.
	sites := map[string]bool{}
	for _, fn := range funcs {
		for _, c := range fn.calls {
			if liveDirSite(fn, c) {
				sites[fn.key] = true
			}
		}
	}
	for _, known := range []string{"Service.enableMod", "Service.disableMod", "Service.uninstallMod",
		"Service.removeRecordedPaths", "Service.purgeMods", "verifyRun.repairUnlinkedLoaderFiles"} {
		assert.True(t, sites[known], "the walk no longer sees %s's write", known)
	}

	used := map[string]bool{}
	var offenders []string
	for key, fn := range funcs {
		name := strings.TrimPrefix(key, "Service.")
		if fn.recv != "Service" || !ast.IsExported(name) {
			continue
		}
		paths := ungatedPaths(key, funcs, byName, liveDirSite)
		if len(paths) == 0 {
			continue
		}
		if _, ok := activeProfileExempt[name]; ok {
			used[name] = true
			continue
		}
		offenders = append(offenders, paths...)
	}
	sort.Strings(offenders)
	assert.Empty(t, offenders,
		"a write into the game directory reached with no active-profile check on the way (#462): "+
			"call requireActiveProfile (a deploy) or profileScope (a removal that runs recorded-only) before it, "+
			"or list the entry point in activeProfileExempt with why")
	for name := range activeProfileExempt {
		assert.True(t, used[name], "activeProfileExempt names %s, which reaches no unchecked write", name)
	}
}

// TestTheRecordedOnlyFlowsBranchOnTheActiveProfile: every flow allowed to
// act for a profile that is not active exists, and asks which profile is
// active on the way - so the list cannot outlive the check it documents.
func TestTheRecordedOnlyFlowsBranchOnTheActiveProfile(t *testing.T) {
	funcs, byName := coreCallGraph(parseCorePackage(t), activeProfileGates)
	var asks func(key string, seen map[string]bool) bool
	asks = func(key string, seen map[string]bool) bool {
		fn := funcs[key]
		if fn == nil || seen[key] {
			return false
		}
		seen[key] = true
		for _, c := range fn.calls {
			if c.isGate {
				return true
			}
			for _, callee := range byName[c.name] {
				if asks(callee, seen) {
					return true
				}
			}
		}
		return false
	}
	for name, why := range recordedOnlyFlows {
		require.NotEmpty(t, why)
		_, ok := funcs["Service."+name]
		if !assert.True(t, ok, "recordedOnlyFlows names Service.%s, which does not exist", name) {
			continue
		}
		assert.True(t, asks("Service."+name, map[string]bool{}), "Service.%s acts for another profile without asking which is active", name)
	}
}

// TestEveryActiveProfileGateReadsTheActiveProfile: a name in
// activeProfileGates is a gate only if it reaches liveProfile, so a gate
// that stops asking cannot keep the ratchet above quiet.
func TestEveryActiveProfileGateReadsTheActiveProfile(t *testing.T) {
	funcs, byName := coreCallGraph(parseCorePackage(t), activeProfileGates)
	var reaches func(key string, seen map[string]bool) bool
	reaches = func(key string, seen map[string]bool) bool {
		fn := funcs[key]
		if fn == nil || seen[key] {
			return false
		}
		seen[key] = true
		for _, c := range fn.calls {
			if c.name == "readProfileFlags" {
				return true
			}
			for _, callee := range byName[c.name] {
				if reaches(callee, seen) {
					return true
				}
			}
		}
		return false
	}
	for gate := range activeProfileGates {
		keys := byName[gate]
		require.NotEmpty(t, keys, "activeProfileGates names %s, which package core does not declare", gate)
		for _, key := range keys {
			assert.True(t, reaches(key, map[string]bool{}), "%s is listed as a gate but never reads the active profile", key)
		}
	}
}
