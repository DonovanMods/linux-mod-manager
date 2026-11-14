package core

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// The file load-order verify tier (#620), mirroring the plugin list's
// (#616): the sync's findings for the active profile, and DRIFT - a file
// lmm deployed that the game's list (Cyberpunk 2077's modlist.txt) no
// longer has, or has moved relative to lmm's other files, since lmm wrote
// it. Only lmm's own files drift: a file lmm did not deploy, and the
// user's edits to it, are the user's. --fix rewrites the list. A profile
// that orders nothing has no list to drift.

// VerifyStatusLoadOrderDrift is the verify row for a file lmm deployed that
// the game's load-order list has dropped or moved since lmm wrote it: a
// warning, fixable on a plain run (--fix rewrites the list).
const VerifyStatusLoadOrderDrift = "load_order_drift"

// VerifyStatusFixedLoadOrderDrift is the row a --fix run leaves for the
// load-order list it rewrote.
const VerifyStatusFixedLoadOrderDrift = "fixed_" + VerifyStatusLoadOrderDrift

// loadOrderPass runs the tier, beside the plugin list's.
func (r *verifyRun) loadOrderPass() {
	if r.opts.ModFilter != "" {
		return // the list is the profile's, never one mod's
	}
	specs := r.svc.loadOrderSpecs(r.game)
	if len(specs) == 0 {
		return
	}
	if _, recordedOnly, err := r.svc.profileScope(r.ctx, r.game.ID, r.profile); err != nil || recordedOnly {
		return
	}
	for _, spec := range specs {
		c, err := r.svc.computeLoadOrder(r.ctx, r.game, r.profile, spec, nil, false)
		if err != nil {
			r.result.Issues++
			r.finding(VerifyFinding{Status: LoadOrderFindingUnwritable, Note: fmt.Sprintf("the %s list cannot be read: %v", spec.Label, err)}, VerifyEvent{})
			continue
		}
		if c.nothing || c.restore {
			continue
		}
		for _, f := range c.findings {
			if f.Severity == PluginSeverityError {
				r.result.Issues++
			} else {
				r.result.Warnings++
			}
			r.finding(VerifyFinding{Status: f.Code, FileID: f.File, Note: f.Message}, VerifyEvent{})
		}
		if c.skipped {
			continue
		}
		drift := loadOrderDrift(c)
		if len(drift) == 0 {
			continue
		}
		if r.opts.Fix {
			if _, err := r.svc.writeLoadOrder(r.game, c); err != nil {
				r.result.Warnings++
				r.finding(VerifyFinding{Status: VerifyStatusLoadOrderDrift, FileID: c.listName,
					Note: fmt.Sprintf("%s; rewriting the list failed: %v", strings.Join(drift, "; "), err)}, VerifyEvent{})
				continue
			}
			r.finding(VerifyFinding{Status: VerifyStatusFixedLoadOrderDrift, FileID: c.listName,
				Note: fmt.Sprintf("rewrote %s: %s", c.listName, strings.Join(drift, "; "))}, VerifyEvent{Fixed: true})
			continue
		}
		for _, d := range drift {
			r.result.Warnings++
			r.finding(VerifyFinding{Status: VerifyStatusLoadOrderDrift, FileID: c.listName, Note: d, Fixable: true,
				FixableReason: fmt.Sprintf("--fix rewrites %s in the profile's order (`lmm deploy` does too)", c.listName)}, VerifyEvent{})
		}
	}
}

// loadOrderDrift describes each of lmm's own files that the current list
// has dropped, or moved relative to lmm's other files - and a list that is
// gone altogether.
func loadOrderDrift(c *loadOrderComputation) []string {
	var want []string
	for _, n := range c.entries {
		if _, ok := c.managed[loadOrderKey(n)]; ok {
			want = append(want, n)
		}
	}
	if len(want) == 0 {
		return nil
	}
	if !c.listExists {
		return []string{fmt.Sprintf("%s is missing; the profile orders %d file(s) in it", c.listName, len(want))}
	}
	listed := map[string]bool{}
	var have []string
	for _, n := range c.current {
		key := loadOrderKey(n)
		if _, ok := c.managed[key]; ok && !listed[key] {
			listed[key] = true
			have = append(have, key)
		}
	}
	var out []string
	var wantListed []string
	for _, n := range want {
		if !listed[loadOrderKey(n)] {
			out = append(out, fmt.Sprintf("%s is not in %s", n, c.listName))
			continue
		}
		wantListed = append(wantListed, n)
	}
	for i, n := range wantListed {
		if i < len(have) && have[i] != loadOrderKey(n) {
			out = append(out, fmt.Sprintf("%s has moved in %s since lmm wrote it", n, c.listName))
			break
		}
	}
	return out
}

// loadOrderToken is a stat of game's load-order list files, for the verify
// memo: their size and modification time, or "" for a game with none.
func (s *Service) loadOrderToken(game *domain.Game) string {
	specs := s.loadOrderSpecs(game)
	if len(specs) == 0 || game.InstallPath == "" {
		return ""
	}
	var b strings.Builder
	for _, spec := range specs {
		p := foldPath(game.InstallPath, path.Join(spec.Dir.Path, spec.ListFile))
		if p == "" {
			b.WriteString("absent;")
			continue
		}
		if info, err := os.Lstat(p); err == nil {
			fmt.Fprintf(&b, "%d:%d:%s;", info.Size(), info.ModTime().UnixNano(), info.Mode())
		} else {
			b.WriteString("error;")
		}
	}
	return b.String()
}
