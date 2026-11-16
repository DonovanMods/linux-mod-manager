package larian

import (
	"bytes"
	"testing"
)

// The reader's promise on hostile input (package comment): an error, never
// a panic, never an allocation past the caps. Run each for a minute with
//
//	go test ./internal/larian -run '^$' -fuzz FuzzPakModules -fuzztime 60s
//
// (and FuzzParseModSettings, FuzzLZ4). Plain `go test` runs the seeds.

func FuzzPakModules(f *testing.F) {
	meta := metaLSX("Mod", "A Mod", testUUID, "36028797018963968")
	for _, v := range []uint32{15, 16, 18} {
		for _, m := range []uint32{methodNone, methodZlib, methodLZ4, methodZstd} {
			f.Add(buildPak(f, pakOptions{version: v},
				testMember{name: "Mods/Mod/meta.lsx", data: meta, method: m},
				testMember{name: "Public/Mod/x", data: bytes.Repeat([]byte("ab"), 40), method: m}))
		}
	}
	f.Add(buildPak(f, pakOptions{version: 18, flags: flagSolid}, testMember{name: "Mods/M/meta.lsx", data: meta, method: methodLZ4}))
	f.Add(buildPak(f, pakOptions{version: 18}, testMember{name: "Mods/M/meta.lsx", data: meta, method: methodLZ4, part: 1}))
	f.Add([]byte("LSPK"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		mods, err := PakModules(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			if mods != nil {
				t.Fatal("a refused pak returned modules")
			}
			return
		}
		for _, m := range mods {
			if !ValidUUID(m.UUID) || m.Folder == "" || !validValue(m.Name) || !validValue(m.Folder) {
				t.Fatalf("an invalid module came back: %+v", m)
			}
		}
	})
}

func FuzzParseModSettings(f *testing.F) {
	f.Add([]byte(patch8))
	f.Add([]byte("<save><region id=\"ModuleSettings\"><node id=\"root\"><children><node id=\"Mods\"/></children></node></region></save>"))
	f.Add([]byte("\xEF\xBB\xBF<save><region id=\"ModuleSettings\"><node id=\"root\"><children><node id=\"ModOrder\"/><node id=\"Mods\"><children/></node></children></node></region></save>"))
	f.Add([]byte("<save/>"))
	f.Fuzz(func(t *testing.T, data []byte) {
		s, err := ParseModSettings(data)
		if err != nil {
			return
		}
		if !bytes.Equal(s.Render(s.Entries), data) {
			t.Fatal("a no-op render changed the file")
		}
		// Adding one entry gives a file that parses to one entry more, the
		// file's own first and as they were.
		mine := NewModEntry(Module{UUID: testUUID, Folder: "F<&\"'>", Name: "N", Version64: "1"})
		out := s.Render(append(append([]ModEntry(nil), s.Entries...), mine))
		again, err := ParseModSettings(out)
		if err != nil {
			t.Fatalf("a rendered file does not parse: %v\n%q", err, out)
		}
		if len(again.Entries) != len(s.Entries)+1 {
			t.Fatalf("%d entries became %d", len(s.Entries), len(again.Entries))
		}
		last := again.Entries[len(again.Entries)-1]
		if last.UUID != testUUID || last.Folder != "F<&\"'>" {
			t.Fatalf("the added entry reads back as %+v", last)
		}
	})
}

func FuzzLZ4(f *testing.F) {
	f.Add(lz4(bytes.Repeat([]byte("abcabcabc"), 30)), 300)
	f.Add([]byte{0x1F, 'a', 1, 0, 0xFF, 0xFF, 0x10}, 1000)
	f.Fuzz(func(t *testing.T, src []byte, room int) {
		if room < 0 || room > 1<<16 {
			return
		}
		dst := make([]byte, room)
		n, err := decodeLZ4Block(src, dst)
		if err == nil && (n < 0 || n > room) {
			t.Fatalf("wrote %d bytes into %d", n, room)
		}
	})
}
