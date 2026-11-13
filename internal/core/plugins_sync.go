package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// The plugin-list sync (#616, design docs/plans/2026-11-10-collections-
// design.md §5): after every flow that can change which plugins a profile
// deploys, core works out the game's effective plugin order and writes it
// to the game's Plugins.txt (and Skyrim LE's loadorder.txt) in its Wine
// prefix. The hook is syncMergedPak, the resync every deploying flow
// already ends with (see its doc comment), so no flow can deploy plugins
// and leave the list behind.
//
// OWNERSHIP (owner ruling 2026-11-10, design §11 Q3): lmm writes the whole
// list, but it re-reads the file before every write. A plugin the file
// names that lmm did not deploy, and that is in a plugin directory, is
// EXTERNAL - installed by hand, or by the game's own Creations menu - and
// keeps its active state and its place relative to its neighbours. lmm
// never deactivates a plugin it did not deploy.
//
// The effective order is NOT written back to the profile: it is a
// deterministic function of the profile's plugins: block, the deployed set
// and the file, so the profile needs no copy of it.

// PluginListWritten, PluginListUnchanged, PluginListSkipped,
// PluginListRestored and PluginListRemoved are PluginListResult.Status's
// values.
const (
	// PluginListWritten: lmm wrote the list, and it changed.
	PluginListWritten = "written"
	// PluginListUnchanged: the list already said what lmm would write.
	PluginListUnchanged = "unchanged"
	// PluginListSkipped: nothing was written; Findings say why.
	PluginListSkipped = "skipped"
	// PluginListRestored: a purge put back the list the user had before
	// lmm first wrote it.
	PluginListRestored = "restored"
	// PluginListRemoved: a purge removed the list lmm had created, since
	// there was none before.
	PluginListRemoved = "removed"
)

// PluginFinding codes. Severity PluginSeverityError is a problem the game
// will not survive (it crashes at start, or ignores the list); warning is
// one lmm worked around.
const (
	// PluginFindingCycle: the plugin order's constraints form a cycle
	// (PluginOrderCycleError); nothing was written.
	PluginFindingCycle = "plugin_order_cycle"
	// PluginFindingMissingMaster: an active plugin needs a master that is
	// not installed, or not active.
	PluginFindingMissingMaster = "plugin_missing_master"
	// PluginFindingOverLimit: more plugins of one class are active than the
	// game loads.
	PluginFindingOverLimit = "plugin_limit_exceeded"
	// PluginFindingUnencodable: a plugin's name cannot be written to the
	// list (no Windows-1252 spelling, or a control character); it is left
	// out.
	PluginFindingUnencodable = "plugin_name_unencodable"
	// PluginFindingUnreadableHeader: a plugin's header could not be read;
	// it is ordered by its extension alone.
	PluginFindingUnreadableHeader = "plugin_header_unreadable"
	// PluginFindingMissingRequirement: a profile rule's `req` names a
	// plugin that is not active.
	PluginFindingMissingRequirement = "plugin_requirement_missing"
	// PluginFindingUnwritable: the list could not be read or written (a
	// link where the file is, a permission error); nothing was written.
	PluginFindingUnwritable = "plugin_list_unwritable"
	// PluginFindingDrift: a plugin lmm deployed is not where, or not in
	// the state, lmm last wrote it - the file was edited since (verify).
	PluginFindingDrift = "plugin_drift"
)

// PluginSeverityError and PluginSeverityWarning are PluginFinding.Severity's
// values.
const (
	PluginSeverityError   = "error"
	PluginSeverityWarning = "warning"
)

// PluginFinding is one thing the plugin sync found.
type PluginFinding struct {
	// Code is the finding's kind (PluginFinding* or FindingPrefixUnresolved).
	Code string `json:"code"`
	// Severity is PluginSeverityError or PluginSeverityWarning.
	Severity string `json:"severity"`
	// Plugin is the plugin it is about, when it is about one.
	Plugin string `json:"plugin,omitempty"`
	// Missing names the masters or requirements that are not active
	// (PluginFindingMissingMaster, PluginFindingMissingRequirement).
	Missing []string `json:"missing,omitempty"`
	// Cycle is the cycle's edges (PluginFindingCycle).
	Cycle []PluginOrderEdge `json:"cycle,omitempty"`
	// Class, Count and Limit describe an over-limit class: "full",
	// "light" or "medium", how many are active, and how many load.
	Class string `json:"class,omitempty"`
	Count int    `json:"count,omitzero"`
	Limit int    `json:"limit,omitzero"`
	// Message is the finding in words, with what to do.
	Message string `json:"message"`
}

// PluginListResult is what a flow did to the game's plugin list (#616):
// DeployResult.Plugins, PurgeResult.Plugins. nil on a game whose adapter
// manages no plugin list, and for a flow acting for a profile that is not
// the active one.
type PluginListResult struct {
	// Status is PluginListWritten, PluginListUnchanged, PluginListSkipped,
	// PluginListRestored or PluginListRemoved.
	Status string `json:"status"`
	// ListFile is the list file's path; OrderFile is Skyrim LE's
	// loadorder.txt beside it. Absent when the prefix is unresolved.
	ListFile  string `json:"list_file,omitempty"`
	OrderFile string `json:"order_file,omitempty"`
	// Plugins is how many plugins the list names, Active how many of them
	// are active, and External how many of those lmm did not deploy and
	// kept as they were.
	Plugins  int `json:"plugins"`
	Active   int `json:"active"`
	External int `json:"external,omitzero"`
	// Findings are the problems found, errors first.
	Findings []PluginFinding `json:"findings,omitempty"`
}

// HasErrors reports whether any finding is an error.
func (r *PluginListResult) HasErrors() bool {
	if r == nil {
		return false
	}
	return slices.ContainsFunc(r.Findings, func(f PluginFinding) bool { return f.Severity == PluginSeverityError })
}

// warnings are r's findings as the sentences a flow's Warnings carry.
func (r *PluginListResult) warnings() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.Findings))
	for _, f := range r.Findings {
		out = append(out, "plugins: "+f.Message)
	}
	return out
}

// pluginListRecorder lets a flow with a result of its own (deploy, purge)
// read the structured outcome of the sync its choke point ran.
type pluginListRecorder struct{ result *PluginListResult }

type pluginListRecorderKey struct{}

// withPluginListRecorder returns ctx carrying rec.
func withPluginListRecorder(ctx context.Context, rec *pluginListRecorder) context.Context {
	return context.WithValue(ctx, pluginListRecorderKey{}, rec)
}

func recordPluginList(ctx context.Context, res *PluginListResult) {
	if rec, ok := ctx.Value(pluginListRecorderKey{}).(*pluginListRecorder); ok && rec != nil && res != nil {
		rec.result = res
	}
}

// pluginComputation is everything a sync knows before it writes.
type pluginComputation struct {
	spec   adapter.PluginSpec
	prefix *PrefixStatus
	// listDir holds the list file; listName and orderName are the file
	// names to write (an existing file's own spelling, else the
	// canonical one).
	listDir   prefixPath
	listName  string
	orderName string
	// listData and orderData are the files as read, and whether they
	// existed.
	listData, orderData     []byte
	listExists, orderExists bool
	// current is the list file as read.
	current pluginListFile
	// entries is the effective order; implicit the implicit plugins
	// present.
	entries  []listEntry
	implicit []string
	// managed is the plugins lmm deploys, by key.
	managed  map[string]bool
	external int
	findings []PluginFinding
	// skipped is set when nothing may be written; nothing when there is
	// nothing to report either (no prefix, and no plugin to list).
	skipped bool
	nothing bool
}

// pluginRules is game's plugin spec, when its adapter manages a plugin
// list.
func (s *Service) pluginRules(game *domain.Game) (adapter.PluginSpec, bool) {
	a, err := s.AdapterFor(game)
	if err != nil {
		return adapter.PluginSpec{}, false
	}
	return adapter.PluginRules(a, game)
}

// syncPluginList writes game's plugin list for profileName - the
// choke-point step syncMergedPak runs after every deploying flow. It never
// fails the flow that called it: the mods are deployed by then, so every
// problem is a finding on the result. nil when there is no list to manage
// (the adapter has none, or profileName is not the active profile).
func (s *Service) syncPluginList(ctx context.Context, game *domain.Game, profileName string) *PluginListResult {
	spec, ok := s.pluginRules(game)
	if !ok {
		return nil
	}
	if _, recordedOnly, err := s.profileScope(ctx, game.ID, profileName); err != nil || recordedOnly {
		return nil
	}
	c, err := s.computePluginList(ctx, game, profileName, spec)
	if err != nil {
		return &PluginListResult{Status: PluginListSkipped, Findings: []PluginFinding{unwritable(err)}}
	}
	if c.nothing {
		return nil
	}
	res := c.result()
	if c.skipped {
		res.Status = PluginListSkipped
		return res
	}
	written, err := s.writePluginList(game, c)
	if err != nil {
		res.Status = PluginListSkipped
		res.Findings = append([]PluginFinding{unwritable(err)}, res.Findings...)
		return res
	}
	res.Status = PluginListUnchanged
	if written {
		res.Status = PluginListWritten
	}
	return res
}

// unwritable is the finding for a list lmm could not read or write.
func unwritable(err error) PluginFinding {
	return PluginFinding{Code: PluginFindingUnwritable, Severity: PluginSeverityError,
		Message: fmt.Sprintf("the plugin list was not written: %v", err)}
}

// result is c as a PluginListResult, without a Status.
func (c *pluginComputation) result() *PluginListResult {
	res := &PluginListResult{External: c.external}
	if c.prefix != nil && c.prefix.Resolved && c.listName != "" {
		res.ListFile = filepath.Join(c.listDir.display(), c.listName)
		if c.orderName != "" {
			res.OrderFile = filepath.Join(c.listDir.display(), c.orderName)
		}
	}
	if !c.skipped {
		res.Plugins = len(c.entries)
		for _, e := range c.entries {
			if e.active {
				res.Active++
			}
		}
	}
	res.Findings = slices.Clone(c.findings)
	slices.SortStableFunc(res.Findings, func(a, b PluginFinding) int {
		return severityRank(a.Severity) - severityRank(b.Severity)
	})
	return res
}

func severityRank(s string) int {
	if s == PluginSeverityError {
		return 0
	}
	return 1
}

// pluginDirs indexes the plugin files a game loads from, by key: its
// mod_path's top level, then the spec's extra plugin directories.
type pluginDirs struct {
	// files maps a plugin key to the file's name and directory.
	files map[string]pluginFile
}

type pluginFile struct {
	name string
	dir  string
}

func (d pluginDirs) has(name string) bool { _, ok := d.files[pluginKey(name)]; return ok }

// scanPluginDirs lists the plugin files in game's mod_path and in spec's
// extra directories that resolve.
func scanPluginDirs(game *domain.Game, spec adapter.PluginSpec, st *PrefixStatus, folder string) pluginDirs {
	out := pluginDirs{files: map[string]pluginFile{}}
	add := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !hasPluginExt(e.Name(), spec) {
				continue
			}
			if _, seen := out.files[pluginKey(e.Name())]; !seen {
				out.files[pluginKey(e.Name())] = pluginFile{name: e.Name(), dir: dir}
			}
		}
	}
	add(game.ModPath)
	for _, extra := range spec.ExtraPluginDirs {
		if dir := rootedDir(game, extra, st, folder); dir != "" {
			add(dir)
		}
	}
	return out
}

// hasPluginExt reports whether name has one of spec's plugin extensions.
func hasPluginExt(name string, spec adapter.PluginSpec) bool {
	ext := path.Ext(name)
	return slices.ContainsFunc(spec.Extensions, func(e string) bool { return strings.EqualFold(e, ext) })
}

// rootedDir resolves a RootedPath's directory for reading, matching each
// component case-insensitively; "" when it does not resolve.
func rootedDir(game *domain.Game, rp adapter.RootedPath, st *PrefixStatus, folder string) string {
	var base string
	switch rp.Root {
	case adapter.SpecRootGame:
		base = game.InstallPath
	case adapter.SpecRootMyGames:
		if st == nil || !st.Resolved {
			return ""
		}
		base = filepath.Join(st.UserPath)
		rp.Path = path.Join("Documents", "My Games", folder, rp.Path)
	}
	if base == "" {
		return ""
	}
	return foldPath(base, rp.Path)
}

// foldPath is base joined with rel, each component of rel matched
// case-insensitively against what is on disk; "" when one is missing.
func foldPath(base, rel string) string {
	cur := base
	for _, comp := range strings.Split(rel, "/") {
		if comp == "" || comp == "." {
			continue
		}
		if _, err := os.Lstat(filepath.Join(cur, comp)); err == nil {
			cur = filepath.Join(cur, comp)
			continue
		}
		entries, err := os.ReadDir(cur)
		if err != nil {
			return ""
		}
		found := ""
		for _, e := range entries {
			if strings.EqualFold(e.Name(), comp) {
				found = e.Name()
				break
			}
		}
		if found == "" {
			return ""
		}
		cur = filepath.Join(cur, found)
	}
	return cur
}

// appDataFolder picks spec's AppData folder for game: the first candidate
// whose marker file is in the install directory, else the default.
func appDataFolder(game *domain.Game, spec adapter.PluginSpec) string {
	def := ""
	for _, f := range spec.AppDataFolders {
		if f.When == "" {
			if def == "" {
				def = f.Name
			}
			continue
		}
		if game.InstallPath != "" && foldPath(game.InstallPath, f.When) != "" {
			return f.Name
		}
	}
	return def
}

// computePluginList gathers what game's plugin list should be for
// profileName - the deployed plugins, their headers, the profile's list,
// the current file - and sorts. A finding that forbids writing sets
// c.skipped. The error is for a list file that cannot be read.
func (s *Service) computePluginList(ctx context.Context, game *domain.Game, profileName string, spec adapter.PluginSpec) (*pluginComputation, error) {
	c := &pluginComputation{spec: spec, managed: map[string]bool{}}
	st := s.ResolvePrefix(game)
	c.prefix = st
	folder := appDataFolder(game, spec)

	profile, _ := config.LoadProfile(s.configDir, game.ID, profileName)
	if profile == nil {
		profile = &domain.Profile{}
	}
	modIndex := make(map[string]int, len(profile.Mods))
	for i, m := range profile.Mods {
		modIndex[domain.ModKey(m.SourceID, m.ModID)] = i
	}

	dirs := scanPluginDirs(game, spec, st, folder)
	implicitKeys, implicit := implicitPlugins(game, spec, st, folder, dirs)
	c.implicit = implicit
	implicitlyActive := map[string]bool{}
	for _, name := range spec.ImplicitlyActive {
		if dirs.has(name) {
			implicitlyActive[pluginKey(name)] = true
		}
	}

	rows, err := s.db.ListDeployedFiles(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("reading the profile's deployed files: %w", err)
	}
	modOf := map[string]int{}
	for _, row := range rows {
		rel := filepath.ToSlash(row.Path.Rel)
		if row.Path.IsGameRoot() || strings.Contains(rel, "/") || !hasPluginExt(rel, spec) {
			continue
		}
		if row.ModPath != "" && filepath.Clean(row.ModPath) != filepath.Clean(game.ModPath) {
			continue
		}
		key := pluginKey(rel)
		if implicitKeys[key] || !dirs.has(rel) {
			continue
		}
		if _, seen := modOf[key]; seen {
			continue
		}
		idx, ok := modIndex[domain.ModKey(row.SourceID, row.ModID)]
		if !ok {
			idx = -1
		}
		modOf[key] = idx
	}
	if !st.Resolved {
		// Nothing to write without the prefix. Worth saying only when the
		// profile deploys a plugin: a profile of textures and an ENB has no
		// plugin list to keep in step.
		c.skipped = true
		if len(modOf) > 0 {
			c.findings = append(c.findings, PluginFinding{Code: FindingPrefixUnresolved, Severity: PluginSeverityWarning,
				Message: "the game's Plugins.txt was not written, because lmm cannot find the game's Wine prefix: " + st.Problem})
		} else {
			c.nothing = true
		}
		return c, nil
	}
	c.listDir = listDirOf(st, folder)
	if err := c.readCurrent(); err != nil {
		return nil, err
	}

	enabled := make(map[string]bool, len(profile.Plugins.Order))
	for _, e := range profile.Plugins.Order {
		if _, seen := enabled[pluginKey(e.Name)]; !seen {
			enabled[pluginKey(e.Name)] = e.Enabled
		}
	}
	var nodes []pluginNode
	var externals []pluginNode
	addNode := func(name string, managed bool, mod int, active bool) {
		f := dirs.files[pluginKey(name)]
		node := pluginNode{name: f.name, managed: managed, modIndex: mod, active: active}
		h, herr := readHeaderAt(filepath.Join(f.dir, f.name), spec)
		if herr != nil {
			c.findings = append(c.findings, PluginFinding{Code: PluginFindingUnreadableHeader, Severity: PluginSeverityWarning, Plugin: f.name,
				Message: fmt.Sprintf("%s: %v - it is ordered by its extension alone", f.name, herr)})
		}
		node.class = ClassifyPlugin(f.name, h, spec)
		node.masters = h.Masters
		if node.class.Blueprint || unlisted(f.name, spec) {
			return // the game lists and orders these itself
		}
		if !listableName(f.name) {
			c.findings = append(c.findings, PluginFinding{Code: PluginFindingUnencodable, Severity: PluginSeverityWarning, Plugin: f.name,
				Message: fmt.Sprintf("%q cannot be written to Plugins.txt (its name has a character with no Windows-1252 spelling, or a control character), so it is left out - rename the plugin", f.name)})
			return
		}
		if managed {
			nodes = append(nodes, node)
			c.managed[pluginKey(f.name)] = true
		} else {
			externals = append(externals, node)
		}
	}
	managedKeys := slices.Sorted(func(yield func(string) bool) {
		for k := range modOf {
			if !yield(k) {
				return
			}
		}
	})
	for _, key := range managedKeys {
		active, listed := enabled[key]
		addNode(dirs.files[key].name, true, modOf[key], !listed || active)
	}
	for _, e := range c.current.entries {
		key := pluginKey(e.name)
		if _, isManaged := modOf[key]; isManaged || implicitKeys[key] || !dirs.has(e.name) {
			continue
		}
		addNode(e.name, false, -1, e.active || implicitlyActive[key])
	}
	c.external = len(externals)
	if len(nodes) == 0 && !s.wrotePluginList(game) {
		// No plugin of lmm's, and a list lmm has never written: it is the
		// user's (or the game's) alone, and lmm leaves it as it is.
		c.nothing = true
		return c, nil
	}

	loadedFirst := make(map[string]bool, len(implicit))
	for _, name := range implicit {
		loadedFirst[pluginKey(name)] = true
	}
	sorted, err := sortPlugins(sortRequest{nodes: nodes, loadedFirst: loadedFirst, order: profile.Plugins.Order, rules: profile.Plugins.Rules})
	if err == nil && len(externals) > 0 {
		all := append(slices.Clone(sorted), externals...)
		sorted, err = sortPlugins(sortRequest{nodes: all, loadedFirst: loadedFirst, rules: profile.Plugins.Rules,
			preferred: mergedRanks(sorted, externals, c.current.entries)})
	}
	var cycle *PluginOrderCycleError
	if errors.As(err, &cycle) {
		c.skipped = true
		c.findings = append(c.findings, PluginFinding{Code: PluginFindingCycle, Severity: PluginSeverityError, Cycle: cycle.Edges,
			Message: cycle.Error() + "; the game keeps the plugin list it had"})
		return c, nil
	}
	for _, n := range sorted {
		c.entries = append(c.entries, listEntry{name: n.name, active: n.active})
	}
	c.findings = append(c.findings, checkPluginList(sorted, implicit, implicitlyActive, dirs, spec, profile.Plugins.Rules)...)
	return c, nil
}

// unlisted reports whether name starts with one of spec's prefixes the
// game strips from its list.
func unlisted(name string, spec adapter.PluginSpec) bool {
	return slices.ContainsFunc(spec.UnlistedPrefixes, func(p string) bool {
		return len(name) >= len(p) && strings.EqualFold(name[:len(p)], p)
	})
}

// readHeaderAt reads the plugin header of the file at p.
func readHeaderAt(p string, spec adapter.PluginSpec) (PluginHeader, error) {
	if spec.HeaderKind != adapter.HeaderTES4 {
		return PluginHeader{}, nil
	}
	f, err := os.Open(p)
	if err != nil {
		return PluginHeader{}, err
	}
	defer func() { _ = f.Close() }()
	return ReadPluginHeader(f)
}

// implicitPlugins is spec's implicit plugins - the fixed list, then the
// first CCC file's - by key (every one, present or not, is never listed)
// and the ones present, in load order.
func implicitPlugins(game *domain.Game, spec adapter.PluginSpec, st *PrefixStatus, folder string, dirs pluginDirs) (map[string]bool, []string) {
	keys := map[string]bool{}
	var present []string
	add := func(name string) {
		key := pluginKey(name)
		if name == "" || keys[key] {
			return
		}
		keys[key] = true
		if f, ok := dirs.files[key]; ok {
			present = append(present, f.name)
		}
	}
	for _, name := range spec.Implicit {
		add(name)
	}
	for _, ccc := range spec.CCCFiles {
		dir := rootedDir(game, adapter.RootedPath{Root: ccc.Root, Path: path.Dir(ccc.Path)}, st, folder)
		if dir == "" {
			continue
		}
		p := foldPath(dir, path.Base(ccc.Path))
		if p == "" {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(decodeWindows1252(data), "\n") {
			add(strings.TrimSpace(line))
		}
		break // the first CCC file that exists wins, even when empty
	}
	return keys, present
}

// readCurrent reads the list file (and the order file) as they are now.
func (c *pluginComputation) readCurrent() error {
	c.listName = c.spec.ListFile
	c.orderName = c.spec.OrderFile
	dir, err := c.listDir.open(false)
	if errors.Is(err, os.ErrNotExist) {
		c.current = pluginListFile{eol: eolCRLF}
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	read := func(canonical string) (string, []byte, bool, error) {
		real, err := entryFold(dir, canonical)
		if err != nil {
			return canonical, nil, false, err
		}
		if real == "" {
			return canonical, nil, false, nil
		}
		data, _, exists, err := readAt(dir, real)
		return real, data, exists, err
	}
	if c.listName, c.listData, c.listExists, err = read(c.spec.ListFile); err != nil {
		return err
	}
	asterisk := c.spec.ListKind != adapter.ListTextfile
	c.current = parsePluginList(decodePluginsTxt(c.listData), asterisk)
	if !c.listExists {
		c.current.eol = eolCRLF
	}
	if c.spec.OrderFile == "" {
		c.orderName = ""
		return nil
	}
	if c.orderName, c.orderData, c.orderExists, err = read(c.spec.OrderFile); err != nil {
		return err
	}
	if c.orderExists {
		// The textfile format: loadorder.txt is the order, Plugins.txt
		// the active set.
		active := map[string]bool{}
		for _, e := range c.current.entries {
			active[pluginKey(e.name)] = true
		}
		order := parsePluginList(decodeLoadOrderTxt(c.orderData), false)
		entries := make([]listEntry, 0, len(order.entries))
		for _, e := range order.entries {
			entries = append(entries, listEntry{name: e.name, active: active[pluginKey(e.name)]})
		}
		c.current.entries = entries
	}
	return nil
}

// mergedRanks is the final pass's tiebreak: the managed plugins in their
// sorted order, with each external plugin placed straight after the plugin
// it followed in the current file (the nearest earlier one lmm also
// places), and one that followed none at the front - so an external
// plugin keeps its place relative to its neighbours.
func mergedRanks(sorted, externals []pluginNode, current []listEntry) map[string]int {
	isExternal := map[string]bool{}
	for _, n := range externals {
		isExternal[pluginKey(n.name)] = true
	}
	isManaged := map[string]bool{}
	for _, n := range sorted {
		isManaged[pluginKey(n.name)] = true
	}
	after := map[string][]string{} // anchor key ("" = front) -> externals
	anchor := ""
	for _, e := range current {
		key := pluginKey(e.name)
		switch {
		case isManaged[key]:
			anchor = key
		case isExternal[key]:
			after[anchor] = append(after[anchor], key)
		}
	}
	ranks := map[string]int{}
	next := 0
	place := func(key string) {
		if _, done := ranks[key]; !done {
			ranks[key] = next
			next++
		}
	}
	for _, key := range after[""] {
		place(key)
	}
	for _, n := range sorted {
		key := pluginKey(n.name)
		place(key)
		for _, ext := range after[key] {
			place(ext)
		}
	}
	for _, n := range externals {
		place(pluginKey(n.name))
	}
	return ranks
}

// checkPluginList is the findings for a sorted list: active plugins whose
// masters or requirements are not active, and classes over their limits.
func checkPluginList(sorted []pluginNode, implicit []string, implicitlyActive map[string]bool, dirs pluginDirs, spec adapter.PluginSpec, rules domain.PluginRules) []PluginFinding {
	var out []PluginFinding
	active := map[string]bool{}
	for _, name := range implicit {
		active[pluginKey(name)] = true
	}
	for k := range implicitlyActive {
		active[k] = true
	}
	for _, n := range sorted {
		if n.active {
			active[pluginKey(n.name)] = true
		}
	}
	missingMsg := func(n pluginNode, missing []string, what string) string {
		var parts []string
		for _, m := range missing {
			if dirs.has(m) {
				parts = append(parts, m+" (installed, not active)")
			} else {
				parts = append(parts, m+" (not installed)")
			}
		}
		return fmt.Sprintf("%s needs %s %s", n.name, what, strings.Join(parts, ", "))
	}
	for _, n := range sorted {
		if !n.active {
			continue
		}
		var missing []string
		for _, m := range n.masters {
			if !active[pluginKey(m)] {
				missing = append(missing, m)
			}
		}
		if len(missing) > 0 {
			out = append(out, PluginFinding{Code: PluginFindingMissingMaster, Severity: PluginSeverityError, Plugin: n.name, Missing: missing,
				Message: missingMsg(n, missing, "the master(s)") + " - the game will not start with it active: install and activate them, or deactivate it"})
		}
	}
	for _, rule := range rules.Plugins {
		var n *pluginNode
		for i := range sorted {
			if pluginKey(sorted[i].name) == pluginKey(rule.Name) {
				n = &sorted[i]
			}
		}
		if n == nil || !n.active {
			continue
		}
		var missing []string
		for _, r := range rule.Req {
			if !active[pluginKey(r)] {
				missing = append(missing, r)
			}
		}
		if len(missing) > 0 {
			out = append(out, PluginFinding{Code: PluginFindingMissingRequirement, Severity: PluginSeverityWarning, Plugin: n.name, Missing: missing,
				Message: missingMsg(*n, missing, "(by a profile rule)")})
		}
	}

	var full, light, medium int
	count := func(c PluginClass) {
		switch {
		case c.Light:
			light++
		case c.Medium:
			medium++
		default:
			full++
		}
	}
	for _, name := range implicit {
		f := dirs.files[pluginKey(name)]
		h, _ := readHeaderAt(filepath.Join(f.dir, f.name), spec)
		count(ClassifyPlugin(f.name, h, spec))
	}
	for _, n := range sorted {
		if n.active {
			count(n.class)
		}
	}
	maxFull := spec.MaxFull
	if light > 0 {
		maxFull--
	}
	if medium > 0 {
		maxFull--
	}
	over := func(class string, count, limit int) {
		out = append(out, PluginFinding{Code: PluginFindingOverLimit, Severity: PluginSeverityError, Class: class, Count: count, Limit: limit,
			Message: fmt.Sprintf("%d %s plugins are active, but the game loads at most %d - deactivate %d of them", count, class, limit, count-limit)})
	}
	if spec.MaxFull > 0 && full > maxFull {
		over("full", full, maxFull)
	}
	if spec.MaxLight > 0 && light > spec.MaxLight {
		over("light", light, spec.MaxLight)
	}
	if spec.MaxMedium > 0 && medium > spec.MaxMedium {
		over("medium", medium, spec.MaxMedium)
	}
	return out
}

// writePluginList writes c's list (and order) file, capturing each one's
// original first. It reports whether any file changed.
func (s *Service) writePluginList(game *domain.Game, c *pluginComputation) (bool, error) {
	rendered := renderPluginList(c.spec, c.entries, c.implicit, c.current.header, c.current.eol, orderEOL(c))
	if !c.listExists && len(c.entries) == 0 {
		return false, nil // nothing to list, and no file to keep in step
	}
	dir, err := c.listDir.open(true)
	if err != nil {
		return false, err
	}
	defer func() { _ = dir.Close() }()
	store := s.prefixOriginalsFor(game.ID)
	changed := false
	write := func(name string, content []byte) error {
		if sameContentAt(dir, name, content) {
			return nil
		}
		data, perm, exists, err := readAt(dir, name)
		if err != nil {
			return err
		}
		if store != nil {
			row := prefixOriginal{Prefix: c.prefix.Path, User: c.prefix.User, Path: userRelative(c.prefix, dir.Name(), name), Existed: exists, Mode: uint32(perm)}
			if err := store.capture(row, data); err != nil {
				return fmt.Errorf("keeping a copy of %s before replacing it: %w", name, err)
			}
		}
		if perm == 0 {
			perm = 0o644
		}
		if err := replaceAt(dir, name, content, perm); err != nil {
			return err
		}
		changed = true
		return nil
	}
	if err := write(c.listName, rendered.list); err != nil {
		return changed, err
	}
	if c.orderName != "" {
		if err := write(c.orderName, rendered.order); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// orderEOL is the order file's line ending: its own, else the list file's.
func orderEOL(c *pluginComputation) string {
	if c.orderExists {
		return lineEndingOf(c.orderData)
	}
	return c.current.eol
}

// userRelative is dir/name relative to st's user directory, in slash form:
// the path a prefix original records, as it is spelled on disk.
func userRelative(st *PrefixStatus, dir, name string) string {
	user := filepath.Join(st.Path, "drive_c", "users", st.User)
	if real, err := filepath.EvalSymlinks(user); err == nil {
		user = real
	}
	rel, err := filepath.Rel(user, filepath.Join(dir, name))
	if err != nil {
		return name
	}
	return filepath.ToSlash(rel)
}

// restorePluginList puts back the list files game had before lmm first
// wrote them - `lmm purge`'s last step (#616). A file lmm created is
// removed. One plugin the current list names that lmm did not deploy and
// that the original does not name - a Creation bought in-game since - is
// kept, active as it is, after the plugin it followed: lmm never
// deactivates a plugin it did not deploy, purge included. nil when lmm
// never wrote the game's list.
func (s *Service) restorePluginList(game *domain.Game) *PluginListResult {
	store := s.prefixOriginalsFor(game.ID)
	if store == nil {
		return nil
	}
	rows, err := store.list()
	if err != nil {
		return &PluginListResult{Status: PluginListSkipped, Findings: []PluginFinding{unrestorable(err)}}
	}
	if len(rows) == 0 {
		return nil
	}
	spec, managed := s.pluginRules(game)
	var dirs pluginDirs
	if managed {
		st := s.ResolvePrefix(game)
		dirs = scanPluginDirs(game, spec, st, appDataFolder(game, spec))
	}
	res := &PluginListResult{Status: PluginListRestored}
	removedAll := true
	for _, row := range rows {
		listFile := managed && spec.ListKind == adapter.ListAsterisk && strings.EqualFold(path.Base(row.Path), spec.ListFile)
		status, file, err := s.restorePrefixFile(store, row, listFile, dirs)
		if err != nil {
			res.Status = PluginListSkipped
			res.Findings = append(res.Findings, unrestorable(err))
			continue
		}
		if status != PluginListRemoved {
			removedAll = false
		}
		if res.ListFile == "" || strings.EqualFold(path.Base(row.Path), spec.ListFile) {
			res.ListFile = file
		}
	}
	if res.Status == PluginListRestored && removedAll {
		res.Status = PluginListRemoved
	}
	return res
}

// unrestorable is the finding for an original lmm could not put back.
func unrestorable(err error) PluginFinding {
	return PluginFinding{Code: PluginFindingUnwritable, Severity: PluginSeverityError,
		Message: fmt.Sprintf("the plugin list lmm replaced was not put back: %v", err)}
}

// restorePrefixFile puts row's file back and forgets it. listFile says the
// file is an asterisk-format list, whose external plugins (in dirs) are
// kept. It answers PluginListRestored or PluginListRemoved, and the path.
func (s *Service) restorePrefixFile(store *prefixOriginals, row prefixOriginal, listFile bool, dirs pluginDirs) (string, string, error) {
	dirRel, name := path.Split(row.Path)
	p := prefixPath{prefix: row.Prefix, user: row.User, comps: strings.FieldsFunc(dirRel, func(r rune) bool { return r == '/' })}
	original, err := store.stored(row)
	if err != nil {
		return "", "", err
	}
	dir, err := p.open(row.Existed)
	if errors.Is(err, os.ErrNotExist) && !row.Existed {
		return PluginListRemoved, p.display(), store.forget(row)
	}
	if err != nil {
		return "", "", err
	}
	defer func() { _ = dir.Close() }()
	file := filepath.Join(dir.Name(), name)
	current, _, exists, err := readAt(dir, name)
	if err != nil {
		return "", "", err
	}
	content := original
	if listFile && exists {
		content = keepExternals(original, current, row.Existed, dirs)
	}
	status := PluginListRestored
	switch {
	case content == nil && !row.Existed:
		if err := removeAt(dir, name); err != nil {
			return "", "", err
		}
		status = PluginListRemoved
	default:
		perm := os.FileMode(row.Mode).Perm()
		if perm == 0 {
			perm = 0o644
		}
		if !sameContentAt(dir, name, content) {
			if err := replaceAt(dir, name, content, perm); err != nil {
				return "", "", err
			}
		}
	}
	return status, file, store.forget(row)
}

// keepExternals is the original list with every plugin current activates
// or names that the original does not, and that is installed (dirs), put
// after the plugin it follows in current - nil when there are none and the
// original did not exist (the file is removed then).
func keepExternals(original, current []byte, existed bool, dirs pluginDirs) []byte {
	base := parsePluginList(decodePluginsTxt(original), true)
	if !existed {
		base = pluginListFile{eol: lineEndingOf(current)}
	}
	now := parsePluginList(decodePluginsTxt(current), true)
	inBase := map[string]bool{}
	for _, e := range base.entries {
		inBase[pluginKey(e.name)] = true
	}
	var extra []pluginNode
	for _, e := range now.entries {
		if !inBase[pluginKey(e.name)] && dirs.has(e.name) {
			extra = append(extra, pluginNode{name: e.name, active: e.active})
		}
	}
	if len(extra) == 0 {
		if !existed {
			return nil
		}
		return original
	}
	nodes := make([]pluginNode, len(base.entries))
	for i, e := range base.entries {
		nodes[i] = pluginNode{name: e.name, active: e.active}
	}
	ranks := mergedRanks(nodes, extra, now.entries)
	all := append(nodes, extra...)
	slices.SortStableFunc(all, func(a, b pluginNode) int { return ranks[pluginKey(a.name)] - ranks[pluginKey(b.name)] })
	entries := make([]listEntry, len(all))
	for i, n := range all {
		entries[i] = listEntry{name: n.name, active: n.active}
	}
	header := base.header
	if !existed {
		header = now.header
	}
	return renderPluginList(adapter.PluginSpec{ListKind: adapter.ListAsterisk}, entries, nil, header, base.eol, "").list
}

// wrotePluginList reports whether lmm has written game's plugin list - the
// prefix originals keep a row for every list file lmm replaced or created
// until a purge hands it back. A list lmm never wrote, for a profile that
// deploys no plugin, is left alone.
func (s *Service) wrotePluginList(game *domain.Game) bool {
	store := s.prefixOriginalsFor(game.ID)
	if store == nil {
		return false
	}
	rows, err := store.list()
	return err != nil || len(rows) > 0
}
