package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// validateAdapterFlag refuses an --adapter value that names no registered
// adapter, listing the ones that exist (#353).
//
// The list comes from core.Service.ListAdapters, which is why --adapter is
// a plain string flag and cmd/lmm's import allow-list does not grow an
// internal/adapter entry (design §4). An EMPTY value is always fine: it is
// the generic-files identity, and it is what every game had before the
// seam existed.
func validateAdapterFlag(service *core.Service, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	known := service.ListAdapters()
	if slices.Contains(known, name) {
		return nil
	}
	return fmt.Errorf("unknown adapter %q (available: %s)", name, strings.Join(known, ", "))
}
