package larian

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestParseMeta_ReadsModuleInfoNotItsChildrenOrDependencies(t *testing.T) {
	m, err := ParseMeta(metaLSX("ImpUI", "ImpUI (ImprovedUI)", testUUID, "36028797018963970"), "Fallback")
	if err != nil {
		t.Fatal(err)
	}
	want := Module{UUID: testUUID, Folder: "ImpUI", Name: "ImpUI (ImprovedUI)", Version64: "36028797018963970"}
	if m != want {
		t.Fatalf("got %+v, want %+v (the dependency's UUID and PublishVersion's Version64 are not the module's)", m, want)
	}
}

func TestParseMeta_FallsBackAsVortexDoes(t *testing.T) {
	data := []byte(`<save><region id="Config"><node id="root"><children><node id="ModuleInfo">` +
		`<attribute id="UUID" type="FixedString" value="` + testUUID + `"/>` +
		`</node></children></node></region></save>`)
	m, err := ParseMeta(append(bytes.Clone(utf8BOM), data...), "FolderInPak")
	if err != nil {
		t.Fatal(err)
	}
	if m.Folder != "FolderInPak" || m.Name != "FolderInPak" || m.Version64 != "1" {
		t.Fatalf("got %+v", m)
	}
}

func TestParseMeta_Refuses(t *testing.T) {
	cases := map[string]string{
		"no uuid":        `<save><region id="Config"><node id="root"><children><node id="ModuleInfo"><attribute id="Folder" value="x"/></node></children></node></region></save>`,
		"made-up uuid":   `<save><region id="Config"><node id="root"><children><node id="ModuleInfo"><attribute id="UUID" value="not a uuid"/></node></children></node></region></save>`,
		"no module info": `<save><region id="Config"><node id="root"/></region></save>`,
		"not lsx":        `{"json": true}`,
		"control char":   `<save><region id="Config"><node id="root"><children><node id="ModuleInfo"><attribute id="UUID" value="` + testUUID + `"/><attribute id="Name" value="a&#x1;b"/></node></children></node></region></save>`,
		"utf-16":         `<?xml version="1.0" encoding="UTF-16"?><save/>`,
		"unclosed":       `<save><region id="Config">`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMeta([]byte(doc), "f"); !errors.Is(err, ErrBadMeta) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestPakModules_FindsTheModuleAndSkipsTheGamesOwn(t *testing.T) {
	// A mod that overrides game modules ships their meta.lsx beside its own
	// (one popular mod in the survey carries ten).
	pak := buildPak(t, pakOptions{version: 18},
		testMember{name: "Mods/GustavX/meta.lsx", data: metaLSX("GustavX", "GustavX", "cb555efe-2d9e-131f-8195-a89329d218ea", "1"), method: methodLZ4},
		testMember{name: "Mods/Shared/meta.lsx", data: metaLSX("Shared", "Shared", "ED539163-BB70-431B-96A7-F5B2EDA5376B", "1"), method: methodLZ4},
		testMember{name: "Mods/PartyLimit/meta.lsx", data: metaLSX("PartyLimit", "Party Limit", testUUID, "5"), method: methodLZ4},
		testMember{name: "Public/PartyLimit/Stats/x.txt", data: []byte("x"), method: methodLZ4},
		testMember{name: "Mods/PartyLimit/Sub/meta.lsx", data: []byte("not a module: too deep"), method: methodNone},
	)
	mods, err := PakModules(bytes.NewReader(pak), int64(len(pak)))
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) != 1 || mods[0].UUID != testUUID || mods[0].Meta != "Mods/PartyLimit/meta.lsx" {
		t.Fatalf("got %+v", mods)
	}
}

// An override pak - no meta.lsx - declares no module: the game loads it
// unlisted, and Vortex never lists it.
func TestPakModules_AnOverridePakHasNone(t *testing.T) {
	pak := buildPak(t, pakOptions{version: 15}, testMember{name: "Public/Game/GUI/x.lsx", data: []byte("x"), method: methodLZ4})
	mods, err := PakModules(bytes.NewReader(pak), int64(len(pak)))
	if err != nil || len(mods) != 0 {
		t.Fatalf("got %+v, %v", mods, err)
	}
}

func TestPakModules_AnUnreadableMetaIsAnError(t *testing.T) {
	pak := buildPak(t, pakOptions{version: 18}, testMember{name: "Mods/Z/meta.lsx", data: metaLSX("Z", "Z", testUUID, "1"), method: methodZstd})
	if _, err := PakModules(bytes.NewReader(pak), int64(len(pak))); !errors.Is(err, ErrZstd) {
		t.Fatalf("got %v", err)
	}
	pak = buildPak(t, pakOptions{version: 18}, testMember{name: "MODS/z/META.LSX", data: []byte("<save>"), method: methodNone})
	if _, err := PakModules(bytes.NewReader(pak), int64(len(pak))); !errors.Is(err, ErrBadMeta) {
		t.Fatalf("got %v", err)
	}
}

func TestIsBaseModule(t *testing.T) {
	if !IsBaseModule("28AC9CE2-2ABA-8CDA-B3B5-6E922F71B6B8", "anything") || !IsBaseModule(testUUID, "GustavX") || IsBaseModule(testUUID, "ImpUI") {
		t.Fatal("IsBaseModule disagrees with the game's module table and Vortex's name test")
	}
}

func TestValidUUID(t *testing.T) {
	for s, want := range map[string]bool{
		testUUID:                               true,
		strings.ToUpper(testUUID):              true,
		"GOLDWEIG-HTZE-RO12-4444-deeeeeeeeeef": true, // a real mod's, which the game loads
		"26922ba9601852520 75d-7ff2ba6ed879xx": false,
		"":                                     false,
		"a/b":                                  false,
		strings.Repeat("a", 65):                false,
	} {
		if ValidUUID(s) != want {
			t.Errorf("ValidUUID(%q) != %v", s, want)
		}
	}
}
