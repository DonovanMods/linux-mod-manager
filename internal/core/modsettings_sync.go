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
	"syscall"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/larian"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// Baldur's Gate 3's mod list (#646): an adapter.FileListModSettings load
// order. The game loads a .pak from its Mods folder only while
// PlayerProfiles/Public/modsettings.lsx lists the module the pak's
// meta.lsx declares, and loads them in the list's order. #620's sync writes
// it, at the same choke point and with the same originals store, through
// the functions here: computeLoadOrder hands a FileListModSettings order to
// computeModSettings, writeLoadOrder to writeModSettings, and a purge's
// restore to restoreModSettingsRow.
//
// It shares #620's rules where they apply, and differs where the list
// ENABLES rather than only orders:
//
//   - OWNERSHIP. lmm owns the entries for the modules of the paks it
//     deployed, and nothing else. Every other entry - the game's own
//     GustavX (or GustavDev) entry, a mod the user or the in-game manager
//     enabled - is EXTERNAL: kept byte for byte where it is, never moved,
//     never dropped. The game's own entries stay at the top. lmm never
//     ADDS an external entry: a pak lmm did not deploy that the list does
//     not name is disabled, and stays so.
//   - The list is always written once lmm deploys a pak with a module: an
//     unlisted pak does not load, so there is no "game's own order" to
//     leave it to. The profile's load_orders: list orders lmm's paks; a pak
//     it does not order goes after the ones it does - in the list's order
//     for those already listed, then alphabetically - which is where
//     Vortex adds a new pak too (loadOrder.ts deserialize, "Add any newly
//     added mods to the bottom").
//   - An override pak (no meta.lsx of its own) needs no entry and gets
//     none: Vortex lists only paks with a meta.lsx (cache.ts isLOListed,
//     loadOrder.ts exportTo's !isListed filter). A pak whose meta.lsx lmm
//     cannot read is left out with a warning.
//   - lmm never creates modsettings.lsx. The game writes it on its first
//     run, and which base entry it needs depends on the game's patch
//     (Vortex keeps one template per patch, common.ts); a missing list is a
//     warning saying to run the game once. A pre-Patch 7 list (a ModOrder
//     node) is not written either.
//   - The file lmm first replaced is kept, and a purge puts it back without
//     undoing what the user changed since (restoreModSettingsRow).

// LoadOrderFindingPakUnreadable, LoadOrderFindingListMissing and
// LoadOrderFindingListFormat are LoadOrderFinding.Code's values for a
// modsettings.lsx order (#646).
const (
	// LoadOrderFindingPakUnreadable: a .pak lmm deploys has a meta.lsx lmm
	// cannot read (a corrupt pak, a zstd-compressed member), so its module
	// is not listed and the game will not load it. A warning.
	LoadOrderFindingPakUnreadable = "load_order_pak_unreadable"
	// LoadOrderFindingListMissing: the game's list file is not there yet
	// (the game writes it on its first run), so lmm listed nothing. A
	// warning.
	LoadOrderFindingListMissing = "load_order_list_missing"
	// LoadOrderFindingListFormat: the list file is in a format lmm does not
	// write (modsettings.lsx from before Patch 7), so lmm listed nothing.
	LoadOrderFindingListFormat = "load_order_list_format"
)

// modSettingsState is what a modsettings.lsx computation knows beyond a
// FileListLines one.
type modSettingsState struct {
	// root is the game's resolved prefix; listDir the list's directory in
	// it.
	root    *domain.ResolvedPrefix
	listDir prefixPath
	// parsed is the list as read.
	parsed *larian.ModSettings
	// modules are each managed pak's modules, by pak key.
	modules map[string][]larian.Module
	// unlisted are lmm's paks that get no entry: override paks, and paks
	// whose meta.lsx could not be read. They are not "not deployed".
	unlisted map[string]bool
	// external maps an external entry's name in c.entries to its entry.
	external map[string]larian.ModEntry
	// fileEntry is the list's first entry for each module UUID (lower
	// case).
	fileEntry map[string]larian.ModEntry
	// owned are the UUIDs (lower case) of the modules lmm lists.
	owned []string
	// adopted are the entries lmm has taken over from the user, from the
	// kept row, and with this computation's changes: entries it takes over
	// now added, entries it gives back now removed (#646 review).
	adopted []adoptedEntry
}

// modulesFor is the modules of the managed pak named name.
func (m *modSettingsState) modulesFor(name string) []larian.Module {
	return m.modules[loadOrderKey(name)]
}

// computeModSettings is computeLoadOrder for a FileListModSettings order.
func (s *Service) computeModSettings(ctx context.Context, game *domain.Game, profileName string, spec adapter.FileLoadOrderSpec, order []string, report bool) (*loadOrderComputation, error) {
	ms := &modSettingsState{modules: map[string][]larian.Module{}, unlisted: map[string]bool{}, external: map[string]larian.ModEntry{}, fileEntry: map[string]larian.ModEntry{}}
	listDir := spec.ListDirectory()
	c := &loadOrderComputation{spec: spec, dirRel: listDir.Path, listName: spec.ListFile, onDisk: map[string]string{}, managed: map[string]int{}, placement: map[string]string{}, ms: ms}
	profile, _ := config.LoadProfile(s.configDir, game.ID, profileName)
	if order == nil && profile != nil {
		order = profile.LoadOrders[spec.ID]
	}
	c.order = order
	if spec.Dir.Root != adapter.SpecRootUserDir {
		c.skip(fmt.Sprintf("the %s list was not written: the game's adapter places it outside the prefix's user directory, which lmm does not write", spec.Label))
		return c, nil
	}
	store := s.loadOrderOriginalsFor(game.ID)
	var prevOwned map[string]bool
	if store != nil {
		rows, err := store.list()
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if c.ownsRow(game, r) {
				c.wrote = true
				prevOwned = map[string]bool{}
				for _, u := range r.Owned {
					prevOwned[strings.ToLower(u)] = true
				}
				ms.adopted = slices.Clone(r.Adopted)
			}
		}
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
	for _, row := range rows {
		if !row.Path.IsPrefix() {
			continue
		}
		dir, name := path.Split(filepath.ToSlash(row.Path.Rel))
		if !strings.EqualFold(strings.TrimSuffix(dir, "/"), spec.Dir.Path) || !spec.Covers(name) {
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
	if len(deployedKeys) == 0 && !c.wrote {
		c.nothing = true
		return c, nil
	}

	ms.root = game.PrefixRoot
	if ms.root == nil {
		ms.root = s.resolvedPrefixRoot(game)
	}
	if ms.root == nil {
		c.skip(fmt.Sprintf("the %s list was not written: the game's Wine/Proton prefix cannot be resolved (%s)", spec.Label, s.ResolvePrefix(game).Problem))
		return c, nil
	}
	ms.listDir = prefixPath{prefix: ms.root.Path, user: ms.root.User, comps: splitRel(listDir.Path)}
	modsDir := prefixPath{prefix: ms.root.Path, user: ms.root.User, comps: splitRel(spec.Dir.Path)}

	dir, err := modsDir.open(false)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if len(deployedKeys) > 0 {
			c.skip(fmt.Sprintf("the %s list was not written, because %s, where this profile deploys %d pak(s), is not there - is the prefix's drive mounted? (lmm deploy puts them back)",
				spec.Label, modsDir.display(), len(deployedKeys)))
			return c, nil
		}
		c.restore = c.wrote
		c.nothing = !c.wrote
		return c, nil
	case err != nil:
		c.skip(fmt.Sprintf("the %s list was not written: %v", spec.Label, err))
		return c, nil
	}
	defer func() { _ = dir.Close() }()
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
	there := 0
	for key, idx := range deployedKeys {
		name, ok := c.onDisk[key]
		if !ok {
			continue
		}
		there++
		mods, err := s.pakModules(dir, name)
		switch {
		case err != nil:
			ms.unlisted[key] = true
			c.findings = append(c.findings, pakUnreadable(spec, name, err))
		case len(mods) == 0:
			ms.unlisted[key] = true // an override pak: loaded without an entry
		default:
			ms.modules[key] = mods
			c.managed[key] = idx
		}
	}
	if len(deployedKeys) > 0 && there == 0 {
		c.skip(fmt.Sprintf("the %s list was not written, because none of the %d pak(s) this profile deploys is in %s - is the prefix's drive mounted? (lmm deploy puts them back)",
			spec.Label, len(deployedKeys), dir.Name()))
		return c, nil
	}
	if len(c.managed) == 0 && !c.wrote {
		// No module of lmm's to list, and a list lmm does not hold: it is
		// the user's alone. Any finding (a pak lmm could not read) still
		// stands.
		c.nothing = len(c.findings) == 0
		c.skipped = !c.nothing
		return c, nil
	}

	if err := s.readModSettings(c); err != nil || c.skipped {
		return c, err
	}
	if len(c.managed) == 0 && !report {
		// lmm listed paks before and deploys none now: hand the list back.
		c.restore = true
		return c, nil
	}
	c.placeModSettings(prevOwned)
	return c, nil
}

// readModSettings reads c's list file into c (listPath, listData, parsed,
// current), or marks c skipped with the finding that says why not.
func (s *Service) readModSettings(c *loadOrderComputation) error {
	ms, spec := c.ms, c.spec
	c.listPath = filepath.Join(ms.listDir.display(), spec.ListFile)
	missing := func() {
		c.skipped = true
		c.findings = append(c.findings, LoadOrderFinding{Code: LoadOrderFindingListMissing, Severity: PluginSeverityWarning, File: spec.ListFile,
			Message: fmt.Sprintf("%s is not there yet (%s), so the paks lmm deploys are not enabled: run the game once so it writes its mod list, then `lmm deploy` (or `lmm verify --fix`) lists them",
				spec.ListFile, c.listPath)})
	}
	dir, err := ms.listDir.open(false)
	if errors.Is(err, os.ErrNotExist) {
		missing()
		return nil
	}
	if err != nil {
		c.skip(fmt.Sprintf("the %s list was not written: %v", spec.Label, err))
		return nil
	}
	defer func() { _ = dir.Close() }()
	c.dirRel = relOrSelf(ms.root.UserDir(), dir.Name(), spec.ListDirectory().Path)
	if real, err := entryFold(dir, spec.ListFile); err != nil {
		c.skip(fmt.Sprintf("the %s list was not written: %v", spec.Label, err))
		return nil
	} else if real != "" {
		c.listName = real
	}
	c.listPath = filepath.Join(dir.Name(), c.listName)
	data, _, exists, err := readAt(dir, c.listName)
	if err != nil {
		c.skip(fmt.Sprintf("the %s list was not written: %v", spec.Label, err))
		return nil
	}
	if !exists {
		missing()
		return nil
	}
	c.listData, c.listExists = data, true
	parsed, err := larian.ParseModSettings(data)
	if err != nil {
		c.skip(fmt.Sprintf("%s cannot be read as the game's mod list (%v), so lmm leaves it alone and lists nothing; the game rewrites it when it starts", c.listPath, err))
		return nil
	}
	if parsed.HasModOrder {
		c.skipped = true
		c.findings = append(c.findings, LoadOrderFinding{Code: LoadOrderFindingListFormat, Severity: PluginSeverityWarning, File: spec.ListFile,
			Message: fmt.Sprintf("%s is in the format the game used before Patch 7 (it has a ModOrder node), which lmm does not write, so the paks lmm deploys are not enabled: update the game, or enable them with BG3 Mod Manager", c.listPath)})
		return nil
	}
	ms.parsed = parsed
	return nil
}

// placeModSettings works out c's entries: the game's own entries first, as
// the list has them; lmm's paks in the profile's order, then the paks it
// does not order (those the list names in its order, then the rest
// alphabetically); every other entry the list holds at its place among
// them (mergeExternals). An entry for a module lmm listed before whose pak
// it no longer deploys (prevOwned) is dropped - or, when lmm took it over
// from the user, given back as the user wrote it.
//
// Adoption (#646 review): an entry lmm did not write, for a module a pak
// lmm deploys declares, becomes lmm's - it is ordered with the pak and
// kept in step with its meta.lsx. Once lmm holds the list (c.wrote), the
// entry's own bytes are recorded first, so the user's entry comes back
// when the pak goes; before that, the original lmm captures at its first
// write holds it.
func (c *loadOrderComputation) placeModSettings(prevOwned map[string]bool) {
	ms := c.ms
	uuidPak := map[string]string{} // module UUID -> managed pak key
	var keys []string
	for key := range ms.modules {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		for _, m := range ms.modules[key] {
			if _, taken := uuidPak[strings.ToLower(m.UUID)]; !taken {
				uuidPak[strings.ToLower(m.UUID)] = key
			}
		}
	}

	// The list as it is: lmm's paks by name (each once), every other entry
	// by a name of its own.
	var base, current []string
	inList := map[string]bool{}
	taken := map[string]bool{}
	adopted := map[string]adoptedEntry{}
	for _, a := range ms.adopted {
		adopted[a.UUID] = a
	}
	prev := ""
	for _, e := range ms.parsed.Entries {
		u := strings.ToLower(e.UUID)
		after := prev
		prev = u
		if _, seen := ms.fileEntry[u]; !seen && u != "" {
			ms.fileEntry[u] = e
		}
		if key, ok := uuidPak[u]; ok {
			if _, have := adopted[u]; c.wrote && !prevOwned[u] && !have && u != "" {
				a := adoptedEntry{UUID: u, After: after, Entry: string(e.Raw())}
				adopted[u] = a
				ms.adopted = append(ms.adopted, a)
			}
			if !inList[key] {
				inList[key] = true
				current = append(current, c.onDisk[key])
			}
			continue
		}
		if prevOwned[u] {
			a, ok := adopted[u]
			if !ok {
				continue // lmm's, for a pak it no longer deploys
			}
			// The user's own entry lmm took over: given back as written.
			delete(adopted, u)
			ms.adopted = slices.DeleteFunc(ms.adopted, func(x adoptedEntry) bool { return x.UUID == u })
			given, err := larian.EntryFromRaw([]byte(a.Entry))
			if err != nil {
				continue
			}
			e = given
		}
		name := externalEntryName(e, taken)
		ms.external[name] = e
		current = append(current, name)
		if larian.IsBaseModule(e.UUID, e.Name) {
			base = append(base, name)
		}
	}
	c.current = current

	// lmm's paks: the profile's order, then the rest.
	var managed []string
	seen := map[string]bool{}
	for _, name := range c.order {
		key := loadOrderKey(name)
		if _, ok := c.managed[key]; ok && !seen[key] {
			seen[key] = true
			managed = append(managed, c.onDisk[key])
			c.placement[key] = LoadOrderPlacedOrder
		}
	}
	for _, name := range current {
		key := loadOrderKey(name)
		if _, ok := c.managed[key]; ok && !seen[key] {
			seen[key] = true
			managed = append(managed, name)
			c.placement[key] = LoadOrderPlacedDefault
		}
	}
	var rest []string
	for key := range c.managed {
		if !seen[key] {
			rest = append(rest, c.onDisk[key])
			c.placement[key] = LoadOrderPlacedDefault
		}
	}
	slices.Sort(rest)
	managed = append(managed, rest...)

	isBase := map[string]bool{}
	for _, n := range base {
		isBase[n] = true
	}
	var externals []string
	for _, name := range current {
		if _, ok := c.managed[loadOrderKey(name)]; !ok && !isBase[name] {
			externals = append(externals, name)
		}
	}
	c.entries = append(slices.Clone(base), mergeExternals(managed, externals, current)...)
	for _, name := range c.entries {
		if _, ok := c.managed[loadOrderKey(name)]; !ok {
			c.placement[loadOrderKey(name)] = LoadOrderPlacedExternal
			c.external++
		}
	}
	ms.owned = nil
	for _, name := range c.entries {
		for _, m := range ms.modulesFor(name) {
			if uuidPak[strings.ToLower(m.UUID)] == loadOrderKey(name) {
				ms.owned = append(ms.owned, strings.ToLower(m.UUID))
			}
		}
	}
	slices.Sort(ms.owned)
}

// outdatedModSettings describes each of lmm's paks whose entry the list
// has, but with fields its meta.lsx no longer states (#646 review): an
// older version, another name, folder or checksum. A deploy rewrites such
// an entry (renderModSettings), so verify's --fix does too.
func (c *loadOrderComputation) outdatedModSettings() []string {
	ms := c.ms
	var out []string
	for _, name := range c.entries {
		for _, m := range ms.modulesFor(name) {
			if !slices.Contains(ms.owned, strings.ToLower(m.UUID)) {
				continue
			}
			e, ok := ms.fileEntry[strings.ToLower(m.UUID)]
			if !ok || e.Matches(m) {
				continue
			}
			var differ []string
			for _, d := range []struct {
				field     string
				have, now string
			}{{"name", e.Name, m.Name}, {"folder", e.Folder, m.Folder}, {"version", e.Version64, m.Version64}, {"MD5", e.MD5, m.MD5}} {
				if d.have != d.now {
					differ = append(differ, d.field)
				}
			}
			out = append(out, fmt.Sprintf("%s's entry in %s does not match its meta.lsx (its %s differ)", name, c.listName, strings.Join(differ, ", ")))
		}
	}
	return out
}

// externalEntryName is the name an entry lmm did not add goes by in a
// computation and a report: its Name, else its Folder, else its UUID -
// with the UUID after it when that name is taken already (two entries may
// share a display name, and a name may look like a pak's).
func externalEntryName(e larian.ModEntry, taken map[string]bool) string {
	name := e.Name
	if name == "" {
		name = e.Folder
	}
	if name == "" {
		name = e.UUID
	}
	if taken[loadOrderKey(name)] || strings.HasSuffix(strings.ToLower(name), ".pak") {
		name = fmt.Sprintf("%s {%s}", name, e.UUID)
	}
	for i := 2; taken[loadOrderKey(name)]; i++ {
		name = fmt.Sprintf("%s {%s} #%d", e.Name, e.UUID, i)
	}
	taken[loadOrderKey(name)] = true
	return name
}

// renderModSettings is c's list file: its entries, lmm's paks as one entry
// per module - the list's own bytes for an entry that still matches the
// pak's meta.lsx, a new entry otherwise.
func (c *loadOrderComputation) renderModSettings() []byte {
	ms := c.ms
	var out []larian.ModEntry
	for _, name := range c.entries {
		if e, ok := ms.external[name]; ok {
			out = append(out, e)
			continue
		}
		for _, m := range ms.modulesFor(name) {
			if !slices.Contains(ms.owned, strings.ToLower(m.UUID)) {
				continue
			}
			if e, ok := ms.fileEntry[strings.ToLower(m.UUID)]; ok && e.Matches(m) {
				out = append(out, e)
				continue
			}
			out = append(out, larian.NewModEntry(m))
		}
	}
	return ms.parsed.Render(out)
}

// writeModSettings is writeLoadOrder for a modsettings.lsx order.
func (s *Service) writeModSettings(game *domain.Game, c *loadOrderComputation) (bool, error) {
	ms := c.ms
	if ms.parsed == nil || (!c.wrote && len(ms.owned) == 0) {
		return false, nil
	}
	content := c.renderModSettings()
	dir, err := ms.listDir.open(false)
	if err != nil {
		return false, err
	}
	defer func() { _ = dir.Close() }()
	data, perm, exists, err := readAt(dir, c.listName)
	if err != nil {
		return false, err
	}
	if !exists || !bytes.Equal(data, c.listData) {
		return false, errLoadOrderChanged
	}
	store := s.loadOrderOriginalsFor(game.ID)
	rel := path.Join(c.dirRel, c.listName)
	changed := false
	if !bytes.Equal(data, content) {
		if store != nil {
			row := prefixOriginal{Prefix: ms.root.Path, User: ms.root.User, Path: rel, Existed: true, Mode: uint32(perm)}
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
		if err := store.setOwnedAdded(ms.root.Path, ms.root.User, rel, ms.owned, nil); err != nil {
			return changed, fmt.Errorf("recording which entries in %s are lmm's: %w", c.listName, err)
		}
		if err := store.setAdopted(ms.root.Path, ms.root.User, rel, ms.adopted); err != nil {
			return changed, fmt.Errorf("recording the entries in %s lmm took over: %w", c.listName, err)
		}
	}
	return changed, nil
}

// restoreModSettingsRow is restoreLoadOrderRow for a list in a prefix's
// user directory: the current list without lmm's entries, plus the
// entries only the kept original has, each after the entry it followed
// there (mergeRestoredModSettings).
func (s *Service) restoreModSettingsRow(store *prefixOriginals, row prefixOriginal, id string) FileLoadOrderResult {
	dirRel, name := path.Split(row.Path)
	where := prefixPath{prefix: row.Prefix, user: row.User, comps: splitRel(dirRel)}
	res := FileLoadOrderResult{ID: id, Status: PluginListRestored, ListFile: filepath.Join(where.display(), name)}
	fail := func(err error) FileLoadOrderResult {
		res.Status = PluginListSkipped
		res.Findings = append(res.Findings, loadOrderUnrestorable(err))
		return res
	}
	if _, err := os.Stat(row.Prefix); errors.Is(err, os.ErrNotExist) {
		res.Status = PluginListSkipped
		res.Findings = append(res.Findings, LoadOrderFinding{Code: LoadOrderFindingUnwritable, Severity: PluginSeverityWarning,
			Message: fmt.Sprintf("%s, the prefix where lmm keeps the game's original %s, cannot be found now (is its drive mounted?); lmm keeps the copy, and the next purge puts it back", row.Prefix, name)})
		return res
	}
	original, err := store.stored(row)
	if err != nil {
		return fail(err)
	}
	dir, err := where.open(false)
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
	if real, err := entryFold(dir, name); err == nil && real != "" {
		name = real
	}
	res.ListFile = filepath.Join(dir.Name(), name)
	current, _, exists, err := readAt(dir, name)
	if err != nil {
		return fail(err)
	}
	content := original
	if exists {
		content = mergeRestoredModSettings(original, current, row.Existed, row.Owned, row.Adopted)
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
		if parsed, err := larian.ParseModSettings(content); err == nil {
			res.Files = len(parsed.Entries)
		}
	}
	if err := store.forget(row); err != nil {
		return fail(err)
	}
	return res
}

// mergeRestoredModSettings is the list a purge writes: current without the
// entries lmm owned, plus every entry of the kept original that current no
// longer has, after the entry it followed there, plus every entry lmm took
// over from the user (adopted) that neither has, as the user wrote it,
// after the entry it followed when lmm took it. The original's own bytes
// when that is exactly the original's list; the original when current is
// not a list lmm can read (the game cannot read it either).
func mergeRestoredModSettings(original, current []byte, existed bool, owned []string, adopted []adoptedEntry) []byte {
	cur, err := larian.ParseModSettings(current)
	if err != nil || cur.HasModOrder {
		if existed {
			return original
		}
		return current
	}
	isOwned := map[string]bool{}
	for _, u := range owned {
		isOwned[strings.ToLower(u)] = true
	}
	var orig *larian.ModSettings
	if existed {
		orig, _ = larian.ParseModSettings(original)
	}
	byKey := map[string]larian.ModEntry{}
	var keep []pluginNode
	stillThere := map[string]bool{}
	for i, e := range cur.Entries {
		key := modEntryKey(e, "c", i)
		if isOwned[strings.ToLower(e.UUID)] || stillThere[key] {
			continue
		}
		stillThere[key] = true
		byKey[key] = e
		keep = append(keep, pluginNode{name: key})
	}
	var only []pluginNode
	var baseEntries []listEntry
	var baseKeys []string
	if orig != nil {
		for i, e := range orig.Entries {
			key := modEntryKey(e, "o", i)
			baseEntries = append(baseEntries, listEntry{name: key})
			baseKeys = append(baseKeys, key)
			if !stillThere[key] {
				byKey[key] = e
				only = append(only, pluginNode{name: key})
			}
		}
	}
	ranks := mergedRanks(keep, only, baseEntries)
	all := append(slices.Clone(keep), only...)
	slices.SortStableFunc(all, func(a, b pluginNode) int { return ranks[pluginKey(a.name)] - ranks[pluginKey(b.name)] })
	keys := make([]string, len(all))
	entries := make([]larian.ModEntry, len(all))
	for i, n := range all {
		keys[i] = n.name
		entries[i] = byKey[n.name]
	}
	for _, a := range adopted {
		if slices.Contains(keys, a.UUID) {
			continue // the list, or the original, has it already
		}
		e, err := larian.EntryFromRaw([]byte(a.Entry))
		if err != nil {
			continue
		}
		at := len(keys)
		if i := slices.Index(keys, a.After); a.After != "" && i >= 0 {
			at = i + 1
		}
		keys = slices.Insert(keys, at, a.UUID)
		entries = slices.Insert(entries, at, e)
	}
	if existed && orig != nil && slices.Equal(keys, baseKeys) {
		return original
	}
	return cur.Render(entries)
}

// modEntryKey is an entry's identity across two versions of the list: its
// UUID, lower case; an entry with none is its own (doc, index).
func modEntryKey(e larian.ModEntry, doc string, index int) string {
	if e.UUID != "" {
		return strings.ToLower(e.UUID)
	}
	return fmt.Sprintf("\x00%s%d", doc, index)
}

// pakUnreadable is the finding for a pak whose module lmm cannot read.
func pakUnreadable(spec adapter.FileLoadOrderSpec, name string, err error) LoadOrderFinding {
	why := err.Error()
	if errors.Is(err, larian.ErrZstd) {
		why = "its meta.lsx is zstd-compressed, which lmm does not read"
	}
	return LoadOrderFinding{Code: LoadOrderFindingPakUnreadable, Severity: PluginSeverityWarning, File: name,
		Message: fmt.Sprintf("%s is not listed in %s, so the game will not load it: lmm could not read the module it declares (%s) - reinstall the mod, or enable it with BG3 Mod Manager", name, spec.ListFile, why)}
}

// splitRel is a slash-separated relative path's components.
func splitRel(rel string) []string {
	var out []string
	for _, c := range strings.Split(filepath.ToSlash(rel), "/") {
		if c != "" && c != "." {
			out = append(out, c)
		}
	}
	return out
}

// maxPakBytes caps a .pak lmm opens for its modules: the reader reads
// only the header, the file list and meta.lsx, so the cap only guards a
// size that cannot be a file (the format's own limit is 4 GiB per part).
const maxPakBytes = 1 << 40

// pakModules is the modules of the pak name in dir, cached by the file's
// identity, size and modification time: a deploy reads every deployed
// pak's file list, and they change only when a mod does. The pak may be a
// link (a symlink deploy into lmm's own cache); it is read, never written,
// and must end at a regular file.
func (s *Service) pakModules(dir *os.File, name string) ([]larian.Module, error) {
	fd, err := syscall.Openat(int(dir.Fd()), name, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: filepath.Join(dir.Name(), name), Err: err}
	}
	f := os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name))
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w", f.Name(), errNotRegularTarget)
	}
	if info.Size() > maxPakBytes {
		return nil, fmt.Errorf("%s is %d bytes, more than any .pak", f.Name(), info.Size())
	}
	key := pakMetaKey{size: info.Size(), mtime: info.ModTime().UnixNano()}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		key.dev, key.ino = uint64(st.Dev), st.Ino
	}
	s.pakMetaMu.Lock()
	cached, ok := s.pakMeta[key]
	s.pakMetaMu.Unlock()
	if ok {
		return slices.Clone(cached), nil
	}
	mods, err := larian.PakModules(f, info.Size())
	if err != nil {
		return nil, err
	}
	s.pakMetaMu.Lock()
	if s.pakMeta == nil || len(s.pakMeta) >= maxPakMetaCache {
		s.pakMeta = map[pakMetaKey][]larian.Module{}
	}
	s.pakMeta[key] = slices.Clone(mods)
	s.pakMetaMu.Unlock()
	return mods, nil
}

// pakMetaKey identifies a pak file's content for the module cache.
type pakMetaKey struct {
	dev, ino uint64
	size     int64
	mtime    int64
}

// maxPakMetaCache bounds the module cache; it is emptied when full.
const maxPakMetaCache = 8192
