package control

import (
	"strings"

	"gitbay.org/gitbay/internal/gitutil"
)

// licenseNames maps content markers to display names, checked in order.
var licenseChecks = []struct{ marker, name string }{
	{"MIT License", "MIT"},
	{"Permission is hereby granted, free of charge", "MIT"},
	{"Apache License", "Apache-2.0"},
	{"GNU AFFERO GENERAL PUBLIC LICENSE", "AGPL-3.0"},
	{"GNU GENERAL PUBLIC LICENSE", "GPL"},
	{"GNU LESSER GENERAL PUBLIC LICENSE", "LGPL"},
	{"Mozilla Public License", "MPL-2.0"},
	{"BSD 3-Clause", "BSD-3-Clause"},
	{"BSD 2-Clause", "BSD-2-Clause"},
	{"Redistribution and use in source and binary forms", "BSD"},
	{"BSD Zero Clause License", "0BSD"},
	{"Zero Clause BSD", "0BSD"},
	// ISC is the 0BSD grant plus the notice-retention clause; match on the
	// clause so title-less 0BSD files don't read as ISC.
	{"hereby granted, provided that the above copyright notice", "ISC"},
	{"Permission to use, copy, modify, and/or distribute", "0BSD"},
	{"This is free and unencumbered software", "Unlicense"},
	{"CC0", "CC0"},
}

// DetectLicense reports the repo's license name from a conventional file
// at the ref root, or "". It lives here because profile show and the
// web's repo listings both report it.
func DetectLicense(dir, ref string) string {
	for _, name := range []string{"LICENSE", "LICENSE.md", "LICENSE.txt", "COPYING", "UNLICENSE"} {
		raw, err := gitutil.ReadBlob(dir, ref, name, 2048)
		if err != nil {
			continue
		}
		// Collapse whitespace so markers match across line wraps.
		text := strings.Join(strings.Fields(string(raw)), " ")
		for _, c := range licenseChecks {
			if strings.Contains(text, c.marker) {
				return c.name
			}
		}
		return "license"
	}
	return ""
}
