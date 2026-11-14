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

// The file load-order sync (#620, design docs/plans/2026-11-10-collections-
// design.md §3 `file_load_order:<id>`): for a game whose adapter is an
// adapter.FileLoadOrderer (Cyberpunk 2077's archives), core writes the
// order of the files the profile deploys to the game's list file
// (archive/pc/mod/modlist.txt) after every deploying flow. It runs where
// the plugin list does (syncPluginList): at syncMergedPak's choke point, or
// before a flow's install.after_all hook.
//
// It shares P3's rules (#616) where they apply:
//
//   - The profile's load_orders: list is the user's INTENT. The effective
//     order is worked out from it and the files deployed, and never
//     written back to the profile.
//   - OWNERSHIP. lmm writes the whole list, re-reading the file before
//     every write. A file the list names that lmm did not deploy, and that
//     is in the directory, is EXTERNAL: it keeps its place relative to its
//     neighbours. lmm never adds an external file to the list - for the
//     game, a file the list does not name loads after the listed ones, in
//     its own order - and says so (LoadOrderFindingUnlisted).
//   - A file lmm deploys that the profile does not order yet goes in at its
//     place in the game's default (byte-wise alphabetical) order, beside
//     the file it sorts after: where the game would load it without a
//     list, and where Vortex's Cyberpunk extension puts it.
//   - The file lmm first replaced is kept (loadOrderOriginalsFor), and
//     purge puts it back without undoing what the user changed since.
//
// The list is opt-in: lmm writes it only for a profile whose load_orders:
// orders something for that id, or one lmm has written before. A profile
// with no order (a reset, or a switch to one without) hands the list back,
// as a purge does, so the game's own order applies again.

// LoadOrderFindingUnwritable, LoadOrderFindingDeployed,
// LoadOrderFindingUnlisted and LoadOrderFindingUnencodable are
// LoadOrderFinding.Code's values.
const (
	// LoadOrderFindingUnwritable: the list could not be read or written (a
	// link where the file is, a permission error, a folder that is not
	// there); nothing was written.
	LoadOrderFindingUnwritable = "load_order_unwritable"
	// LoadOrderFindingDeployed: a mod deploys the list file itself, so lmm
	// does not write over it.
	LoadOrderFindingDeployed = "load_order_list_deployed"
	// LoadOrderFindingUnlisted: files lmm did not deploy are in the folder
	// and not in the list; the game loads them after the listed ones.
	LoadOrderFindingUnlisted = "load_order_unlisted"
	// LoadOrderFindingUnencodable: a file's name cannot be a line of the
	// list (it holds a control character); it is left out.
	LoadOrderFindingUnencodable = "load_order_name_unencodable"
)

// LoadOrderFinding is one thing a file load-order sync found. Severity is
// PluginSeverityError or PluginSeverityWarning, as a plugin finding's is.
type LoadOrderFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	// File is the file it is about, when it is about one.
	File string `json:"file,omitempty"`
	// Files names the files it is about, when it is about several
	// (LoadOrderFindingUnlisted).
	Files []string `json:"files,omitempty"`
	// Message is the finding in words, with what to do.
	Message string `json:"message"`
}

// FileLoadOrderResult is what a flow did to one of the game's file
// load-order lists (#620): DeployResult.LoadOrders, PurgeResult.LoadOrders.
type FileLoadOrderResult struct {
	// ID is the load order's id ("cyberpunk2077-archive").
	ID string `json:"id"`
	// Status is PluginListWritten, PluginListUnchanged, PluginListSkipped,
	// PluginListRestored or PluginListRemoved, as a plugin list's is.
	Status string `json:"status"`
	// ListFile is the list file's path.
	ListFile string `json:"list_file,omitempty"`
	// Files is how many files the list names, and External how many of
	// those lmm did not deploy and kept where they were.
	Files    int `json:"files"`
	External int `json:"external,omitzero"`
	// Findings are the problems found, errors first.
	Findings []LoadOrderFinding `json:"findings,omitempty"`
}

// HasErrors reports whether any of r's findings is an error.
func (r FileLoadOrderResult) HasErrors() bool {
	return slices.ContainsFunc(r.Findings, func(f LoadOrderFinding) bool { return f.Severity == PluginSeverityError })
}

// loadOrderWarnings are rs's findings as the sentences a flow's Warnings
// carry.
func loadOrderWarnings(rs []FileLoadOrderResult) []string {
	var out []string
	for _, r := range rs {
		for _, f := range r.Findings {
			out = append(out, "load order: "+f.Message)
		}
	}
	return out
}

// loadOrderSpecs is game's file load orders, when its adapter has any.
func (s *Service) loadOrderSpecs(game *domain.Game) []adapter.FileLoadOrderSpec {
	a, err := s.AdapterFor(game)
	if err != nil {
		return nil
	}
	return adapter.FileLoadOrders(a, game)
}

// loadOrderSpec is game's file load order id, if its adapter has it.
func (s *Service) loadOrderSpec(game *domain.Game, id string) (adapter.FileLoadOrderSpec, bool) {
	a, err := s.AdapterFor(game)
	if err != nil {
		return adapter.FileLoadOrderSpec{}, false
	}
	return adapter.FileLoadOrder(a, game, id)
}

// loadOrderOriginalsFor is gameID's store of the list files lmm replaced:
// the prefix originals' store, under a manifest of its own
// (originals-loadorders.json), which no older binary opens. A row's Prefix
// is the game's install_path, its User empty, and its Path the list file's
// path under the install_path, as spelled on disk.
func (s *Service) loadOrderOriginalsFor(gameID string) *prefixOriginals {
	p := s.prefixOriginalsFor(gameID)
	if p == nil {
		return nil
	}
	p.file, p.sub = "originals-loadorders.json", "loadorders"
	return p
}

// loadOrderComputation is everything one load order's sync knows before
// it writes.
type loadOrderComputation struct {
	spec adapter.FileLoadOrderSpec
	// dirRel is the list's directory under the install_path, as spelled on
	// disk once found (spec.Dir.Path until then); listName the list file's
	// name to write (an existing file's spelling, else spec.ListFile).
	dirRel, listName string
	listPath         string
	// listData is the file as read, and listExists whether it existed.
	listData   []byte
	listExists bool
	current    []string
	// onDisk maps a file key to its name in the directory.
	onDisk map[string]string
	// managed maps the key of each file lmm deploys there to the index of
	// its mod in the profile (-1 when the profile does not list it).
	managed map[string]int
	// order is the profile's list used; entries the effective order;
	// placement why each entry is where it is (by key).
	order     []string
	entries   []string
	placement map[string]string
	unlisted  []string
	external  int
	findings  []LoadOrderFinding
	// wrote reports that lmm keeps an original for this list: it has
	// written it, and it is lmm's to keep in step or hand back.
	wrote bool
	// skipped: nothing may be written, Findings say why. nothing: there is
	// nothing to do and nothing to say. restore: the profile orders
	// nothing, so a list lmm wrote goes back to the user's.
	skipped, nothing, restore bool
}

// Placement values: why a file is where it is in the effective order.
const (
	// LoadOrderPlacedOrder: the profile's load order puts it there.
	LoadOrderPlacedOrder = "order"
	// LoadOrderPlacedDefault: the profile does not order it yet; it is at
	// its place in the game's own (alphabetical) order.
	LoadOrderPlacedDefault = "default"
	// LoadOrderPlacedExternal: lmm did not deploy it; it keeps the place
	// the list gave it.
	LoadOrderPlacedExternal = "external"
)

// loadOrderKey is a file's identity in a load order: its name, compared
// case-insensitively, as Windows compares it.
func loadOrderKey(name string) string { return strings.ToLower(name) }

// computeLoadOrder works out spec's effective order for profileName.
// order, when non-nil, stands in for the profile's list (an edit's
// proposal); report keeps going where a sync would stop with nothing to
// write, so a report can still show lmm's own order.
func (s *Service) computeLoadOrder(ctx context.Context, game *domain.Game, profileName string, spec adapter.FileLoadOrderSpec, order []string, report bool) (*loadOrderComputation, error) {
	c := &loadOrderComputation{spec: spec, dirRel: spec.Dir.Path, listName: spec.ListFile, onDisk: map[string]string{}, managed: map[string]int{}, placement: map[string]string{}}
	profile, _ := config.LoadProfile(s.configDir, game.ID, profileName)
	if order == nil && profile != nil {
		order = profile.LoadOrders[spec.ID]
	}
	c.order = order
	if game.InstallPath == "" {
		c.nothing = true
		return c, nil
	}
	c.listPath = filepath.Join(game.InstallPath, filepath.FromSlash(spec.Dir.Path), spec.ListFile)
	if store := s.loadOrderOriginalsFor(game.ID); store != nil {
		rows, err := store.list()
		if err != nil {
			return nil, err
		}
		c.wrote = slices.ContainsFunc(rows, func(r prefixOriginal) bool { return c.ownsRow(game, r) })
	}

	modIndex := map[string]int{}
	if profile != nil {
		for i, m := range profile.Mods {
			modIndex[domain.ModKey(m.SourceID, m.ModID)] = i
		}
	}
	rows, err := s.db.ListDeployedFiles(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("reading the profile's deployed files: %w", err)
	}
	deployedKeys := map[string]int{}
	listDeployed := false
	for _, row := range rows {
		if row.ModPath != "" && filepath.Clean(row.ModPath) != filepath.Clean(game.ModPath) {
			continue
		}
		rel, ok := relUnder(game.InstallPath, deployTarget(game, row.Path))
		if !ok {
			continue
		}
		dir, name := path.Split(rel)
		if !strings.EqualFold(strings.TrimSuffix(dir, "/"), spec.Dir.Path) {
			continue
		}
		if strings.EqualFold(name, spec.ListFile) {
			listDeployed = true
			continue
		}
		if !spec.Covers(name) {
			continue
		}
		if _, seen := deployedKeys[loadOrderKey(name)]; seen {
			continue
		}
		idx, ok := modIndex[domain.ModKey(row.SourceID, row.ModID)]
		if !ok {
			idx = -1
		}
		deployedKeys[loadOrderKey(name)] = idx
	}

	dir, err := openGameDir(game.InstallPath, spec.Dir.Path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if len(deployedKeys) > 0 {
			c.skip(fmt.Sprintf("the %s list was not written, because %s, where this profile deploys %d file(s), is not there - is its drive mounted? (lmm deploy puts them back)",
				spec.Label, filepath.Join(game.InstallPath, spec.Dir.Path), len(deployedKeys)))
			return c, nil
		}
		c.nothing = !c.wrote
		c.restore = c.wrote
		return c, nil
	case err != nil:
		c.skip(fmt.Sprintf("the %s list was not written: %v", spec.Label, err))
		return c, nil
	}
	defer func() { _ = dir.Close() }()
	c.dirRel = relOrSelf(game.InstallPath, dir.Name(), spec.Dir.Path)
	names, err := dirNames(dir)
	if err != nil {
		c.skip(fmt.Sprintf("the %s list was not written, because lmm could not read %s: %v", spec.Label, dir.Name(), err))
		return c, nil
	}
	for _, n := range names {
		if !spec.Covers(n) {
			continue
		}
		if st, exists, err := lstatAt(dir, n); err != nil || !exists || !isRegularOrLink(st) {
			continue
		}
		if _, seen := c.onDisk[loadOrderKey(n)]; !seen {
			c.onDisk[loadOrderKey(n)] = n
		}
	}
	for key, idx := range deployedKeys {
		if _, ok := c.onDisk[key]; ok {
			c.managed[key] = idx
		}
	}
	if len(deployedKeys) > 0 && len(c.managed) == 0 {
		// The database says this profile deploys files here, and not one of
		// them is there: that is a folder that is not the game's (an empty
		// mount point), and writing would drop every one of them.
		c.skip(fmt.Sprintf("the %s list was not written, because none of the %d file(s) this profile deploys is in %s - is its drive mounted? (lmm deploy puts them back)",
			spec.Label, len(deployedKeys), dir.Name()))
		return c, nil
	}
	if listDeployed {
		c.findings = append(c.findings, LoadOrderFinding{Code: LoadOrderFindingDeployed, Severity: PluginSeverityError, File: spec.ListFile,
			Message: fmt.Sprintf("a mod this profile deploys ships its own %s, so lmm does not write the %s order over it - disable that file, or the mod, to let lmm write it", spec.ListFile, spec.Label)})
		c.skipped = true
		if !report {
			return c, nil
		}
	}

	if real, err := entryFold(dir, spec.ListFile); err != nil {
		c.skip(fmt.Sprintf("the %s list was not written: %v", spec.Label, err))
		return c, nil
	} else if real != "" {
		c.listName = real
	}
	c.listPath = filepath.Join(dir.Name(), c.listName)
	data, _, exists, err := readAt(dir, c.listName)
	if err != nil {
		c.skip(fmt.Sprintf("the %s list was not written: %v", spec.Label, err))
		return c, nil
	}
	c.listData, c.listExists = data, exists
	c.current = parseLoadOrderList(data)

	if len(order) == 0 && !report && !c.skipped {
		// The profile orders nothing: lmm leaves the list to the game, and
		// hands back one it wrote.
		c.nothing = !c.wrote
		c.restore = c.wrote
		return c, nil
	}
	c.place()
	return c, nil
}

// skip marks c as unwritable, with message as its finding.
func (c *loadOrderComputation) skip(message string) {
	c.skipped = true
	c.findings = append(c.findings, LoadOrderFinding{Code: LoadOrderFindingUnwritable, Severity: PluginSeverityError, Message: message})
}

// ownsRow reports whether r is the kept original of c's list file.
func (c *loadOrderComputation) ownsRow(game *domain.Game, r prefixOriginal) bool {
	return filepath.Clean(r.Prefix) == filepath.Clean(game.InstallPath) &&
		strings.EqualFold(path.Dir(r.Path), c.spec.Dir.Path) && strings.EqualFold(path.Base(r.Path), c.spec.ListFile)
}

// place works out c's effective order: the profile's order of the files
// lmm deploys, the external files the current list names after the file
// they followed there, and every file lmm deploys that the order does not
// name at its place in the game's default order.
func (c *loadOrderComputation) place() {
	var listed []string
	seen := map[string]bool{}
	for _, name := range c.order {
		key := loadOrderKey(name)
		if seen[key] {
			continue
		}
		if _, ok := c.managed[key]; ok {
			seen[key] = true
			listed = append(listed, c.onDisk[key])
			c.placement[key] = LoadOrderPlacedOrder
		}
	}
	var externals []string
	for _, name := range c.current {
		key := loadOrderKey(name)
		if seen[key] {
			continue
		}
		if _, isManaged := c.managed[key]; isManaged {
			continue
		}
		if _, there := c.onDisk[key]; !there || hasControl(name) {
			continue
		}
		seen[key] = true
		externals = append(externals, name)
		c.placement[key] = LoadOrderPlacedExternal
	}
	c.external = len(externals)
	if len(externals) > 0 {
		nodes := func(names []string) []pluginNode {
			out := make([]pluginNode, len(names))
			for i, n := range names {
				out[i] = pluginNode{name: n}
			}
			return out
		}
		current := make([]listEntry, len(c.current))
		for i, n := range c.current {
			current[i] = listEntry{name: n}
		}
		ranks := mergedRanks(nodes(listed), nodes(externals), current)
		listed = append(listed, externals...)
		slices.SortStableFunc(listed, func(a, b string) int { return ranks[loadOrderKey(a)] - ranks[loadOrderKey(b)] })
	}
	var rest []string
	for key := range c.managed {
		if !seen[key] {
			rest = append(rest, c.onDisk[key])
		}
	}
	slices.Sort(rest)
	for _, name := range rest {
		c.placement[loadOrderKey(name)] = LoadOrderPlacedDefault
		slot := defaultSlot(listed, name)
		listed = slices.Insert(listed, slot, name)
	}
	for _, name := range listed {
		if hasControl(name) {
			c.findings = append(c.findings, LoadOrderFinding{Code: LoadOrderFindingUnencodable, Severity: PluginSeverityWarning, File: name,
				Message: fmt.Sprintf("%q cannot be written to %s (its name has a control character), so it is left out and loads after the listed files - rename it", name, c.spec.ListFile)})
			continue
		}
		c.entries = append(c.entries, name)
	}
	for key, name := range c.onDisk {
		if _, isManaged := c.managed[key]; !isManaged && !seen[key] {
			c.unlisted = append(c.unlisted, name)
		}
	}
	slices.Sort(c.unlisted)
	if len(c.unlisted) > 0 && len(c.entries) > 0 {
		c.findings = append(c.findings, LoadOrderFinding{Code: LoadOrderFindingUnlisted, Severity: PluginSeverityWarning, Files: slices.Clone(c.unlisted),
			Message: fmt.Sprintf("%d file(s) in %s were not deployed by lmm and are not in %s, so the game loads them after every listed file (%s) - add them to the list by hand to place them",
				len(c.unlisted), c.spec.Dir.Path, c.spec.ListFile, previewNames(c.unlisted, 5))})
	}
}

// defaultSlot is where name goes in order when nothing orders it: just
// after the file it follows in byte-wise order, else just before the first
// one it precedes - its place among its neighbours in the order the game
// uses with no list (as Vortex's Cyberpunk extension places a new archive).
func defaultSlot(order []string, name string) int {
	best := -1
	for i, n := range order {
		if n < name && (best < 0 || n > order[best]) {
			best = i
		}
	}
	if best >= 0 {
		return best + 1
	}
	first := -1
	for i, n := range order {
		if first < 0 || n < order[first] {
			first = i
		}
	}
	if first < 0 {
		return 0
	}
	return first
}

// previewNames is names joined, at most n of them, with how many more.
func previewNames(names []string, n int) string {
	if len(names) <= n {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:n], ", "), len(names)-n)
}

// parseLoadOrderList is a list file's names, in order: each line trimmed,
// blank lines skipped (as the game and Vortex read it).
func parseLoadOrderList(data []byte) []string {
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// renderLoadOrderList is names as a list file: one per line, with eol
// between them, and after the last one only when trailing is set.
func renderLoadOrderList(names []string, eol string, trailing bool) []byte {
	if len(names) == 0 {
		return nil
	}
	out := strings.Join(names, eol)
	if trailing {
		out += eol
	}
	return []byte(out)
}

// renderEntries is c's entries as the list file: an existing file's line
// ending and trailing line ending kept, a new file's from the spec (no
// trailing line ending, as Vortex writes it).
func (c *loadOrderComputation) render() []byte {
	eol, trailing := c.spec.LineEnding, false
	if eol == "" {
		eol = eolNew
	}
	if c.listExists && bytes.ContainsRune(c.listData, '\n') {
		eol = lineEndingOf(c.listData)
		trailing = bytes.HasSuffix(c.listData, []byte("\n"))
	}
	return renderLoadOrderList(c.entries, eol, trailing)
}

// ownedNames is the files c lists that lmm deploys, sorted.
func (c *loadOrderComputation) ownedNames() []string {
	var out []string
	for _, n := range c.entries {
		if _, ok := c.managed[loadOrderKey(n)]; ok {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// openGameDir opens rel beneath the game's install_path, matching each
// component case-insensitively and following no link below the
// install_path itself.
func openGameDir(installPath, rel string) (*os.File, error) {
	dir, err := openDirBeneath(installPath, "", false)
	if err != nil {
		return nil, err
	}
	for _, comp := range strings.Split(filepath.ToSlash(rel), "/") {
		if comp == "" || comp == "." {
			continue
		}
		next, err := openChildFold(dir, comp, false)
		_ = dir.Close()
		if err != nil {
			return nil, err
		}
		dir = next
	}
	return dir, nil
}

// relOrSelf is dir relative to base in slash form, or fallback.
func relOrSelf(base, dir, fallback string) string {
	if rel, ok := relUnder(base, dir); ok {
		return rel
	}
	if real, err := filepath.EvalSymlinks(base); err == nil {
		if rel, ok := relUnder(real, dir); ok {
			return rel
		}
	}
	return fallback
}

// errLoadOrderChanged is writeLoadOrder's refusal when the list file is no
// longer what the computation read: something saved it in between.
var errLoadOrderChanged = errors.New("the load-order list changed while lmm was working out its order")

// syncLoadOrders writes every file load order of game for profileName -
// the step syncMergedPak runs beside the plugin list. It never fails the
// flow: every problem is a finding. nil when the game has no file load
// order, or profileName is not the active profile.
func (s *Service) syncLoadOrders(ctx context.Context, game *domain.Game, profileName string) []FileLoadOrderResult {
	specs := s.loadOrderSpecs(game)
	if len(specs) == 0 {
		return nil
	}
	if _, recordedOnly, err := s.profileScope(ctx, game.ID, profileName); err != nil || recordedOnly {
		return nil
	}
	var out []FileLoadOrderResult
	for _, spec := range specs {
		if res := s.syncLoadOrder(ctx, game, profileName, spec); res != nil {
			out = append(out, *res)
		}
	}
	return out
}

// syncLoadOrder is syncLoadOrders for one order.
func (s *Service) syncLoadOrder(ctx context.Context, game *domain.Game, profileName string, spec adapter.FileLoadOrderSpec) *FileLoadOrderResult {
	var res *FileLoadOrderResult
	for attempt := 0; attempt < 2; attempt++ {
		c, err := s.computeLoadOrder(ctx, game, profileName, spec, nil, false)
		if err != nil {
			return &FileLoadOrderResult{ID: spec.ID, Status: PluginListSkipped, Findings: []LoadOrderFinding{loadOrderUnwritable(spec, err)}}
		}
		if c.restore {
			return s.restoreLoadOrder(game, spec)
		}
		if c.nothing {
			return nil
		}
		res = c.result()
		if c.skipped {
			res.Status = PluginListSkipped
			return res
		}
		written, err := s.writeLoadOrder(game, c)
		if errors.Is(err, errLoadOrderChanged) {
			continue
		}
		if err != nil {
			res.Status = PluginListSkipped
			res.Findings = append([]LoadOrderFinding{loadOrderUnwritable(spec, err)}, res.Findings...)
			return res
		}
		res.Status = PluginListUnchanged
		if written {
			res.Status = PluginListWritten
		}
		return res
	}
	res.Status = PluginListSkipped
	res.Findings = append([]LoadOrderFinding{loadOrderUnwritable(spec, errLoadOrderChanged)}, res.Findings...)
	return res
}

// loadOrderUnwritable is the finding for a list lmm could not read or
// write.
func loadOrderUnwritable(spec adapter.FileLoadOrderSpec, err error) LoadOrderFinding {
	return LoadOrderFinding{Code: LoadOrderFindingUnwritable, Severity: PluginSeverityError,
		Message: fmt.Sprintf("the %s list (%s) was not written: %v", spec.Label, spec.ListFile, err)}
}

// result is c as a FileLoadOrderResult, without a Status.
func (c *loadOrderComputation) result() *FileLoadOrderResult {
	res := &FileLoadOrderResult{ID: c.spec.ID, ListFile: c.listPath, External: c.external, Findings: slices.Clone(c.findings)}
	if !c.skipped {
		res.Files = len(c.entries)
	}
	slices.SortStableFunc(res.Findings, func(a, b LoadOrderFinding) int {
		return severityRank(a.Severity) - severityRank(b.Severity)
	})
	return res
}

// writeLoadOrder writes c's list file, capturing its original first. It
// reports whether the file changed, and refuses with errLoadOrderChanged
// when the file is no longer what c was computed from.
func (s *Service) writeLoadOrder(game *domain.Game, c *loadOrderComputation) (bool, error) {
	content := c.render()
	if !c.listExists && len(c.entries) == 0 {
		return false, nil // nothing to list, and no file to keep in step
	}
	dir, err := openGameDir(game.InstallPath, c.dirRel)
	if err != nil {
		return false, err
	}
	defer func() { _ = dir.Close() }()
	data, perm, exists, err := readAt(dir, c.listName)
	if err != nil {
		return false, err
	}
	if exists != c.listExists || !bytes.Equal(data, c.listData) {
		return false, errLoadOrderChanged
	}
	store := s.loadOrderOriginalsFor(game.ID)
	rel := path.Join(c.dirRel, c.listName)
	changed := false
	if !exists || !bytes.Equal(data, content) {
		if store != nil {
			row := prefixOriginal{Prefix: game.InstallPath, Path: rel, Existed: exists, Mode: uint32(perm)}
			if err := store.capture(row, data); err != nil {
				return false, fmt.Errorf("keeping a copy of %s before replacing it: %w", c.listName, err)
			}
		}
		if perm == 0 {
			perm = 0o644
		}
		if err := replaceAt(dir, c.listName, content, perm); err != nil {
			return false, err
		}
		changed = true
	}
	if store != nil {
		if err := store.setOwned(game.InstallPath, "", rel, c.ownedNames()); err != nil {
			return changed, fmt.Errorf("recording which files in %s are lmm's: %w", c.listName, err)
		}
	}
	return changed, nil
}

// restoreLoadOrders puts back every load-order list game had before lmm
// first wrote it - `lmm purge`'s step, beside the plugin list's. nil when
// lmm never wrote one.
func (s *Service) restoreLoadOrders(game *domain.Game) []FileLoadOrderResult {
	store := s.loadOrderOriginalsFor(game.ID)
	if store == nil {
		return nil
	}
	rows, err := store.list()
	if err != nil {
		return []FileLoadOrderResult{{Status: PluginListSkipped, Findings: []LoadOrderFinding{loadOrderUnrestorable(err)}}}
	}
	var out []FileLoadOrderResult
	for _, row := range rows {
		res := s.restoreLoadOrderRow(store, game, row, s.loadOrderIDFor(game, row))
		out = append(out, res)
	}
	return out
}

// restoreLoadOrder hands back spec's list, when lmm keeps an original of
// it: the profile now orders nothing.
func (s *Service) restoreLoadOrder(game *domain.Game, spec adapter.FileLoadOrderSpec) *FileLoadOrderResult {
	store := s.loadOrderOriginalsFor(game.ID)
	if store == nil {
		return nil
	}
	rows, err := store.list()
	if err != nil {
		return &FileLoadOrderResult{ID: spec.ID, Status: PluginListSkipped, Findings: []LoadOrderFinding{loadOrderUnrestorable(err)}}
	}
	probe := &loadOrderComputation{spec: spec}
	for _, row := range rows {
		if probe.ownsRow(game, row) {
			res := s.restoreLoadOrderRow(store, game, row, spec.ID)
			return &res
		}
	}
	return nil
}

// loadOrderIDFor is the id of game's load order whose list row keeps, or
// "" when the game's adapter no longer has it.
func (s *Service) loadOrderIDFor(game *domain.Game, row prefixOriginal) string {
	for _, spec := range s.loadOrderSpecs(game) {
		probe := &loadOrderComputation{spec: spec}
		if probe.ownsRow(game, row) {
			return spec.ID
		}
	}
	return ""
}

// restoreLoadOrderRow puts row's list back and forgets it: the CURRENT
// list without the files lmm deployed in it, plus the entries only the
// kept file has, each after the file it followed - so an entry the user
// added or moved since stays as they left it. When nothing outside lmm
// changed, the kept file comes back byte for byte; a file lmm created and
// nothing else uses is removed. A row whose install_path cannot be found
// now (an unmounted drive) is kept, with a finding, for the next purge.
func (s *Service) restoreLoadOrderRow(store *prefixOriginals, game *domain.Game, row prefixOriginal, id string) FileLoadOrderResult {
	res := FileLoadOrderResult{ID: id, Status: PluginListRestored, ListFile: filepath.Join(row.Prefix, filepath.FromSlash(row.Path))}
	fail := func(err error) FileLoadOrderResult {
		res.Status = PluginListSkipped
		res.Findings = append(res.Findings, loadOrderUnrestorable(err))
		return res
	}
	if _, err := os.Stat(row.Prefix); errors.Is(err, os.ErrNotExist) {
		res.Status = PluginListSkipped
		res.Findings = append(res.Findings, LoadOrderFinding{Code: LoadOrderFindingUnwritable, Severity: PluginSeverityWarning,
			Message: fmt.Sprintf("%s, where lmm keeps the game's original %s, cannot be found now (is its drive mounted?); lmm keeps the copy, and the next purge puts it back", row.Prefix, path.Base(row.Path))})
		return res
	}
	original, err := store.stored(row)
	if err != nil {
		return fail(err)
	}
	dirRel, name := path.Split(row.Path)
	dir, err := openGameDir(row.Prefix, dirRel)
	if errors.Is(err, os.ErrNotExist) && !row.Existed {
		res.Status = PluginListRemoved
		if err := store.forget(row); err != nil {
			return fail(err)
		}
		return res
	}
	if err != nil {
		return fail(err)
	}
	defer func() { _ = dir.Close() }()
	res.ListFile = filepath.Join(dir.Name(), name)
	current, _, exists, err := readAt(dir, name)
	if err != nil {
		return fail(err)
	}
	content := original
	if exists {
		content = mergeRestoredLoadOrder(original, current, row.Existed, row.Owned)
	}
	switch {
	case content == nil && !row.Existed:
		if err := removeAt(dir, name); err != nil {
			return fail(err)
		}
		res.Status = PluginListRemoved
	default:
		perm := os.FileMode(row.Mode).Perm()
		if perm == 0 {
			perm = 0o644
		}
		if !sameContentAt(dir, name, content) {
			if err := replaceAt(dir, name, content, perm); err != nil {
				return fail(err)
			}
		}
		res.Files = len(parseLoadOrderList(content))
	}
	if err := store.forget(row); err != nil {
		return fail(err)
	}
	return res
}

// mergeRestoredLoadOrder is the list a purge writes (restoreLoadOrderRow):
// current without owned, plus every entry of the kept original that
// current - lmm's files aside - no longer has, after the file it followed
// there. The original's own bytes when that is exactly the original's
// list; nil when the original did not exist and nothing is left to list.
func mergeRestoredLoadOrder(original, current []byte, existed bool, owned []string) []byte {
	now := parseLoadOrderList(current)
	var base []string
	if existed {
		base = parseLoadOrderList(original)
	}
	isOwned := map[string]bool{}
	for _, n := range owned {
		isOwned[loadOrderKey(n)] = true
	}
	var keep []pluginNode
	stillThere := map[string]bool{}
	for _, n := range now {
		if !isOwned[loadOrderKey(n)] && !stillThere[loadOrderKey(n)] {
			keep = append(keep, pluginNode{name: n})
			stillThere[loadOrderKey(n)] = true
		}
	}
	var only []pluginNode
	for _, n := range base {
		if !stillThere[loadOrderKey(n)] {
			only = append(only, pluginNode{name: n})
		}
	}
	baseEntries := make([]listEntry, len(base))
	for i, n := range base {
		baseEntries[i] = listEntry{name: n}
	}
	ranks := mergedRanks(keep, only, baseEntries)
	all := append(slices.Clone(keep), only...)
	slices.SortStableFunc(all, func(a, b pluginNode) int { return ranks[pluginKey(a.name)] - ranks[pluginKey(b.name)] })
	names := make([]string, len(all))
	for i, n := range all {
		names[i] = n.name
	}
	if existed && slices.Equal(names, base) {
		return original
	}
	if !existed && len(names) == 0 {
		return nil
	}
	format := current
	if len(bytes.TrimSpace(current)) == 0 && existed {
		format = original
	}
	eol := lineEndingOf(format)
	return renderLoadOrderList(names, eol, bytes.HasSuffix(format, []byte("\n")))
}

// loadOrderUnrestorable is the finding for an original lmm could not put
// back.
func loadOrderUnrestorable(err error) LoadOrderFinding {
	return LoadOrderFinding{Code: LoadOrderFindingUnwritable, Severity: PluginSeverityError,
		Message: fmt.Sprintf("the load-order list lmm replaced was not put back: %v", err)}
}
