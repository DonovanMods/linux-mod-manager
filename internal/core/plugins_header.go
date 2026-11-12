package core

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
)

// The TES4 plugin header (#606, design docs/plans/2026-11-10-collections-
// design.md §5): the one record a plugin sort needs from each plugin. Its
// layout, for every game from Skyrim (2011) on, is (facts read from
// esplugin's src/record.rs and src/plugin.rs, GPL; no code taken):
//
//	record header, 24 bytes: type "TES4", data size u32, flags u32,
//	  form id u32, version-control info u32, form version u16 + 2 bytes
//	subrecords, filling the data size: type (4 bytes), data size u16, data
//	  - "MAST" is one master's file name, NUL-terminated, Windows-1252
//	  - "XXXX" carries a u32 that replaces the NEXT subrecord's u16 size,
//	    for a subrecord too big for one (Skyrim's large ONAM lists)
//
// Only the flags and the masters are read; everything else is skipped
// without being buffered, so a hostile size costs a read, not memory.

// ErrPluginHeader is the sentinel every ReadPluginHeader refusal wraps.
var ErrPluginHeader = errors.New("not a readable plugin header")

const (
	// tes4HeaderSize is the record header's length in every TES4 game
	// since Skyrim.
	tes4HeaderSize = 24
	// maxHeaderRecord bounds the header record's declared size. Real ones
	// are kilobytes; this only refuses a size no plugin has.
	maxHeaderRecord = 64 << 20
	// maxMasterName bounds one master's file name in bytes.
	maxMasterName = 1024
	// compressedFlag marks a compressed record, which a header never is.
	compressedFlag = 0x00040000
)

// PluginHeader is what a plugin's TES4 header says about load order.
type PluginHeader struct {
	// Flags is the header record's flags word; what each bit means is the
	// game's (adapter.PluginSpec), applied by ClassifyPlugin.
	Flags uint32
	// Masters are the plugin's masters, in file order, as UTF-8.
	Masters []string
}

// pluginHeaderError wraps ErrPluginHeader with what was wrong.
func pluginHeaderError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrPluginHeader, fmt.Sprintf(format, args...))
}

// ReadPluginHeader reads the TES4 header record at the start of r: its
// flags and its masters. It reads nothing past the header record. Every
// refusal wraps ErrPluginHeader: a file that is not a TES4 plugin
// (Morrowind's TES3 included), a truncated or inconsistent header, or a
// compressed one.
func ReadPluginHeader(r io.Reader) (PluginHeader, error) {
	var hdr [tes4HeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return PluginHeader{}, pluginHeaderError("the file is shorter than a record header")
	}
	if typ := string(hdr[:4]); typ != "TES4" {
		return PluginHeader{}, pluginHeaderError("the first record is %q, not TES4", typ)
	}
	size := binary.LittleEndian.Uint32(hdr[4:8])
	flags := binary.LittleEndian.Uint32(hdr[8:12])
	if size > maxHeaderRecord {
		return PluginHeader{}, pluginHeaderError("the header record claims %d bytes", size)
	}
	if flags&compressedFlag != 0 {
		return PluginHeader{}, pluginHeaderError("the header record is compressed")
	}

	out := PluginHeader{Flags: flags}
	body := io.LimitReader(r, int64(size))
	remaining := int64(size)
	var override int64 = -1 // an XXXX subrecord's size for the next one
	for remaining > 0 {
		var sh [6]byte
		if remaining < int64(len(sh)) {
			return PluginHeader{}, pluginHeaderError("a subrecord header does not fit in the header record")
		}
		if _, err := io.ReadFull(body, sh[:]); err != nil {
			return PluginHeader{}, pluginHeaderError("the header record is truncated")
		}
		remaining -= int64(len(sh))
		typ := string(sh[:4])
		n := int64(binary.LittleEndian.Uint16(sh[4:6]))
		if override >= 0 {
			n, override = override, -1
		}
		if n > remaining {
			return PluginHeader{}, pluginHeaderError("subrecord %q runs past the header record", typ)
		}
		remaining -= n
		switch typ {
		case "XXXX":
			if n != 4 {
				return PluginHeader{}, pluginHeaderError("an XXXX subrecord holds %d bytes, not 4", n)
			}
			var v [4]byte
			if _, err := io.ReadFull(body, v[:]); err != nil {
				return PluginHeader{}, pluginHeaderError("the header record is truncated")
			}
			override = int64(binary.LittleEndian.Uint32(v[:]))
			continue
		case "MAST":
			if n > maxMasterName {
				return PluginHeader{}, pluginHeaderError("a master's name is %d bytes long", n)
			}
			raw := make([]byte, n)
			if _, err := io.ReadFull(body, raw); err != nil {
				return PluginHeader{}, pluginHeaderError("the header record is truncated")
			}
			if i := strings.IndexByte(string(raw), 0); i >= 0 {
				raw = raw[:i]
			}
			if len(raw) == 0 {
				return PluginHeader{}, pluginHeaderError("a master has an empty name")
			}
			out.Masters = append(out.Masters, decodeWindows1252(raw))
		default:
			if _, err := io.CopyN(io.Discard, body, n); err != nil {
				return PluginHeader{}, pluginHeaderError("the header record is truncated")
			}
		}
	}
	if override >= 0 {
		return PluginHeader{}, pluginHeaderError("an XXXX subrecord is the last in the header record")
	}
	return out, nil
}

// windows1252High maps bytes 0x80-0x9F, the only range where Windows-1252
// differs from Latin-1. The five bytes Windows-1252 leaves undefined (0x81,
// 0x8D, 0x8F, 0x90, 0x9D) map to the C1 control of the same value, as
// Windows' own conversion does.
var windows1252High = [32]rune{
	'€', 0x81, '‚', 'ƒ', '„', '…', '†', '‡', 'ˆ', '‰', 'Š', '‹', 'Œ', 0x8D, 'Ž', 0x8F,
	0x90, '‘', '’', '“', '”', '•', '–', '—', '˜', '™', 'š', '›', 'œ', 0x9D, 'ž', 'Ÿ',
}

// decodeWindows1252 converts Windows-1252 bytes to a UTF-8 string.
func decodeWindows1252(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c < 0xA0:
			sb.WriteRune(windows1252High[c-0x80])
		default:
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

// PluginClass is what a plugin is, for ordering and for the active limits.
type PluginClass struct {
	// Master is a master file: it loads before every non-master.
	Master bool
	// Light is a light (small) plugin, counted against the light limit.
	Light bool
	// Medium is a medium plugin (Starfield), counted against the medium
	// limit. Never true together with Light.
	Medium bool
	// Blueprint is a Starfield blueprint plugin, which the game delists
	// and orders itself.
	Blueprint bool
}

// ClassifyPlugin applies a game's rules to a plugin's file name and header.
// The rules are esplugin's (src/plugin.rs, read for behaviour): a master
// has the master flag or a master-implying extension; a plugin is light
// when it has the light flag, or a light-implying extension without the
// update flag; a medium flag is ignored on a light plugin.
func ClassifyPlugin(name string, h PluginHeader, spec adapter.PluginSpec) PluginClass {
	ext := strings.ToLower(path.Ext(name))
	has := func(bit uint32) bool { return bit != 0 && h.Flags&bit != 0 }
	extIn := func(list []string) bool {
		for _, e := range list {
			if strings.EqualFold(e, ext) {
				return true
			}
		}
		return false
	}
	var c PluginClass
	c.Master = has(spec.MasterFlag) || extIn(spec.ExtImpliesMaster)
	if spec.LightFlag != 0 {
		c.Light = has(spec.LightFlag) || (extIn(spec.ExtImpliesLight) && !has(spec.UpdateFlag))
	}
	c.Medium = has(spec.MediumFlag) && !c.Light
	c.Blueprint = has(spec.BlueprintFlag)
	return c
}
