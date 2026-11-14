package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// The plugin-list verify tier (#616): the sort's findings for the active
// profile - a cycle, a missing master, a class over its limit, a name the
// list cannot hold - and DRIFT: a plugin lmm deployed that the game's list
// no longer has where, or as, lmm would write it. Only lmm's own plugins
// drift: a plugin lmm did not deploy, and the game's own edits to it, are
// the game's (design §5, "Ownership"). --fix rewrites the list.

// VerifyStatusPluginDrift is the verify row for a plugin lmm deployed that
// the game's plugin list has moved, deactivated or dropped since lmm wrote
// it: a warning, fixable on a plain run (--fix rewrites the list).
const VerifyStatusPluginDrift = PluginFindingDrift

// VerifyStatusFixedPluginDrift is the row a --fix run leaves for the
// plugin list it rewrote.
const VerifyStatusFixedPluginDrift = "fixed_" + PluginFindingDrift

// pluginListPass runs the tier, after the adapter's rows (which already
// report an unresolved prefix).
func (r *verifyRun) pluginListPass() {
	if r.opts.ModFilter != "" {
		return // the list is the profile's, never one mod's
	}
	spec, ok := r.svc.pluginRules(r.game)
	if !ok {
		return
	}
	if _, recordedOnly, err := r.svc.profileScope(r.ctx, r.game.ID, r.profile); err != nil || recordedOnly {
		return
	}
	c, err := r.svc.computePluginList(r.ctx, r.game, r.profile, spec)
	if err != nil {
		// An issue, as the deploy reports it (plugin_list_unwritable): a
		// list lmm cannot read - a link where the file was, a permission
		// error - is one lmm cannot keep in step either (#616 review 9).
		r.result.Issues++
		r.finding(VerifyFinding{Status: PluginFindingUnwritable, Note: fmt.Sprintf("the plugin list cannot be read: %v", err)}, VerifyEvent{})
		return
	}
	if c.nothing {
		return
	}
	for _, f := range c.findings {
		if f.Code == FindingPrefixUnresolved {
			continue // the adapter pass's prefix row says it
		}
		if f.Severity == PluginSeverityError {
			r.result.Issues++
		} else {
			r.result.Warnings++
		}
		r.finding(VerifyFinding{Status: f.Code, FileID: f.Plugin, Note: f.Message}, VerifyEvent{})
	}
	if c.skipped {
		return
	}
	drift := pluginDrift(c)
	if len(drift) == 0 {
		return
	}
	if r.opts.Fix {
		if _, err := r.svc.writePluginList(r.game, c); err != nil {
			r.result.Warnings++
			r.finding(VerifyFinding{Status: VerifyStatusPluginDrift, FileID: c.listName,
				Note: fmt.Sprintf("%s; rewriting the list failed: %v", strings.Join(drift, "; "), err)}, VerifyEvent{})
			return
		}
		r.finding(VerifyFinding{Status: VerifyStatusFixedPluginDrift, FileID: c.listName,
			Note: fmt.Sprintf("rewrote %s: %s", c.listName, strings.Join(drift, "; "))}, VerifyEvent{Fixed: true})
		return
	}
	for _, d := range drift {
		r.result.Warnings++
		r.finding(VerifyFinding{Status: VerifyStatusPluginDrift, FileID: c.listName, Note: d, Fixable: true,
			FixableReason: "--fix rewrites the plugin list in lmm's order (`lmm deploy` does too)"}, VerifyEvent{})
	}
}

// pluginDrift describes each of lmm's own plugins that the current list
// has dropped, deactivated or moved relative to lmm's other plugins.
func pluginDrift(c *pluginComputation) []string {
	have := map[string]listEntry{}
	var haveOrder []string
	for _, e := range c.current.entries {
		key := pluginKey(e.name)
		have[key] = e
		if c.managed[key] {
			haveOrder = append(haveOrder, key)
		}
	}
	var wantOrder []string
	var out []string
	for _, e := range c.entries {
		key := pluginKey(e.name)
		if !c.managed[key] {
			continue
		}
		wantOrder = append(wantOrder, key)
		cur, listed := have[key]
		switch {
		case !listed:
			out = append(out, fmt.Sprintf("%s is not in %s", e.name, c.listName))
		case cur.active != e.active && e.active:
			out = append(out, fmt.Sprintf("%s is inactive in %s; the profile activates it", e.name, c.listName))
		case cur.active != e.active:
			out = append(out, fmt.Sprintf("%s is active in %s; the profile deactivates it", e.name, c.listName))
		}
	}
	// Order among lmm's own plugins: compare the listed ones' sequence.
	listed := map[string]bool{}
	for _, k := range haveOrder {
		listed[k] = true
	}
	var want []string
	for _, k := range wantOrder {
		if listed[k] {
			want = append(want, k)
		}
	}
	names := map[string]string{}
	for _, e := range c.entries {
		names[pluginKey(e.name)] = e.name
	}
	for i := range want {
		if i < len(haveOrder) && haveOrder[i] != want[i] {
			out = append(out, fmt.Sprintf("%s has moved in %s since lmm wrote it", names[want[i]], c.listName))
			break
		}
	}
	return out
}

// pluginListToken is a stat of game's list files, for the verify memo:
// their size and modification time, or "" for a game with no list.
func (s *Service) pluginListToken(game *domain.Game) string {
	spec, ok := s.pluginRules(game)
	if !ok {
		return ""
	}
	st := s.ResolvePrefix(game)
	if !st.Resolved {
		return "unresolved"
	}
	dir := foldPath(st.UserPath, filepath.ToSlash(filepath.Join("AppData", "Local", appDataFolder(game, spec))))
	if dir == "" {
		return "absent"
	}
	var b strings.Builder
	for _, name := range []string{spec.ListFile, spec.OrderFile} {
		if name == "" {
			continue
		}
		p := foldPath(dir, name)
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
