package notices

import (
	"sort"
	"strings"
)

// DetectSPDX returns the SPDX ids of every license whose standard text (or a
// distinctive phrase of it) appears in text, sorted. A file that bundles
// several licenses, such as yaml's MIT-plus-Apache LICENSE, yields several
// ids. An empty result means no rule matched; the generator treats that as
// an error rather than guessing.
//
// Detection is deliberately small: it knows the licenses lmm's dependencies
// use today. A new one fails the generator, and the fix is one more rule
// here (or an entry in spdxOverrides) plus a test.
func DetectSPDX(text string) []string {
	t := strings.ToLower(strings.Join(strings.Fields(text), " "))
	ids := map[string]bool{}

	// The full text, or the boilerplate grant yaml's LICENSE carries
	// beside its MIT text.
	if (strings.Contains(t, "apache license") && strings.Contains(t, "version 2.0, january 2004")) ||
		strings.Contains(t, "licensed under the apache license, version 2.0") {
		ids["Apache-2.0"] = true
	}
	if strings.Contains(t, "permission is hereby granted, free of charge, to any person obtaining a copy") {
		ids["MIT"] = true
	}
	if strings.Contains(t, "redistribution and use in source and binary forms") &&
		strings.Contains(t, "redistributions in binary form must reproduce") {
		if strings.Contains(t, "endorse or promote") || strings.Contains(t, "may not be used to endorse") {
			ids["BSD-3-Clause"] = true
		} else {
			ids["BSD-2-Clause"] = true
		}
	}
	if strings.Contains(t, "permission to use, copy, modify, and/or distribute this software for any purpose with or without fee") {
		ids["ISC"] = true
	}
	if strings.Contains(t, "public domain") {
		ids["LicenseRef-PublicDomain"] = true
	}

	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
