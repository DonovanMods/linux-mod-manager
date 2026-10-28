package config

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/safeyaml"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// canonicalProfile is a profile with a little of everything a file records.
func canonicalProfile() *domain.Profile {
	return &domain.Profile{
		Name: "p", GameID: "g1", IsDefault: true,
		Mods: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "1", Version: "1.0", FileIDs: []string{"10", "11"}},
			{SourceID: "nexusmods", ModID: "2", Version: "2.0", Locked: true},
			{SourceID: "curseforge", ModID: "3", Disabled: true},
		},
		Hooks:         domain.GameHooks{Install: domain.HookConfig{AfterAll: "/bin/after.sh"}},
		HooksExplicit: domain.GameHooksExplicit{Install: domain.HookExplicitFlags{AfterAll: true}},
	}
}

// profileChanges is every kind of change a save makes, applied to
// canonicalProfile.
var profileChanges = []struct {
	name   string
	change func(p *domain.Profile)
}{
	{"add a mod", func(p *domain.Profile) {
		p.Mods = append(p.Mods, domain.ModReference{SourceID: "s", ModID: "4", Version: "4", FileIDs: []string{"x"}, Locked: true})
	}},
	{"remove the first", func(p *domain.Profile) { p.Mods = p.Mods[1:] }},
	{"remove the last", func(p *domain.Profile) { p.Mods = p.Mods[:2] }},
	{"remove all", func(p *domain.Profile) { p.Mods = nil }},
	{"reverse", func(p *domain.Profile) { slices.Reverse(p.Mods) }},
	{"update files", func(p *domain.Profile) { p.Mods[0].FileIDs = []string{"12"} }},
	{"add files", func(p *domain.Profile) { p.Mods[1].FileIDs = []string{"20", "21"} }},
	{"drop files", func(p *domain.Profile) { p.Mods[0].FileIDs = nil }},
	{"new version", func(p *domain.Profile) { p.Mods[0].Version = "1.1" }},
	{"add a version", func(p *domain.Profile) { p.Mods[2].Version = "3.0" }},
	{"drop a version", func(p *domain.Profile) { p.Mods[1].Version = "" }},
	{"lock", func(p *domain.Profile) { p.Mods[0].Locked = true }},
	{"unlock", func(p *domain.Profile) { p.Mods[1].Locked = false }},
	{"disable all", func(p *domain.Profile) {
		for i := range p.Mods {
			p.Mods[i].Disabled = true
		}
	}},
	{"enable", func(p *domain.Profile) { p.Mods[2].Disabled = false }},
	{"not default", func(p *domain.Profile) { p.IsDefault = false }},
	{"link method", func(p *domain.Profile) { p.LinkMethod, p.LinkMethodExplicit = domain.LinkCopy, true }},
	{"more hooks", func(p *domain.Profile) {
		p.Hooks.Uninstall.BeforeAll, p.HooksExplicit.Uninstall.BeforeAll = "", true
	}},
	{"no hooks", func(p *domain.Profile) { p.Hooks, p.HooksExplicit = domain.GameHooks{}, domain.GameHooksExplicit{} }},
	{"overrides", func(p *domain.Profile) { p.Overrides = map[string][]byte{"a.ini": []byte("k=v\n")} }},
	{"a duplicate", func(p *domain.Profile) { p.Mods = append(p.Mods, p.Mods[0]) }},
	{"everything", func(p *domain.Profile) {
		p.Mods = []domain.ModReference{p.Mods[2], {SourceID: "s", ModID: "9"}, p.Mods[0]}
		p.Mods[2].Locked, p.IsDefault = true, false
		p.LinkMethod, p.LinkMethodExplicit = domain.LinkHardlink, true
		p.Hooks, p.HooksExplicit = domain.GameHooks{}, domain.GameHooksExplicit{}
	}},
}

// TestSaveProfile_AnLmmWrittenFileStaysAsAWholeWriteWritesIt: a file lmm
// wrote is edited IN PLACE into exactly the bytes a whole write of the new
// profile produces, for every change a save makes - so an in-place save
// never leaves lmm's own files looking hand-written, and the whole-rewrite
// fallback is never what makes that true.
func TestSaveProfile_AnLmmWrittenFileStaysAsAWholeWriteWritesIt(t *testing.T) {
	for _, tc := range profileChanges {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "p.yaml")
			outcome, err := saveProfileFile(path, path, canonicalProfile())
			require.NoError(t, err)
			require.Equal(t, savedWhole, outcome)

			profile := canonicalProfile()
			tc.change(profile)
			outcome, err = saveProfileFile(path, path, profile)
			require.NoError(t, err)
			assert.Equal(t, savedInPlace, outcome)

			whole, err := yaml.Marshal(profileFileOf(profile))
			require.NoError(t, err)
			got, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, string(whole), string(got))
		})
	}
}

// saveFuzzSeeds is the editor corpus plus lmm's own layout.
func saveFuzzSeeds(t testing.TB) []string {
	whole, err := yaml.Marshal(profileFileOf(canonicalProfile()))
	require.NoError(t, err)
	return append(slices.Clone(markerFuzzSeeds), string(whole),
		"name: p\ngame_id: g\nmods: []\nis_default: true\nlink_method: copy\n",
		"name: p\ngame_id: g\n",
		"mods:\n  - source_id: s\n    mod_id: a\n",
		"# only a comment\n",
		"",
	)
}

// FuzzSaveProfile is the save's property test. For any profile file lmm
// can read and any change to it, SaveProfile never panics, the file reads
// back as exactly the saved profile, and either
//
//   - it was edited in place, and no copy was kept; or
//   - it was rewritten whole, as a whole write writes it, and the old text
//     was kept beside it unless the rewrite lost nothing.
//
// A file lmm wrote itself is always edited in place, into the bytes a whole
// write produces.
func FuzzSaveProfile(f *testing.F) {
	for _, seed := range saveFuzzSeeds(f) {
		for _, change := range []uint64{0, 1, 0b100, 0b1000, 0b10000, 0b100000, 0b1000000000, ^uint64(0)} {
			f.Add([]byte(seed), change)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte, change uint64) {
		path := filepath.Join(t.TempDir(), "p.yaml")
		require.NoError(t, os.WriteFile(path, data, 0o644))
		var cfg ProfileConfig
		if err := safeyaml.Unmarshal(data, &cfg); err != nil {
			return
		}
		loaded, err := profileFromConfig(cfg, "g", "p")
		if err != nil {
			return
		}
		lmmWritten := false
		if whole, err := yaml.Marshal(profileFileOf(loaded)); err == nil {
			lmmWritten = bytes.Equal(whole, data)
		}

		want := mutate(loaded, change)
		outcome, err := saveProfileFile(path, path, want)
		if errors.Is(err, ErrProfileUnwritable) {
			raw, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			require.Equal(t, data, raw, "a profile that cannot be written leaves the file alone")
			return
		}
		require.NoError(t, err)

		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		var after ProfileConfig
		require.NoError(t, safeyaml.Unmarshal(raw, &after))
		got, err := profileFromConfig(after, "g", "p")
		require.NoError(t, err)
		require.True(t, sameProfile(got, want), "saved %+v, read back %+v", want, got)

		_, bakErr := os.Stat(path + profileBackupSuffix)
		switch outcome {
		case savedInPlace:
			require.ErrorIs(t, bakErr, fs.ErrNotExist)
			if change == 0 {
				require.Equal(t, string(data), string(raw), "a save that changes nothing writes nothing")
			}
			// A change that removes nothing keeps every comment line: only
			// a removed reference takes the comments above it, and only a
			// replaced hooks or overrides block, or a mods list that had no
			// entries, loses the ones inside it.
			const removing = 1<<3 | 1<<8 | 1<<9
			if change&removing == 0 && len(loaded.Mods) > 0 && len(want.Mods) > 0 {
				after := map[string]int{}
				for _, line := range strings.FieldsFunc(string(raw), func(r rune) bool { return r == '\n' || r == '\r' }) {
					if line = strings.TrimSpace(line); strings.HasPrefix(line, "#") {
						after[line]++
					}
				}
				for line, n := range commentLines(data) {
					require.GreaterOrEqual(t, after[line], n, "comment %q was lost", line)
				}
			}
		case savedRewritten:
			require.False(t, lmmWritten, "a file lmm wrote is edited in place")
			whole, marshalErr := yaml.Marshal(profileFileOf(want))
			require.NoError(t, marshalErr)
			require.Equal(t, string(whole), string(raw))
			if bakErr == nil {
				kept, readErr := os.ReadFile(path + profileBackupSuffix)
				require.NoError(t, readErr)
				require.Equal(t, data, kept)
			}
		default:
			t.Fatalf("an existing file saved as %v", outcome)
		}
		if lmmWritten {
			whole, marshalErr := yaml.Marshal(profileFileOf(want))
			require.NoError(t, marshalErr)
			require.Equal(t, string(whole), string(raw))
		}
	})
}

// commentLines counts data's lines that hold nothing but a comment, by
// text - or returns nil when that count disagrees with yaml.v3's, for a
// line that only looks like a comment (inside a quoted scalar, say).
func commentLines(data []byte) map[string]int {
	counts := map[string]int{}
	for _, line := range strings.FieldsFunc(string(data), func(r rune) bool { return r == '\n' || r == '\r' }) {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "#") {
			counts[line]++
		}
	}
	var doc yaml.Node
	if err := safeyaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	parsed := map[string]int{}
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		for _, comment := range []string{n.HeadComment, n.LineComment, n.FootComment} {
			for _, line := range strings.FieldsFunc(comment, func(r rune) bool { return r == '\n' || r == '\r' }) {
				parsed[strings.TrimSpace(line)]++
			}
		}
		for _, child := range n.Content {
			walk(child)
		}
	}
	walk(&doc)
	for line := range counts {
		if parsed[line] == 0 {
			return nil
		}
	}
	return counts
}

// mutate returns a copy of p with the changes change's bits select.
func mutate(p *domain.Profile, change uint64) *domain.Profile {
	out := *p
	out.Mods = slices.Clone(p.Mods)
	for i := range out.Mods {
		out.Mods[i].FileIDs = slices.Clone(out.Mods[i].FileIDs)
	}
	bit := func(n int) bool { return change&(1<<n) != 0 }
	if bit(0) {
		out.IsDefault = !out.IsDefault
	}
	if bit(1) {
		if out.LinkMethodExplicit {
			out.LinkMethod, out.LinkMethodExplicit = domain.LinkSymlink, false
		} else {
			out.LinkMethod, out.LinkMethodExplicit = domain.LinkHardlink, true
		}
	}
	if bit(2) {
		out.Mods = append(out.Mods, domain.ModReference{SourceID: "new", ModID: "n", Version: "2", FileIDs: []string{"9"}})
	}
	if bit(3) && len(out.Mods) > 0 {
		out.Mods = out.Mods[1:]
	}
	if bit(4) {
		slices.Reverse(out.Mods)
	}
	if bit(5) && len(out.Mods) > 0 {
		last := &out.Mods[len(out.Mods)-1]
		last.Locked, last.Version = true, "3"
	}
	if bit(6) && len(out.Mods) > 0 {
		if len(out.Mods[0].FileIDs) > 0 {
			out.Mods[0].FileIDs = nil
		} else {
			out.Mods[0].FileIDs = []string{"7", "8"}
		}
	}
	if bit(7) {
		for i := range out.Mods {
			out.Mods[i].Disabled = !out.Mods[i].Disabled
		}
	}
	if bit(8) {
		if out.HooksExplicit != (domain.GameHooksExplicit{}) {
			out.Hooks, out.HooksExplicit = domain.GameHooks{}, domain.GameHooksExplicit{}
		} else {
			out.Hooks.Install.BeforeAll, out.HooksExplicit.Install.BeforeAll = "", true
		}
	}
	if bit(9) {
		if len(out.Overrides) > 0 {
			out.Overrides = nil
		} else {
			out.Overrides = map[string][]byte{"x.ini": []byte("a=1\n")}
		}
	}
	if bit(10) && len(out.Mods) > 0 {
		out.Mods[0].Version = ""
	}
	if bit(11) && len(out.Mods) > 0 {
		out.Mods[0].Locked = !out.Mods[0].Locked
	}
	return &out
}
