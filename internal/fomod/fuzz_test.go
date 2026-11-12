package fomod

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func FuzzParse(f *testing.F) {
	f.Add([]byte(`<config><moduleName>x</moduleName></config>`))
	f.Add(utf16Bytes(`<config><installSteps><installStep name="a"/></installSteps></config>`, false, true))
	f.Add([]byte("<config><requiredInstallFiles><file source=\"a\xE9\"/></requiredInstallFiles></config>"))
	f.Add([]byte(`<!DOCTYPE config [<!ENTITY e "x">]><config>&e;</config>`))
	f.Add([]byte(oneGroup("SelectExactlyOne", plugin("a", "Optional"), plugin("b", "Recommended", "f", "1"))))
	f.Fuzz(func(t *testing.T, data []byte) {
		cfg, err := Parse(data)
		if err != nil {
			if cfg != nil {
				t.Fatal("a refused document returned a config")
			}
			return
		}
		// Whatever parses must evaluate without panicking, under both
		// modes, and resolve to contained destinations.
		for _, mode := range []Mode{ModeReplay, ModeDefaults} {
			res, err := Evaluate(cfg, Request{Mode: mode, Members: fuzzMembers})
			if err != nil {
				continue
			}
			// Feeding the canonical choices back must be stable.
			again, err := Evaluate(cfg, Request{Mode: ModeReplay, Choices: res.Choices, Members: fuzzMembers})
			if err != nil {
				t.Fatalf("replaying canonical choices failed: %v", err)
			}
			if len(again.Unmatched) != 0 {
				t.Fatalf("canonical choices did not match: %+v", again.Unmatched)
			}
			for _, op := range res.Ops {
				if !isContained(op.Destination) {
					t.Fatalf("uncontained destination %q", op.Destination)
				}
			}
		}
	})
}

func FuzzLocate(f *testing.F) {
	f.Add("mod.zip", "fomod/ModuleConfig.xml", `W\fomod\script.cs`)
	f.Fuzz(func(t *testing.T, name, a, b string) {
		in, err := Locate(name, []string{a, b})
		if err == nil && in != nil {
			if _, ok := in.Rel(in.ModuleConfig); !ok {
				t.Fatalf("the config %q is not under its own root %q", in.ModuleConfig, in.Root)
			}
		}
	})
}

// isContained reports whether a destination stays inside the mod root.
func isContained(dest string) bool {
	return dest != ".." && !strings.HasPrefix(dest, "../") && !strings.HasPrefix(dest, "/")
}

// fuzzFixture is testdata/full.xml, read once for FuzzEvaluateChoices.
var fuzzFixture = func() []byte {
	data, err := os.ReadFile("testdata/full.xml")
	if err != nil {
		panic(err)
	}
	return data
}()

func FuzzEvaluateChoices(f *testing.F) {
	f.Add(vortexChoices, false)
	f.Add(`[{"name":"Main","index":-1,"groups":[{"name":"Patches","index":7,"choices":[{"name":"Lighting Patch","idx":-3,"index":0}]}]}]`, true)
	f.Add(`[{"name":"Lighting Options","groups":[{"name":"Brightness","choices":null}]}]`, false)
	f.Fuzz(func(t *testing.T, choices string, defaults bool) {
		var steps []StepChoice
		if json.Unmarshal([]byte(choices), &steps) != nil {
			return
		}
		cfg, err := Parse(fuzzFixture)
		if err != nil {
			t.Fatal(err)
		}
		mode := ModeReplay
		if defaults {
			mode = ModeDefaults
		}
		res, err := Evaluate(cfg, Request{Mode: mode, Choices: steps, Files: fileStates{"skyrim.esm": FileActive}, Members: []string{"00 Core/a.esp", "10 Textures 2K/a.dds"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Steps) == 0 {
			t.Fatal("the first step is always visible")
		}
	})
}

// fuzzMembers mixes ordinary members with hostile ones the resolver must drop.
var fuzzMembers = []string{"a", "b/c", "fomod/ModuleConfig.xml", "x/../../up", "/abs", `C:\win`, `.\b\d`}
