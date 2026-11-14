package core

import (
	"bytes"
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
	// current is the list file as read; currentOrder the order file, for
	// its comment lines and line ending.
	current      pluginListFile
	currentOrder pluginListFile
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
	// A list the game saved while lmm worked out the order is read again,
	// once; a second change in that time leaves the list to the game.
	var c *pluginComputation
	var res *PluginListResult
	var written bool
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		c, err = s.computePluginList(ctx, game, profileName, spec)
		if err != nil {
			return &PluginListResult{Status: PluginListSkipped, Findings: []PluginFinding{unwritable(err)}}
		}
		if c.nothing {
			return nil
		}
		res = c.result()
		if c.skipped {
			res.Status = PluginListSkipped
			return res
		}
		written, err = s.writePluginList(game, c)
		if !errors.Is(err, errPluginListChanged) {
			break
		}
	}
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
// extra directories that resolve. A directory that cannot be read - a
// mod_path on a drive that is not mounted, say - is an error, never an
// empty directory: every plugin would look uninstalled, and the write
// would drop them all from the list (#616 review B1). An extra directory
// that does not exist is no error: Starfield's My Games/Data is optional.
func scanPluginDirs(game *domain.Game, spec adapter.PluginSpec, st *PrefixStatus, folder string) (pluginDirs, error) {
	out := pluginDirs{files: map[string]pluginFile{}}
	add := func(dir string) error {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("reading the plugin folder %s: %w", dir, err)
		}
		for _, e := range entries {
			if !hasPluginExt(e.Name(), spec) {
				continue
			}
			if _, seen := out.files[pluginKey(e.Name())]; !seen {
				out.files[pluginKey(e.Name())] = pluginFile{name: e.Name(), dir: dir}
			}
		}
		return nil
	}
	if err := add(game.ModPath); err != nil {
		return out, err
	}
	for _, extra := range spec.ExtraPluginDirs {
		if dir := rootedDir(game, extra, st, folder); dir != "" {
			if err := add(dir); err != nil {
				return out, err
			}
		}
	}
	return out, nil
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

	dirs, scanErr := scanPluginDirs(game, spec, st, folder)
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
	deployedPlugins := 0
	for _, row := range rows {
		rel := filepath.ToSlash(row.Path.Rel)
		if row.Path.IsGameRoot() || strings.Contains(rel, "/") || !hasPluginExt(rel, spec) {
			continue
		}
		if row.ModPath != "" && filepath.Clean(row.ModPath) != filepath.Clean(game.ModPath) {
			continue
		}
		key := pluginKey(rel)
		if implicitKeys[key] {
			continue
		}
		deployedPlugins++
		if !dirs.has(rel) {
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
	if scanErr != nil {
		c.skipped = true
		c.findings = append(c.findings, PluginFinding{Code: PluginFindingUnwritable, Severity: PluginSeverityError,
			Message: fmt.Sprintf("the plugin list was not written, because lmm could not read the folder the game loads plugins from (%v) - is its drive mounted?", scanErr)})
		return c, nil
	}
	if deployedPlugins > 0 && len(modOf) == 0 {
		// The database says this profile deploys plugins, and not one of
		// them is in the plugin folder: that is not a profile without
		// plugins but a folder that is not the game's (an empty mount
		// point), and writing would drop every plugin from the list.
		c.skipped = true
		c.findings = append(c.findings, PluginFinding{Code: PluginFindingUnwritable, Severity: PluginSeverityError,
			Message: fmt.Sprintf("the plugin list was not written, because none of the %d plugin(s) this profile deploys is in %s - is its drive mounted? (lmm deploy puts them back)", deployedPlugins, game.ModPath)})
		return c, nil
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
	if len(spec.Implicit) > 0 && len(implicit) == 0 && len(modOf) == 0 && len(c.current.entries) > 0 {
		// The game's own masters (Skyrim.esm, ...) are always in its plugin
		// folder; a folder with neither one of them nor a plugin of lmm's is
		// not the game's - an empty mount point - and every plugin the list
		// names would look uninstalled.
		c.skipped = true
		c.findings = append(c.findings, PluginFinding{Code: PluginFindingUnwritable, Severity: PluginSeverityError,
			Message: fmt.Sprintf("the plugin list was not written, because %s holds none of the game's own plugins (%s) - is its drive mounted?", game.ModPath, spec.Implicit[0])})
		return c, nil
	}

	enabled := make(map[string]bool, len(profile.Plugins.Order))
	for _, e := range profile.Plugins.Order {
		if _, seen := enabled[pluginKey(e.Name)]; !seen {
			enabled[pluginKey(e.Name)] = e.Enabled
		}
	}
	var nodes []pluginNode
	var externals []pluginNode
	// addNode adds the plugin spelled name: a managed one as its file is
	// named, an external one as the list spells it, byte for byte (#616
	// review 8).
	addNode := func(name string, managed bool, mod int, active bool) {
		f := dirs.files[pluginKey(name)]
		spelled := f.name
		if !managed {
			spelled = name
		}
		node := pluginNode{name: spelled, managed: managed, modIndex: mod, active: active}
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
		if managed && !listableName(f.name) || !managed && hasControl(name) {
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
		c.current = pluginListFile{eol: eolNew}
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
		c.current.eol = eolNew
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
		c.currentOrder = order
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

// errPluginListChanged is writePluginList's refusal when the list file is
// no longer what the computation read: the game, or another tool, saved it
// in between (#616 review 3).
var errPluginListChanged = errors.New("the plugin list changed while lmm was working out its order")

// pluginListBeforeWrite is a test seam: called just before writePluginList
// re-reads the files it is about to replace. nil in production.
var pluginListBeforeWrite func()

// writePluginList writes c's list (and order) file, capturing each one's
// original first. It reports whether any file changed. It re-reads each
// file immediately before replacing it, and refuses with
// errPluginListChanged when that is not what c was computed from - so a
// save the game's Creations menu made in between is never written over.
func (s *Service) writePluginList(game *domain.Game, c *pluginComputation) (bool, error) {
	rendered := renderPluginList(c.spec, c.entries, c.implicit, c.current, c.currentOrder, orderEOL(c))
	if !c.listExists && len(c.entries) == 0 {
		return false, nil // nothing to list, and no file to keep in step
	}
	if pluginListBeforeWrite != nil {
		pluginListBeforeWrite()
	}
	dir, err := c.listDir.open(true)
	if err != nil {
		return false, err
	}
	defer func() { _ = dir.Close() }()
	store := s.prefixOriginalsFor(game.ID)
	owned := c.ownedNames()
	changed := false
	write := func(name string, content, readBefore []byte, existedBefore bool) error {
		data, perm, exists, err := readAt(dir, name)
		if err != nil {
			return err
		}
		if exists != existedBefore || !bytes.Equal(data, readBefore) {
			return errPluginListChanged
		}
		rel := userRelative(c.prefix, dir.Name(), name)
		if !bytes.Equal(data, content) || !exists {
			if store != nil {
				row := prefixOriginal{Prefix: c.prefix.Path, User: c.prefix.User, Path: rel, Existed: exists, Mode: uint32(perm)}
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
		}
		if store != nil {
			if err := store.setOwned(c.prefix.Path, c.prefix.User, rel, owned); err != nil {
				return fmt.Errorf("recording which plugins in %s are lmm's: %w", name, err)
			}
		}
		return nil
	}
	if err := write(c.listName, rendered.list, c.listData, c.listExists); err != nil {
		return changed, err
	}
	if c.orderName != "" {
		if err := write(c.orderName, rendered.order, c.orderData, c.orderExists); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// ownedNames is the plugins c lists that lmm deploys, sorted: what a purge
// takes out of the list again.
func (c *pluginComputation) ownedNames() []string {
	var out []string
	for _, e := range c.entries {
		if c.managed[pluginKey(e.name)] {
			out = append(out, e.name)
		}
	}
	slices.Sort(out)
	return out
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

// restoreKind is how a purge reads and writes one kept list file.
type restoreKind int

const (
	// restoreBytes puts the kept bytes back as they are: a file lmm cannot
	// read as a list (the game's adapter is no longer one that has one).
	restoreBytes restoreKind = iota
	// restoreAsterisk is an asterisk-format Plugins.txt.
	restoreAsterisk
	// restorePlain1252 is a textfile-format Plugins.txt: every line an
	// active plugin, Windows-1252.
	restorePlain1252
	// restorePlainUTF8 is a textfile-format loadorder.txt, UTF-8.
	restorePlainUTF8
)

// restoreKindOf is how to restore row's file for a game with spec.
func restoreKindOf(row prefixOriginal, spec adapter.PluginSpec, managed bool) restoreKind {
	if !managed {
		return restoreBytes
	}
	name := path.Base(row.Path)
	switch {
	case spec.OrderFile != "" && strings.EqualFold(name, spec.OrderFile):
		return restorePlainUTF8
	case strings.EqualFold(name, spec.ListFile) && spec.ListKind == adapter.ListTextfile:
		return restorePlain1252
	case strings.EqualFold(name, spec.ListFile):
		return restoreAsterisk
	}
	return restoreBytes
}

// restorePluginList puts back the list files game had before lmm first
// wrote them - `lmm purge`'s step (#616). The list it writes is the CURRENT
// file without the plugins lmm deployed there, plus the entries only the
// kept file has, each after the plugin it followed: so a plugin the user
// (or the game's Creations menu) added, turned on, turned off or moved
// since lmm's first write stays as they left it - lmm never deactivates a
// plugin it did not deploy, purge included (#616 review B2). When nothing
// outside lmm changed, the kept file comes back byte for byte; a file lmm
// created and nothing else uses is removed. nil when lmm never wrote the
// game's list.
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
	st := s.ResolvePrefix(game)
	res := &PluginListResult{Status: PluginListRestored}
	removedAll, restoredAny := true, false
	for _, row := range rows {
		row, moved, note, skip, err := rekeyPrefixOriginal(store, rows, row, st)
		if err != nil {
			res.Findings = append(res.Findings, unrestorable(err))
			continue
		}
		if note != "" {
			res.Findings = append(res.Findings, PluginFinding{Code: PluginFindingUnwritable, Severity: PluginSeverityWarning, Message: note})
		}
		if skip {
			continue
		}
		// Into a prefix the game moved to, an entry only the kept file has
		// is put back switched off unless its plugin is installed: the
		// new prefix's game may not have it at all.
		var present func(string) bool
		if moved {
			present = func(string) bool { return false }
			if managed {
				if dirs, err := scanPluginDirs(game, spec, st, appDataFolder(game, spec)); err == nil {
					present = dirs.has
				}
			}
		}
		status, file, err := s.restorePrefixFile(store, row, restoreKindOf(row, spec, managed), present)
		if err != nil {
			res.Findings = append(res.Findings, unrestorable(err))
			continue
		}
		restoredAny = true
		if status != PluginListRemoved {
			removedAll = false
		}
		if res.ListFile == "" || strings.EqualFold(path.Base(row.Path), spec.ListFile) {
			res.ListFile = file
		}
	}
	switch {
	case !restoredAny:
		res.Status = PluginListSkipped
	case removedAll:
		res.Status = PluginListRemoved
	}
	return res
}

// rekeyPrefixOriginal answers for a kept file whose prefix does not exist
// now (#616 review 10). moved reports a row re-targeted into another
// prefix; note, when set, is the finding to report, and skip says the row
// is not restored by this purge.
//
//   - The game resolves to a DIFFERENT prefix that exists: the game moved
//     there. The store is the game's own, so the copy is this game's list,
//     and it is restored into that prefix - unless that prefix has a kept
//     file of its own at the same path, which is then the one to restore,
//     and this stale copy is dropped.
//   - Otherwise - the prefix cannot be resolved, or resolves to the same
//     missing path - the prefix may only be away for now (a Steam library
//     on a drive that is unplugged), so the row is KEPT, with a finding,
//     and the next purge tries again. Dropping it would lose the
//     pre-lmm list for good (#616 re-review 1).
func rekeyPrefixOriginal(store *prefixOriginals, rows []prefixOriginal, row prefixOriginal, st *PrefixStatus) (out prefixOriginal, moved bool, note string, skip bool, err error) {
	if _, err := os.Stat(row.Prefix); !errors.Is(err, os.ErrNotExist) {
		return row, false, "", false, nil
	}
	if st == nil || !st.Resolved || filepath.Clean(st.Path) == filepath.Clean(row.Prefix) {
		return row, false, fmt.Sprintf("the prefix %s, where lmm keeps the game's original %s, cannot be found now (is its drive mounted?); lmm keeps the copy, and the next purge puts it back", row.Prefix, path.Base(row.Path)), true, nil
	}
	target := row
	target.Prefix, target.User = st.Path, st.User
	for _, other := range rows {
		if other.key() == target.key() {
			return row, false, fmt.Sprintf("the prefix %s is gone; lmm keeps the list from the prefix the game uses now, %s, and dropped the older copy", row.Prefix, st.Path), true, store.forget(row)
		}
	}
	return target, true, "", false, store.rekey(row, target)
}

// unrestorable is the finding for an original lmm could not put back.
func unrestorable(err error) PluginFinding {
	return PluginFinding{Code: PluginFindingUnwritable, Severity: PluginSeverityError,
		Message: fmt.Sprintf("the plugin list lmm replaced was not put back: %v", err)}
}

// restorePrefixFile puts row's file back (restorePluginList) and forgets
// it. It answers PluginListRestored or PluginListRemoved, and the path.
func (s *Service) restorePrefixFile(store *prefixOriginals, row prefixOriginal, kind restoreKind, present func(string) bool) (string, string, error) {
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
	if kind != restoreBytes && (exists || present != nil) {
		content = mergeRestored(original, current, row.Existed, row.Owned, kind, present)
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

// mergeRestored is the list a purge writes: current without owned (the
// plugins lmm deployed in it), plus every entry of the kept original that
// current - lmm's plugins aside - no longer has, after the plugin it
// followed there. The original's own bytes when that is exactly the
// original's list (nothing outside lmm changed); nil when the original did
// not exist and nothing is left to list.
func mergeRestored(original, current []byte, existed bool, owned []string, kind restoreKind, present func(string) bool) []byte {
	decode, asterisk := decodePluginsTxt, kind == restoreAsterisk
	if kind == restorePlainUTF8 {
		decode = decodeLoadOrderTxt
	}
	now := parsePluginList(decode(current), asterisk)
	base := pluginListFile{eol: now.eol}
	if existed {
		base = parsePluginList(decode(original), asterisk)
	}
	isOwned := map[string]bool{}
	for _, n := range owned {
		isOwned[pluginKey(n)] = true
	}
	var keep []pluginNode
	stillThere := map[string]bool{}
	for _, e := range now.entries {
		if !isOwned[pluginKey(e.name)] {
			keep = append(keep, pluginNode{name: e.name, active: e.active})
			stillThere[pluginKey(e.name)] = true
		}
	}
	var only []pluginNode
	for _, e := range base.entries {
		if !stillThere[pluginKey(e.name)] {
			// present, when set, is a moved prefix's plugin folder: an
			// entry whose plugin is not there goes back switched off.
			only = append(only, pluginNode{name: e.name, active: e.active && (present == nil || present(e.name))})
		}
	}
	ranks := mergedRanks(keep, only, base.entries)
	all := append(slices.Clone(keep), only...)
	slices.SortStableFunc(all, func(a, b pluginNode) int { return ranks[pluginKey(a.name)] - ranks[pluginKey(b.name)] })
	entries := make([]listEntry, len(all))
	for i, n := range all {
		entries[i] = listEntry{name: n.name, active: n.active}
	}
	if existed && slices.Equal(entries, base.entries) {
		return original
	}
	if !existed && len(entries) == 0 {
		return nil
	}
	format := now
	if len(current) == 0 && existed {
		format = base // no file there now: the kept one's comments and endings
	}
	return renderLines(format, entries, asterisk, kind == restorePlainUTF8)
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

// pluginListSynced is the outcome of a plugin step a flow ran before its
// install.after_all hook, carried on ctx to that flow's syncMergedPak.
type pluginListSynced struct {
	gameID, profile string
	result          *PluginListResult
}

type pluginListSyncedKey struct{}

// syncPluginListBeforeHooks runs the plugin step now - before the flow's
// install.after_all hook, so the hook sees the final Plugins.txt and can
// adjust it, rather than have lmm write over its changes afterwards (#616)
// - and returns ctx carrying the outcome, which the flow's choke-point
// syncMergedPak reports instead of writing the list a second time.
func (s *Service) syncPluginListBeforeHooks(ctx context.Context, game *domain.Game, profileName string) context.Context {
	res := s.syncPluginList(ctx, game, profileName)
	return context.WithValue(ctx, pluginListSyncedKey{}, &pluginListSynced{gameID: game.ID, profile: profileName, result: res})
}

// pluginListSyncedOn is the outcome syncPluginListBeforeHooks put on ctx
// for this game and profile, if it ran.
func pluginListSyncedOn(ctx context.Context, gameID, profileName string) (*PluginListResult, bool) {
	done, ok := ctx.Value(pluginListSyncedKey{}).(*pluginListSynced)
	if !ok || done == nil || done.gameID != gameID || done.profile != profileName {
		return nil, false
	}
	return done.result, true
}

// hasControl reports whether name holds a C0 control character or DEL -
// one that would break a list line. An external plugin's name came from the
// list itself, so it is kept even when it holds a byte Windows-1252 leaves
// undefined (it is written back exactly as read).
func hasControl(name string) bool {
	return strings.ContainsFunc(name, func(r rune) bool { return r < 0x20 || r == 0x7F })
}
