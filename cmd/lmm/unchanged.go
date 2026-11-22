package main

import (
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// printUnchanged says how many mods a check answered from what lmm recorded
// the last time it checked them, because their source said they had not
// changed (#672), and which command asks about them all again.
func printUnchanged(n int, refresh string) {
	if n == 0 {
		return
	}
	fmt.Printf("%s ('%s' asks about every mod).\n", core.UnchangedSummary(n), refresh)
}
