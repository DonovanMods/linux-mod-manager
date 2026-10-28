// Package thunderstore: this file is IDENTITY - how Thunderstore's two
// string forms become the ids lmm addresses a package by, and how a
// dependency string is told apart from the mod loader (#360 §3.1, §3.4).
//
// A package is `Namespace-Name`; a dependency is that with a version
// appended, `Namespace-Name-Version`. Both splits are unambiguous for one
// measured reason: across all 50,707 packages of the largest community on
// the site, owner and name are strictly [A-Za-z0-9_]+ and full_name is
// always exactly owner + "-" + name. That invariant is the whole basis of
// the identity, so identity_test.go pins it rather than trusting it.
//
// Identity is NOT the uuid4 every package also carries: full_name is what
// the user types, what a dependency string names, what the URL contains,
// and what survives a re-upload.
package thunderstore

import (
	"regexp"
	"strings"
)

// identifierPattern is the shape Thunderstore gives a namespace and a
// package name. It is what makes SplitDependency's split safe: neither half
// can contain the "-" the split is made on.
var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// SplitDependency splits one of Thunderstore's dependency strings -
// "BepInEx-BepInExPack-5.4.2100" - into the package it names and the
// version it pins.
//
// It splits on the FIRST TWO hyphens rather than the last two. Both give
// the same answer for every dependency string the site serves today, since
// a namespace and a name contain no hyphen and a version number is strictly
// x.y.z; the difference is what happens to a version that someday carries
// one of its own ("1.0.0-beta.1"), which this reading keeps whole and the
// other reading would truncate. The namespace and the name are validated
// against the measured invariant, and the version must LOOK like a version
// (it starts with a digit) - which is what refuses "Owner-Name-Extra-1.0.0",
// a string whose middle field is not a name at all.
//
// ok is false for anything that is not that shape. A caller treats such a
// dependency as one it cannot address - a plan warning naming the string -
// rather than guessing at what was meant.
func SplitDependency(dep string) (namespace, name, version string, ok bool) {
	ns, rest, found := strings.Cut(dep, "-")
	if !found {
		return "", "", "", false
	}
	pkg, ver, found := strings.Cut(rest, "-")
	if !found {
		return "", "", "", false
	}
	if !identifierPattern.MatchString(ns) || !identifierPattern.MatchString(pkg) {
		return "", "", "", false
	}
	if !looksLikeVersion(ver) {
		return "", "", "", false
	}
	return ns, pkg, ver, true
}

// looksLikeVersion is the weakest test that still tells a version apart
// from a third name-shaped field: it is non-empty, it starts with a digit,
// and it carries nothing that would make it unsafe to put in a path or a
// URL. Deliberately NOT a strict x.y.z match: every version on the site is
// x.y.z today, and a stricter test would silently DROP a real dependency
// the day one is not, which is worse than carrying a version string lmm
// does not recognise.
func looksLikeVersion(version string) bool {
	if version == "" || version[0] < '0' || version[0] > '9' {
		return false
	}
	return !strings.ContainsAny(version, " \t\r\n/\\\x00")
}
