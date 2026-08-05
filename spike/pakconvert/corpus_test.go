package pakconvert

import (
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

func TestMatchTargets(t *testing.T) {
	mods := []domain.Mod{
		{ID: "a1", Name: "FloofLevelCap"},
		{ID: "b2", Name: "Intreeg's 4XP"},
		{ID: "c3", Name: "Unrelated Mod"},
		{ID: "d4", Name: "larkwell care package"},
	}
	got := MatchTargets(mods, []string{"flooflevelcap", "intreeg", "larkwell", "turret variants"})
	if len(got) != 3 {
		t.Fatalf("MatchTargets returned %d mods, want 3", len(got))
	}
	wantIDs := map[string]bool{"a1": true, "b2": true, "d4": true}
	for _, m := range got {
		if !wantIDs[m.ID] {
			t.Errorf("unexpected mod selected: %s (%s)", m.ID, m.Name)
		}
	}
}

func TestManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Manifest{Mods: []CorpusMod{
		{ID: "a1", Name: "FloofLevelCap", Version: "1.2", Week: "w57",
			Pak: "a1/FloofLevelCap.pak", Exmodz: ""},
	}}
	if err := SaveJSON(filepath.Join(dir, "manifest.json"), want); err != nil {
		t.Fatalf("SaveJSON: %v", err)
	}
	got, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(got.Mods) != 1 || got.Mods[0] != want.Mods[0] {
		t.Fatalf("round trip mismatch: got %+v want %+v", got.Mods, want.Mods)
	}
}
