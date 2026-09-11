package thunderstore_test

// The package IDENTITY rules (#409, design §3.1).
//
// Thunderstore names a package `Namespace-Name` and a dependency
// `Namespace-Name-Version`. Both splits are unambiguous only because owner
// and name are strictly [A-Za-z0-9_]+ - verified across all 50,707 packages
// of the largest community on the site - which is exactly the invariant
// these tables exist to pin: if it ever stops holding, the split stops
// being a split and lmm starts addressing the wrong package.

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/thunderstore"
	"github.com/stretchr/testify/assert"
)

func TestSplitPackage(t *testing.T) {
	for _, tc := range []struct {
		in, ns, name string
		ok           bool
	}{
		{in: "RugbugRedfern-Skinwalkers", ns: "RugbugRedfern", name: "Skinwalkers", ok: true},
		{in: "BepInEx-BepInExPack_Valheim", ns: "BepInEx", name: "BepInExPack_Valheim", ok: true},
		{in: "A_B-C_D", ns: "A_B", name: "C_D", ok: true},
		{in: "tinyhoot-ShipLoot", ns: "tinyhoot", name: "ShipLoot", ok: true},
		{in: "NoHyphenAtAll"},
		{in: "-Leading"},
		{in: "Trailing-"},
		{in: "-"},
		{in: ""},
	} {
		t.Run(tc.in, func(t *testing.T) {
			ns, name, ok := thunderstore.SplitPackage(tc.in)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.ns, ns)
			assert.Equal(t, tc.name, name)
		})
	}
}

// TestSplitDependency covers §3.1's second half. A dependency string is the
// package's full_name with a version appended, so it splits on the FIRST
// TWO hyphens rather than the last two: namespace and name cannot contain
// one, and reading the version as "everything after the second" is the only
// reading that survives a version which someday does (`1.0.0-beta`).
func TestSplitDependency(t *testing.T) {
	for _, tc := range []struct {
		name                string
		in, ns, pkg, verNum string
		ok                  bool
	}{
		{name: "the common case", in: "BepInEx-BepInExPack-5.4.2100",
			ns: "BepInEx", pkg: "BepInExPack", verNum: "5.4.2100", ok: true},
		{name: "underscores in both halves", in: "A_B-C_D-1.2.3",
			ns: "A_B", pkg: "C_D", verNum: "1.2.3", ok: true},
		{name: "a loader pack in a third-party namespace", in: "denikson-BepInExPack_Valheim-5.4.2202",
			ns: "denikson", pkg: "BepInExPack_Valheim", verNum: "5.4.2202", ok: true},
		{name: "a pre-release version keeps its own hyphen", in: "Owner-Name-1.0.0-beta.1",
			ns: "Owner", pkg: "Name", verNum: "1.0.0-beta.1", ok: true},
		{name: "no version at all", in: "BepInEx-BepInExPack"},
		{name: "no name and no version", in: "BepInEx"},
		{name: "empty", in: ""},
		{name: "an empty namespace", in: "-Name-1.0.0"},
		{name: "an empty name", in: "Owner--1.0.0"},
		{name: "an empty version", in: "Owner-Name-"},
		{name: "a version that is not one", in: "Owner-Name-Extra-1.0.0"},
		{name: "a hyphen in the name is not a name", in: "Owner-Na-me"},
		{name: "a path separator", in: "Owner-Name-../../etc"},
		{name: "whitespace", in: "Owner-Name-1.0.0 "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ns, pkg, version, ok := thunderstore.SplitDependency(tc.in)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.ns, ns)
			assert.Equal(t, tc.pkg, pkg)
			assert.Equal(t, tc.verNum, version)
		})
	}
}

// TestSplitDependencyRoundTripsAPackage pins the relationship the two
// helpers have to each other: a dependency string's first two fields ARE a
// package's full_name, which is what makes a dependency addressable as a
// mod id at all.
func TestSplitDependencyRoundTripsAPackage(t *testing.T) {
	ns, name, version, ok := thunderstore.SplitDependency("RugbugRedfern-Skinwalkers-3.0.2")
	assert.True(t, ok)

	ns2, name2, ok2 := thunderstore.SplitPackage(ns + "-" + name)
	assert.True(t, ok2)
	assert.Equal(t, ns, ns2)
	assert.Equal(t, name, name2)
	assert.Equal(t, "3.0.2", version)
}
