package core_test

import (
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// #313: a relative mod_path is refused at the spec, so no write path
// (`lmm game add`, `game add --from-detected`, POST /api/v1/games) can put
// a CWD-relative value into games.yaml.
func TestAddGameRefusesRelativeModPath(t *testing.T) {
	svc := newGameAddService(t)
	install := t.TempDir()

	_, err := svc.AddGame(t.Context(), core.GameSpec{
		SourceID:    "nexusmods",
		Identifier:  "fakegame",
		Name:        "Fake Game",
		InstallPath: install,
		ModPath:     "Data",
	})
	if err == nil {
		t.Fatal("AddGame accepted a relative mod_path")
	}
	var specErr *core.GameSpecError
	if !errors.As(err, &specErr) {
		t.Fatalf("error is not a *GameSpecError: %v", err)
	}
	if specErr.Field != "mod_path" {
		t.Errorf("Field = %q, want mod_path", specErr.Field)
	}
}
