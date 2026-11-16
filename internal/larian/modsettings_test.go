package larian

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// patch8 is the mod list as the game writes it on Patch 8, with GustavX
// and one module a user enabled ([VXG common.ts DEFAULT_MOD_SETTINGS_V8],
// [NMA]'s example entry).
const patch8 = `<?xml version="1.0" encoding="UTF-8"?>
<save>
    <version major="4" minor="8" revision="0" build="10"/>
    <region id="ModuleSettings">
        <node id="root">
            <children>
                <node id="Mods">
                    <children>
                        <node id="ModuleShortDesc">
                            <attribute id="Folder" type="LSString" value="GustavX"/>
                            <attribute id="MD5" type="LSString" value=""/>
                            <attribute id="Name" type="LSString" value="GustavX"/>
                            <attribute id="PublishHandle" type="uint64" value="0"/>
                            <attribute id="UUID" type="guid" value="cb555efe-2d9e-131f-8195-a89329d218ea"/>
                            <attribute id="Version64" type="int64" value="36028797018963968"/>
                        </node>
                        <node id="ModuleShortDesc">
                            <attribute id="Folder" type="LSString" value="BG3MCM"/>
                            <attribute id="MD5" type="LSString" value=""/>
                            <attribute id="Name" type="LSString" value="Mod Configuration Menu"/>
                            <attribute id="PublishHandle" type="uint64" value="0"/>
                            <attribute id="UUID" type="guid" value="755a8a72-407f-4f0d-9a33-274ac0f0b53d"/>
                            <attribute id="Version64" type="int64" value="38280596832649216"/>
                        </node>
                    </children>
                </node>
            </children>
        </node>
    </region>
</save>`

func TestParseModSettings_ReadsTheEntriesInOrder(t *testing.T) {
	s, err := ParseModSettings([]byte(patch8))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 2 || s.Entries[0].Name != "GustavX" || s.Entries[1].UUID != "755a8a72-407f-4f0d-9a33-274ac0f0b53d" || s.HasModOrder {
		t.Fatalf("got %+v", s.Entries)
	}
	if !s.Entries[0].FromFile() {
		t.Fatal("a parsed entry is the file's own")
	}
}

func TestRender_TheFilesOwnEntriesGiveBackItsBytes(t *testing.T) {
	s, err := ParseModSettings([]byte(patch8))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Render(s.Entries); string(got) != patch8 {
		t.Fatalf("a no-op render changed the file:\n%s", got)
	}
}

// The round trip #646 asks for: lmm's module goes in after the user's, both
// of the file's own entries are kept byte for byte, every byte outside the
// mod list is kept, and the result parses back to the same list.
func TestRender_AddsAnEntryAndKeepsEverythingElse(t *testing.T) {
	s, err := ParseModSettings([]byte(patch8))
	if err != nil {
		t.Fatal(err)
	}
	mine := NewModEntry(Module{UUID: testUUID, Folder: "ImpUI_x", Name: `Imp "UI" & more`, Version64: "1", MD5: "abc"})
	out := s.Render([]ModEntry{s.Entries[0], s.Entries[1], mine})
	again, err := ParseModSettings(out)
	if err != nil {
		t.Fatalf("the rendered file does not parse: %v\n%s", err, out)
	}
	if len(again.Entries) != 3 || again.Entries[2].Name != `Imp "UI" & more` || again.Entries[2].UUID != testUUID || again.Entries[0].Name != "GustavX" {
		t.Fatalf("got %+v", again.Entries)
	}
	if !bytes.HasPrefix(out, []byte(patch8[:strings.Index(patch8, `<node id="Mods">`)])) || !bytes.HasSuffix(out, []byte(patch8[strings.Index(patch8, "            </children>\n        </node>\n    </region>"):])) {
		t.Fatalf("bytes outside the mod list changed:\n%s", out)
	}
	for _, e := range s.Entries {
		if !bytes.Contains(out, e.raw) {
			t.Fatalf("the file's entry %s was not kept as it was", e.Name)
		}
	}
	want := `                        <node id="ModuleShortDesc">
                            <attribute id="Folder" type="LSString" value="ImpUI_x"/>
                            <attribute id="MD5" type="LSString" value="abc"/>
                            <attribute id="Name" type="LSString" value="Imp &quot;UI&quot; &amp; more"/>
                            <attribute id="PublishHandle" type="uint64" value="0"/>
                            <attribute id="UUID" type="guid" value="` + testUUID + `"/>
                            <attribute id="Version64" type="int64" value="1"/>
                        </node>`
	if !strings.Contains(string(out), want) {
		t.Fatalf("the new entry is not written as the game writes one:\n%s", out)
	}
	if again.Entries[2].Matches(Module{UUID: strings.ToUpper(testUUID), Folder: "ImpUI_x", Name: `Imp "UI" & more`, Version64: "1", MD5: "abc"}) == false {
		t.Fatal("the written entry does not match its module")
	}
}

func TestRender_RemovesAndReordersKeepingLineEndings(t *testing.T) {
	crlf := strings.ReplaceAll(patch8, "\n", "\r\n")
	s, err := ParseModSettings([]byte(crlf))
	if err != nil {
		t.Fatal(err)
	}
	out := s.Render([]ModEntry{s.Entries[0]})
	if bytes.Contains(bytes.ReplaceAll(out, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatalf("a bare LF in a CRLF file:\n%q", out)
	}
	again, err := ParseModSettings(out)
	if err != nil || len(again.Entries) != 1 || again.Entries[0].Name != "GustavX" {
		t.Fatalf("got %+v, %v", again, err)
	}
}

// A Mods node with no children element - as an empty list may be written -
// takes entries too.
func TestRender_FillsAnEmptyModsNode(t *testing.T) {
	doc := "<save>\n\t<region id=\"ModuleSettings\">\n\t\t<node id=\"root\">\n\t\t\t<children>\n\t\t\t\t<node id=\"Mods\"/>\n\t\t\t</children>\n\t\t</node>\n\t</region>\n</save>\n"
	s, err := ParseModSettings([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	out := s.Render([]ModEntry{NewModEntry(Module{UUID: testUUID, Folder: "F", Name: "N", Version64: "1"})})
	again, err := ParseModSettings(out)
	if err != nil || len(again.Entries) != 1 {
		t.Fatalf("got %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "\n\t\t\t\t\t\t<node id=\"ModuleShortDesc\">") {
		t.Fatalf("the file's tab indentation was not followed:\n%s", out)
	}
}

func TestParseModSettings_ReportsThePrePatch7Format(t *testing.T) {
	v6 := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<save>
  <version major="4" minor="0" revision="10" build="100"/>
  <region id="ModuleSettings">
    <node id="root">
      <children>
        <node id="ModOrder">
          <children/>
        </node>
        <node id="Mods">
          <children>
            <node id="ModuleShortDesc">
              <attribute id="Name" type="LSString" value="GustavDev"/>
              <attribute id="UUID" type="FixedString" value="28ac9ce2-2aba-8cda-b3b5-6e922f71b6b8"/>
            </node>
          </children>
        </node>
      </children>
    </node>
  </region>
</save>`
	s, err := ParseModSettings([]byte(v6))
	if err != nil || !s.HasModOrder {
		t.Fatalf("got %+v, %v", s, err)
	}
}

func TestParseModSettings_Refuses(t *testing.T) {
	for name, doc := range map[string]string{
		"no region":   `<save><region id="Config"/></save>`,
		"no mods":     `<save><region id="ModuleSettings"><node id="root"/></region></save>`,
		"not xml":     "\x00\x01",
		"two roots":   `<save></save><save></save>`,
		"wrong root":  `<config/>`,
		"bad entity":  `<!DOCTYPE save [<!ENTITY e "x">]><save>&e;</save>`,
		"mismatched":  `<save><region></save>`,
		"deep":        "<save>" + strings.Repeat("<a>", 100) + strings.Repeat("</a>", 100) + "</save>",
		"nothing":     "",
		"only header": `<?xml version="1.0"?>`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseModSettings([]byte(doc))
			if err == nil {
				t.Fatal("parsed")
			}
			if !errors.Is(err, ErrNoModList) && !errors.Is(err, errLSX) {
				t.Fatalf("unexpected error kind: %v", err)
			}
		})
	}
}

// An entry read from another file - a kept original - is written as its
// own bytes, and is never taken for this file's entry at the same place.
func TestRender_AnEntryFromAnotherFileIsNotThisFilesOwn(t *testing.T) {
	current, err := ParseModSettings([]byte(patch8))
	if err != nil {
		t.Fatal(err)
	}
	other := strings.Replace(patch8, "Mod Configuration Menu", "Another Menu", 1)
	original, err := ParseModSettings([]byte(other))
	if err != nil {
		t.Fatal(err)
	}
	out := current.Render([]ModEntry{current.Entries[0], original.Entries[1]})
	if string(out) == patch8 || !strings.Contains(string(out), "Another Menu") {
		t.Fatalf("the other file's entry was taken for this file's own:\n%s", out)
	}
}

// An entry's bytes, kept and read back, render as those same bytes.
func TestEntryFromRaw_RoundTrips(t *testing.T) {
	s, err := ParseModSettings([]byte(patch8))
	if err != nil {
		t.Fatal(err)
	}
	raw := s.Entries[1].Raw()
	e, err := EntryFromRaw(raw)
	if err != nil || e.UUID != s.Entries[1].UUID || !bytes.Equal(e.Raw(), raw) {
		t.Fatalf("got %+v, %v", e, err)
	}
	out := s.Render([]ModEntry{s.Entries[0], e})
	if !bytes.Contains(out, raw) {
		t.Fatalf("the kept entry was not written as its bytes:\n%s", out)
	}
	for _, bad := range []string{"", "<node id=\"ModuleShortDesc\"/><node id=\"ModuleShortDesc\"/>", "</children>"} {
		if _, err := EntryFromRaw([]byte(bad)); err == nil {
			t.Errorf("%q read as one entry", bad)
		}
	}
}
