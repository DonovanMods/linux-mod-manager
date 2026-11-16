package larian

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Module is one module a .pak declares in its meta.lsx: what the game's mod
// list names it by. A modsettings.lsx entry (ModuleShortDesc) is these
// fields [NMA; VXG loadOrder.ts exportTo].
type Module struct {
	// UUID identifies the module. The game and every mod manager match an
	// entry by it.
	UUID string
	// Folder is the module's folder, Mods/<Folder> inside the pak.
	Folder string
	// Name is its display name.
	Name string
	// Version64 is its packed 64-bit version, as decimal text.
	Version64 string
	// MD5 is the checksum the meta.lsx states, often empty.
	MD5 string
	// Meta is the meta.lsx member it was read from.
	Meta string
}

// ErrBadMeta: a meta.lsx that cannot name a module.
var ErrBadMeta = errors.New("unusable meta.lsx")

// maxMetaBytes caps a meta.lsx: a real one is a few KiB.
const maxMetaBytes = 1 << 20

// maxMetas caps how many meta.lsx files one pak may hold.
const maxMetas = 64

// maxValueLen caps an attribute value lmm copies into the mod list.
const maxValueLen = 1024

// ParseMeta reads a meta.lsx: the attributes of region Config's root
// node's ModuleInfo child [NMA "Pak Metadata"; VXG util.ts
// extractPakInfoImpl]. folder stands in for a missing Folder (it is the
// folder the meta.lsx sits in); Name falls back to Folder, and Version64
// to "1", as Vortex's do. A module with no usable UUID is refused: Vortex
// makes up a random one, which the game cannot match to anything.
func ParseMeta(data []byte, folder string) (Module, error) {
	doc, err := parseLSX(data)
	if err != nil {
		return Module{}, fmt.Errorf("%w: %v", ErrBadMeta, err)
	}
	region := doc.region("Config")
	if region == nil {
		return Module{}, fmt.Errorf("%w: no Config region", ErrBadMeta)
	}
	root := region.node("root")
	if root == nil {
		return Module{}, fmt.Errorf("%w: no root node", ErrBadMeta)
	}
	info := root.child("ModuleInfo")
	if info == nil {
		return Module{}, fmt.Errorf("%w: no ModuleInfo node", ErrBadMeta)
	}
	m := Module{}
	m.UUID, _ = info.attr("UUID")
	m.Folder, _ = info.attr("Folder")
	m.Name, _ = info.attr("Name")
	m.Version64, _ = info.attr("Version64")
	m.MD5, _ = info.attr("MD5")
	m.UUID = strings.TrimSpace(m.UUID)
	if !ValidUUID(m.UUID) {
		return Module{}, fmt.Errorf("%w: its UUID %q is not one", ErrBadMeta, m.UUID)
	}
	if m.Folder == "" {
		m.Folder = folder
	}
	if m.Name == "" {
		m.Name = m.Folder
	}
	if _, err := strconv.ParseInt(strings.TrimSpace(m.Version64), 10, 64); err != nil {
		m.Version64 = "1"
	} else {
		m.Version64 = strings.TrimSpace(m.Version64)
	}
	for _, v := range []string{m.Folder, m.Name, m.MD5} {
		if !validValue(v) {
			return Module{}, fmt.Errorf("%w: a value is longer than %d bytes or holds a control character", ErrBadMeta, maxValueLen)
		}
	}
	if m.Folder == "" {
		return Module{}, fmt.Errorf("%w: no Folder", ErrBadMeta)
	}
	return m, nil
}

// ValidUUID reports whether s can be a module UUID: 1 to 64 ASCII letters,
// digits and hyphens. The game and its toolkit write the 8-4-4-4-12 hex
// form, but a mod may not: one in the survey, among the most downloaded,
// ships "GOLDWEIG-HTZE-RO12-4444-deeeeeeeeeef", and the game loads it. So
// the test is only that the value is an identifier lmm can write back
// as it is.
func ValidUUID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !isIDRune(r) {
			return false
		}
	}
	return true
}

// isIDRune reports whether r may be in a module UUID: an ASCII letter, a
// digit or a hyphen.
func isIDRune(r rune) bool {
	return r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}

// validValue reports whether v can be written to the mod list as it is.
func validValue(v string) bool {
	return len(v) <= maxValueLen && !strings.ContainsFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7F || r == '�' })
}

// baseModules are the game's own modules, by lower-case UUID [BMM
// IgnoredMods.json]: the game loads them itself, and a mod that overrides
// one ships its meta.lsx (one popular mod in the survey carries ten of
// them beside its own). They are never a pak's module.
var baseModules = map[string]bool{
	"991c9c7a-fb80-40cb-8f0d-b92d4e80e9b1": true, // Gustav
	"28ac9ce2-2aba-8cda-b3b5-6e922f71b6b8": true, // GustavDev
	"cb555efe-2d9e-131f-8195-a89329d218ea": true, // GustavX
	"ed539163-bb70-431b-96a7-f5b2eda5376b": true, // Shared
	"3d0c5ff8-c95d-c907-ff3e-34b204f1c630": true, // SharedDev
	"9dff4c3b-fda7-43de-a763-ce1383039999": true, // Engine
	"e842840a-2449-588c-b0c4-22122cfce31b": true, // DiceSet_01
	"b176a0ac-d79f-ed9d-5a87-5c2c80874e10": true, // DiceSet_02
	"e0a4d990-7b9b-8fa9-d7c6-04017c6cf5b1": true, // DiceSet_03
	"77a2155f-4b35-4f0c-e7ff-4338f91426a4": true, // DiceSet_04
	"6efc8f44-cc2a-0273-d4b1-681d3faa411b": true, // DiceSet_05
	"ee4989eb-aab8-968f-8674-812ea2f4bfd7": true, // DiceSet_06
	"bf19bab4-4908-ef39-9065-ced469c0f877": true, // DiceSet_07
	"b77b6210-ac50-4cb1-a3d5-5702fb9c744c": true, // Honour
	"767d0062-d82c-279c-e16b-dfee7fe94cdd": true, // HonourX
	"ee5a55ff-eb38-0b27-c5b0-f358dc306d34": true, // ModBrowser
	"630daa32-70f8-3da5-41b9-154fe8410236": true, // MainUI
	"e1ce736b-52e6-e713-e9e7-e6abbb15a198": true, // CrossplayUI
	"55ef175c-59e3-b44b-3fb2-8f86acc5d550": true, // PhotoMode
}

// IsBaseModule reports whether the module uuid, named name, is one of the
// game's own: a UUID in the game's module table, or - Vortex's own test,
// which keeps the mod list's base entry when it rewrites the list [VXG
// loadOrder.ts exportTo] - a name starting with "Gustav".
func IsBaseModule(uuid, name string) bool {
	return baseModules[strings.ToLower(uuid)] || strings.HasPrefix(name, "Gustav")
}

// metaFolder is the module folder of a meta.lsx member - the <Folder> of
// Mods/<Folder>/meta.lsx, matched case-insensitively - and whether name is
// one. Every meta.lsx in the survey sat there; one elsewhere is not a
// module the game loads from the pak.
func metaFolder(name string) (string, bool) {
	parts := strings.Split(strings.ReplaceAll(name, `\`, "/"), "/")
	if len(parts) != 3 || !strings.EqualFold(parts[0], "Mods") || !strings.EqualFold(parts[2], "meta.lsx") {
		return "", false
	}
	if parts[1] == "" || parts[1] == "." || parts[1] == ".." {
		return "", false
	}
	return parts[1], true
}

// PakModules reads the modules the package r declares: every
// Mods/<Folder>/meta.lsx in its file list, the game's own modules left out
// (by UUID, from the game's module table), each UUID once. None, with no error, is a pak that
// declares no module of its own - an override pak, which the game loads
// without a mod-list entry, and which Vortex never lists [VXG cache.ts
// isLOListed; loadOrder.ts exportTo]. A meta.lsx that cannot be read is an
// error: the pak's module cannot be listed.
func PakModules(r io.ReaderAt, size int64) ([]Module, error) {
	p, err := OpenPak(r, size)
	if err != nil {
		return nil, err
	}
	var out []Module
	seen := map[string]bool{}
	metas := 0
	for _, f := range p.Files {
		folder, ok := metaFolder(f.Name)
		if !ok {
			continue
		}
		metas++
		if metas > maxMetas {
			return nil, fmt.Errorf("%w: more than %d meta.lsx files", ErrCorrupt, maxMetas)
		}
		data, err := p.ReadFile(f, maxMetaBytes)
		if err != nil {
			return nil, err
		}
		m, err := ParseMeta(data, folder)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
		if baseModules[strings.ToLower(m.UUID)] {
			continue // a mod's copy of one of the game's own (UUID only: a mod may be named "Gustav...")
		}
		key := strings.ToLower(m.UUID)
		if seen[key] {
			continue
		}
		seen[key] = true
		m.Meta = f.Name
		out = append(out, m)
	}
	return out, nil
}
