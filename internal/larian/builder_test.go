package larian

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"testing"
)

// The tests build every pak they read: no real game file is in the
// repository. buildPak writes the LSPK layout this package reads (see the
// package comment for the sources), compressing each member as asked; lz4
// is a small greedy LZ4 block compressor, so the decoder meets real
// back-references and not only literals.

// testMember is one file of a synthetic pak.
type testMember struct {
	name   string
	data   []byte
	method uint32 // methodNone, methodZlib, methodLZ4, methodZstd
	part   uint32 // archive part; 0 is the .pak itself
}

// pakOptions shape a synthetic pak beyond its members.
type pakOptions struct {
	version uint32
	flags   byte
}

// buildPak is a pak of members in the given version.
func buildPak(t testing.TB, opts pakOptions, members ...testMember) []byte {
	t.Helper()
	headerSize := 4 + headerV15Size
	if opts.version >= 16 {
		headerSize = 4 + headerV16Size
	}
	var data bytes.Buffer
	type placed struct {
		m              testMember
		off, disk, raw uint64
	}
	var files []placed
	for _, m := range members {
		stored := m.data
		switch m.method {
		case methodZlib:
			var b bytes.Buffer
			w := zlib.NewWriter(&b)
			_, _ = w.Write(m.data)
			_ = w.Close()
			stored = b.Bytes()
		case methodLZ4:
			stored = lz4(m.data)
		case methodZstd:
			stored = append([]byte{0x28, 0xb5, 0x2f, 0xfd}, m.data...) // a zstd magic; never decoded
		}
		raw := uint64(len(m.data))
		if m.method == methodNone {
			raw = 0 // LSLib writes 0 for a stored file
		}
		files = append(files, placed{m: m, off: uint64(headerSize + data.Len()), disk: uint64(len(stored)), raw: raw})
		data.Write(stored)
	}
	var list bytes.Buffer
	for _, f := range files {
		name := make([]byte, nameSize)
		copy(name, f.m.name)
		list.Write(name)
		switch opts.version {
		case 18:
			_ = binary.Write(&list, binary.LittleEndian, uint32(f.off&0xffffffff))
			_ = binary.Write(&list, binary.LittleEndian, uint16(f.off>>32))
			list.WriteByte(byte(f.m.part))
			list.WriteByte(byte(f.m.method | 0x20))
			_ = binary.Write(&list, binary.LittleEndian, uint32(f.disk))
			_ = binary.Write(&list, binary.LittleEndian, uint32(f.raw))
		default:
			_ = binary.Write(&list, binary.LittleEndian, f.off)
			_ = binary.Write(&list, binary.LittleEndian, f.disk)
			_ = binary.Write(&list, binary.LittleEndian, f.raw)
			_ = binary.Write(&list, binary.LittleEndian, f.m.part)
			_ = binary.Write(&list, binary.LittleEndian, f.m.method|0x20)
			_ = binary.Write(&list, binary.LittleEndian, uint32(0)) // crc
			_ = binary.Write(&list, binary.LittleEndian, uint32(0)) // unknown
		}
	}
	compressedList := lz4(list.Bytes())
	listOffset := uint64(headerSize + data.Len())

	var out bytes.Buffer
	out.WriteString("LSPK")
	_ = binary.Write(&out, binary.LittleEndian, opts.version)
	_ = binary.Write(&out, binary.LittleEndian, listOffset)
	_ = binary.Write(&out, binary.LittleEndian, uint32(8+len(compressedList)))
	out.WriteByte(opts.flags)
	out.WriteByte(0)            // priority
	out.Write(make([]byte, 16)) // md5
	if opts.version >= 16 {
		_ = binary.Write(&out, binary.LittleEndian, uint16(1))
	}
	if out.Len() != headerSize {
		t.Fatalf("header is %d bytes, want %d", out.Len(), headerSize)
	}
	out.Write(data.Bytes())
	_ = binary.Write(&out, binary.LittleEndian, int32(len(files)))
	_ = binary.Write(&out, binary.LittleEndian, int32(len(compressedList)))
	out.Write(compressedList)
	return out.Bytes()
}

// lz4 compresses src as one LZ4 block: greedy 4-byte matches through a
// small hash table, the format's end conditions kept (the last five bytes
// are literals, and no match starts within the last twelve).
func lz4(src []byte) []byte {
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

// metaLSX is a meta.lsx for one module, in the layout the BG3 toolkit and
// LSLib write (NexusMods.App's BG3 developer notes quote one).
func metaLSX(folder, name, uuid, version64 string) []byte {
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
