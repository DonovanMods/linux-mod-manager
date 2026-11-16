package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// handOffPathLimit is how many paths one heir's line names before "and N
// more"; handOffHeirLimit how many heirs get a line of their own.
const (
	handOffPathLimit = 5
	handOffHeirLimit = 10
)

// handOffLines renders a plan's hand-offs (#638): a header, then one line
// per heir mod, in the order each first appears - its paths, capped at
// handOffPathLimit, and the mods they come from. Past handOffHeirLimit
// heirs, one line counts the rest. Nil for none. The header hedges: the
// plan names the heir it expects, and Apply decides again from what is
// there when it runs (and says what it did). Mod names and paths are
// printed through domain.SanitizeLine, as every name the CLI prints from
// an installer or a source is.
func handOffLines(handoffs []core.HandOff) []string {
	if len(handoffs) == 0 {
		return nil
	}
	type group struct {
		heir  string
		paths []string
		from  []string
	}
	var order []string
	groups := make(map[string]*group)
	for _, h := range handoffs {
		key := domain.ModKey(h.To.SourceID, h.To.ModID)
		g, ok := groups[key]
		if !ok {
			g = &group{heir: domain.SanitizeLine(h.To.Name)}
			groups[key] = g
			order = append(order, key)
		}
		g.paths = append(g.paths, domain.SanitizeLine(domain.DisplayDeployPath(h.Root, h.Path)))
		if from := domain.SanitizeLine(h.From.Name); !slices.Contains(g.from, from) {
			g.from = append(g.from, from)
		}
	}
	lines := []string{fmt.Sprintf("%d shared file(s) expected to pass to the next mod in load order that ships them (decided again when it runs):", len(handoffs))}
	for j, key := range order {
		if j == handOffHeirLimit {
			rest := 0
			for _, k := range order[j:] {
				rest += len(groups[k].paths)
			}
			lines = append(lines, fmt.Sprintf("  and %d more mod(s) take %d more file(s)", len(order)-j, rest))
			break
		}
		g := groups[key]
		names := g.paths
		if len(names) > handOffPathLimit {
			names = names[:handOffPathLimit]
		}
		list := strings.Join(names, ", ")
		if extra := len(g.paths) - len(names); extra > 0 {
			list += fmt.Sprintf(" and %d more", extra)
		}
		verb := "passes"
		if len(g.paths) > 1 {
			verb = "pass"
		}
		lines = append(lines, fmt.Sprintf("  %s %s to %s (from %s)", list, verb, g.heir, strings.Join(g.from, ", ")))
	}
	return lines
}

// printHandOffs prints handOffLines, each line after indent.
func printHandOffs(indent string, handoffs []core.HandOff) {
	for _, line := range handOffLines(handoffs) {
		fmt.Println(indent + line)
	}
}
