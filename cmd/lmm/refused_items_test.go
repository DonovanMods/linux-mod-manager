package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
)

// TestPrintRefusedItems_RefusedFirstHeldBackCounted (#655 review S1): a
// refused import of a big collection held back hundreds of mods behind one
// refusal. The refusal leads, the held-back mods are counted on one line,
// and the summary says how many of each.
func TestPrintRefusedItems_RefusedFirstHeldBackCounted(t *testing.T) {
	var items []refusedItem
	for i := range 299 {
		items = append(items, refusedItem{Name: fmt.Sprintf("Mod %d", i), SourceID: "s", ModID: fmt.Sprint(i), Refusal: core.RefusalHeldBack})
	}
	items = append(items, refusedItem{Name: "Thing", SourceID: "s", ModID: "thing", Reason: "needs BepInEx", Refusal: core.RefusalLoaderRequired})

	out := captureStdout(t, func() error { printRefusedItems(items); return nil })
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	assert.Equal(t, []string{
		"1 mod refused · 299 held back",
		"  ✗ Thing (s:thing): needs BepInEx",
		"  - 299 mods held back, not written because another mod refused",
	}, lines)
}

// TestPrintRefusedItems_AFewHeldBackAreNamed: with only a few held back,
// each is still named, as before.
func TestPrintRefusedItems_AFewHeldBackAreNamed(t *testing.T) {
	items := []refusedItem{
		{Name: "Mod One", SourceID: "s", ModID: "1", Refusal: core.RefusalHeldBack},
		{Name: "Thing", SourceID: "s", ModID: "thing", Reason: "needs BepInEx", Refusal: core.RefusalLoaderRequired},
	}
	out := captureStdout(t, func() error { printRefusedItems(items); return nil })
	assert.Equal(t, "1 mod refused · 1 held back\n"+
		"  ✗ Thing (s:thing): needs BepInEx\n"+
		"  - Mod One (s:1): held back, not written because another mod refused\n", out)
}
