// Package lariantest builds synthetic Larian files for tests: .pak
// packages, meta.lsx and modsettings.lsx documents, in the layouts
// internal/larian reads (its package comment cites them). No real game
// file is in the repository; every test makes its own with this package.
// It is the standard library only, and test code alone imports it.
package lariantest

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"strings"
)

// Compression methods for Member.Method, and the package flag for a solid
// package.
const (
	MethodNone = 0
	MethodZlib = 1
	MethodLZ4  = 2
	MethodZstd = 3
	FlagSolid  = 0x04
)

// Member is one file of a synthetic pak.
type Member struct {
	Name   string
	Data   []byte
	Method uint32 // MethodNone, MethodZlib, MethodLZ4 or MethodZstd
	Part   uint32 // archive part; 0 is the .pak itself
}

// Pak is a package of members in the given version (15, 16 or 18), with
// the given header flags. A zstd member is stored as a zstd magic and its
// bytes: never decodable, only recognisable by its flags.
func Pak(version uint32, flags byte, members ...Member) []byte {
	headerSize := 4 + 34
	if version >= 16 {
		headerSize = 4 + 36
	}
	var data bytes.Buffer
	type placed struct {
		m              Member
		off, disk, raw uint64
	}
	var files []placed
	for _, m := range members {
		stored := m.Data
		switch m.Method {
		case MethodZlib:
			var b bytes.Buffer
			w := zlib.NewWriter(&b)
			_, _ = w.Write(m.Data)
			_ = w.Close()
			stored = b.Bytes()
		case MethodLZ4:
			stored = LZ4(m.Data)
		case MethodZstd:
			stored = append([]byte{0x28, 0xb5, 0x2f, 0xfd}, m.Data...)
		}
		raw := uint64(len(m.Data))
		if m.Method == MethodNone {
			raw = 0 // LSLib writes 0 for a stored file
		}
		files = append(files, placed{m: m, off: uint64(headerSize + data.Len()), disk: uint64(len(stored)), raw: raw})
		data.Write(stored)
	}
	var list bytes.Buffer
	for _, f := range files {
		name := make([]byte, 256)
		copy(name, f.m.Name)
		list.Write(name)
		if version == 18 {
			_ = binary.Write(&list, binary.LittleEndian, uint32(f.off&0xffffffff))
			_ = binary.Write(&list, binary.LittleEndian, uint16(f.off>>32))
			list.WriteByte(byte(f.m.Part))
			list.WriteByte(byte(f.m.Method | 0x20))
			_ = binary.Write(&list, binary.LittleEndian, uint32(f.disk))
			_ = binary.Write(&list, binary.LittleEndian, uint32(f.raw))
			continue
		}
		_ = binary.Write(&list, binary.LittleEndian, f.off)
		_ = binary.Write(&list, binary.LittleEndian, f.disk)
		_ = binary.Write(&list, binary.LittleEndian, f.raw)
		_ = binary.Write(&list, binary.LittleEndian, f.m.Part)
		_ = binary.Write(&list, binary.LittleEndian, f.m.Method|0x20)
		_ = binary.Write(&list, binary.LittleEndian, uint32(0)) // crc
		_ = binary.Write(&list, binary.LittleEndian, uint32(0)) // unknown
	}
	compressedList := LZ4(list.Bytes())
	listOffset := uint64(headerSize + data.Len())

	var out bytes.Buffer
	out.WriteString("LSPK")
	_ = binary.Write(&out, binary.LittleEndian, version)
	_ = binary.Write(&out, binary.LittleEndian, listOffset)
	_ = binary.Write(&out, binary.LittleEndian, uint32(8+len(compressedList)))
	out.WriteByte(flags)
	out.WriteByte(0)            // priority
	out.Write(make([]byte, 16)) // md5
	if version >= 16 {
		_ = binary.Write(&out, binary.LittleEndian, uint16(1))
	}
	if out.Len() != headerSize {
		panic(fmt.Sprintf("lariantest: header is %d bytes, want %d", out.Len(), headerSize))
	}
	out.Write(data.Bytes())
	_ = binary.Write(&out, binary.LittleEndian, int32(len(files)))
	_ = binary.Write(&out, binary.LittleEndian, int32(len(compressedList)))
	out.Write(compressedList)
	return out.Bytes()
}

// ModPak is a v18, LZ4-compressed mod pak declaring one module - folder,
// name and uuid, version 1 - with one content file beside its meta.lsx.
func ModPak(folder, name, uuid string) []byte {
	return Pak(18, 0,
		Member{Name: "Mods/" + folder + "/meta.lsx", Data: MetaLSX(folder, name, uuid, "36028797018963968"), Method: MethodLZ4},
		Member{Name: "Public/" + folder + "/Stats/Generated/Data/x.txt", Data: []byte(strings.Repeat("data ", 20)), Method: MethodLZ4},
	)
}

// OverridePak is a v18 pak with no meta.lsx: it only replaces game files.
func OverridePak() []byte {
	return Pak(18, 0, Member{Name: "Public/Game/GUI/override.lsx", Data: []byte("<save/>"), Method: MethodLZ4})
}

// LZ4 compresses src as one LZ4 block: greedy 4-byte matches through a
// small hash table, the format's end conditions kept (the last five bytes
// are literals, and no match starts within the last twelve).
func LZ4(src []byte) []byte {
	var out []byte
	table := map[uint32]int{}
	anchor, i := 0, 0
	limit := len(src) - 12
	emit := func(litEnd, matchLen, offset int) {
		lit := litEnd - anchor
		token := byte(0)
		if lit >= 15 {
			token = 0xF0
		} else {
			token = byte(lit << 4)
		}
		ml := matchLen - 4
		if matchLen > 0 {
			if ml >= 15 {
				token |= 0x0F
			} else {
				token |= byte(ml)
			}
		}
		out = append(out, token)
		if lit >= 15 {
			n := lit - 15
			for n >= 255 {
				out = append(out, 255)
				n -= 255
			}
			out = append(out, byte(n))
		}
		out = append(out, src[anchor:litEnd]...)
		if matchLen == 0 {
			return
		}
		out = append(out, byte(offset), byte(offset>>8))
		if ml >= 15 {
			n := ml - 15
			for n >= 255 {
				out = append(out, 255)
				n -= 255
			}
			out = append(out, byte(n))
		}
	}
	for i < limit {
		seq := binary.LittleEndian.Uint32(src[i:])
		cand, ok := table[seq]
		table[seq] = i
		if ok && i-cand <= 0xFFFF && binary.LittleEndian.Uint32(src[cand:]) == seq {
			n := 4
			for i+n < len(src)-5 && src[cand+n] == src[i+n] {
				n++
			}
			emit(i, n, i-cand)
			i += n
			anchor = i
			continue
		}
		i++
	}
	emit(len(src), 0, 0)
	return out
}

// MetaLSX is a meta.lsx for one module, in the layout the BG3 toolkit and
// LSLib write (NexusMods.App's BG3 developer notes quote one).
func MetaLSX(folder, name, uuid, version64 string) []byte {
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<save>
  <version major="4" minor="0" revision="0" build="49" />
  <region id="Config">
    <node id="root">
      <children>
        <node id="Dependencies">
          <children>
            <node id="ModuleShortDesc">
              <attribute id="Folder" type="LSWString" value="SomeDependency" />
              <attribute id="UUID" type="FixedString" value="f97b43be-7398-4ea5-8fe2-be7eb3d4b5ca" />
              <attribute id="Version64" type="int64" value="1" />
            </node>
          </children>
        </node>
        <node id="ModuleInfo">
          <attribute id="Author" type="LSString" value="Someone" />
          <attribute id="Folder" type="LSString" value="%s" />
          <attribute id="MD5" type="LSString" value="" />
          <attribute id="Name" type="FixedString" value="%s" />
          <attribute id="UUID" type="FixedString" value="%s" />
          <attribute id="Version64" type="int64" value="%s" />
          <children>
            <node id="PublishVersion">
              <attribute id="Version64" type="int64" value="999" />
            </node>
          </children>
        </node>
      </children>
    </node>
  </region>
</save>
`, folder, name, uuid, version64))
}

// Entry is one modsettings.lsx entry.
type Entry struct {
	Folder, Name, UUID, Version64 string
}

// GustavX is the game's own entry on Patch 8 [VXG common.ts].
var GustavX = Entry{Folder: "GustavX", Name: "GustavX", UUID: "cb555efe-2d9e-131f-8195-a89329d218ea", Version64: "36028797018963968"}

// ModSettings is a Patch 8 modsettings.lsx listing entries, in the game's
// own layout: four-space indentation, LF line endings.
func ModSettings(entries ...Entry) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<save>
    <version major="4" minor="8" revision="0" build="10"/>
    <region id="ModuleSettings">
        <node id="root">
            <children>
                <node id="Mods">
                    <children>
`)
	for _, e := range entries {
		fmt.Fprintf(&b, `                        <node id="ModuleShortDesc">
                            <attribute id="Folder" type="LSString" value="%s"/>
                            <attribute id="MD5" type="LSString" value=""/>
                            <attribute id="Name" type="LSString" value="%s"/>
                            <attribute id="PublishHandle" type="uint64" value="0"/>
                            <attribute id="UUID" type="guid" value="%s"/>
                            <attribute id="Version64" type="int64" value="%s"/>
                        </node>
`, e.Folder, e.Name, e.UUID, e.Version64)
	}
	b.WriteString(`                    </children>
                </node>
            </children>
        </node>
    </region>
</save>`)
	return []byte(b.String())
}
