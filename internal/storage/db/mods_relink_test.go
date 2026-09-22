package db_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
	"github.com/stretchr/testify/require"
)

func relinkTestMod(game, profile, source, id string) *domain.InstalledMod {
	return &domain.InstalledMod{
		Mod:         domain.Mod{GameID: game, SourceID: source, ID: id, Name: id, Version: "1"},
		ProfileName: profile, Enabled: true, Deployed: true, FileIDs: []string{"archive"},
	}
}

func TestRelinkInstalledMod_PreservesLedgerAndScope(t *testing.T) {
	ctx := context.Background()
	database, err := db.New(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	for _, scope := range [][2]string{{"game", "default"}, {"game", "other"}, {"other-game", "default"}} {
		require.NoError(t, database.SaveInstalledMod(ctx, relinkTestMod(scope[0], scope[1], "local", "old")))
		require.NoError(t, database.RecordDeployedFile(ctx, db.DeployedFileRecord{
			GameID: scope[0], Profile: scope[1], RelativePath: "file.esp", SourceID: "local", ModID: "old",
			ModPath: "/games/" + scope[0], Fingerprint: &db.FileFingerprint{Checksum: "abc", Size: 5, MTime: 6, CTime: 7},
		}))
	}
	moved := relinkTestMod("game", "default", "repo", "new")
	moved.Name = "Renamed"
	require.NoError(t, database.RelinkInstalledMod(ctx, "local", "old", moved))
	mod, err := database.GetInstalledMod(ctx, "repo", "new", "game", "default")
	require.NoError(t, err)
	require.Equal(t, "Renamed", mod.Name)
	require.Equal(t, []string{"archive"}, mod.FileIDs)
	for _, scope := range [][2]string{{"game", "default"}, {"game", "other"}, {"other-game", "default"}} {
		states, err := database.DeployedFileStates(ctx, scope[0], "file.esp")
		require.NoError(t, err)
		var state *db.DeployedFileState
		for i := range states {
			if states[i].Profile == scope[1] {
				state = &states[i]
			}
		}
		require.NotNil(t, state)
		wantSource, wantID := "local", "old"
		if scope[0] == "game" && scope[1] == "default" {
			wantSource, wantID = "repo", "new"
		}
		require.Equal(t, wantSource, state.SourceID)
		require.Equal(t, wantID, state.ModID)
		require.Equal(t, "/games/"+scope[0], state.ModPath)
		require.Equal(t, &db.FileFingerprint{Checksum: "abc", Size: 5, MTime: 6, CTime: 7}, state.Fingerprint)
	}
}

func TestRelinkInstalledMod_PreservesFileChecksums(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source string
		id     string
	}{
		{name: "new identity", source: "repo", id: "new"},
		{name: "same identity", source: "local", id: "old"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			database, err := db.New(":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, database.Close()) })
			require.NoError(t, database.SaveInstalledMod(ctx, relinkTestMod("g", "p", "local", "old")))
			require.NoError(t, database.SaveFileChecksum(ctx, "local", "old", "g", "p", "archive", "abc123"))

			moved := relinkTestMod("g", "p", tc.source, tc.id)
			moved.Name = "Renamed"
			require.NoError(t, database.RelinkInstalledMod(ctx, "local", "old", moved))
			checksum, err := database.GetFileChecksum(ctx, tc.source, tc.id, "g", "p", "archive")
			require.NoError(t, err)
			require.Equal(t, "abc123", checksum)
			stored, err := database.GetInstalledMod(ctx, tc.source, tc.id, "g", "p")
			require.NoError(t, err)
			require.Equal(t, "Renamed", stored.Name)
		})
	}
}

func TestRelinkInstalledMod_CollisionAndLedgerFailureRollBack(t *testing.T) {
	for _, failure := range []string{"collision", "ledger"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			database, err := db.New(":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, database.Close()) })
			require.NoError(t, database.SaveInstalledMod(ctx, relinkTestMod("g", "p", "local", "old")))
			require.NoError(t, database.SaveFileChecksum(ctx, "local", "old", "g", "p", "archive", "abc123"))
			require.NoError(t, database.SaveDeployedFile(ctx, "g", "p", "file.esp", "local", "old"))
			switch failure {
			case "collision":
				require.NoError(t, database.SaveInstalledMod(ctx, relinkTestMod("g", "p", "repo", "new")))
			case "ledger":
				_, err := database.ExecContext(ctx, `CREATE TRIGGER refuse_relink BEFORE UPDATE OF source_id ON deployed_files
					BEGIN SELECT RAISE(ABORT, 'ledger blocked'); END`)
				require.NoError(t, err)
			}
			err = database.RelinkInstalledMod(ctx, "local", "old", relinkTestMod("g", "p", "repo", "new"))
			require.Error(t, err)
			old, err := database.GetInstalledMod(ctx, "local", "old", "g", "p")
			require.NoError(t, err)
			require.Equal(t, []string{"archive"}, old.FileIDs)
			checksum, err := database.GetFileChecksum(ctx, "local", "old", "g", "p", "archive")
			require.NoError(t, err)
			require.Equal(t, "abc123", checksum)
			rows, err := database.GetDeployedFilesForMod(ctx, "g", "p", "local", "old")
			require.NoError(t, err)
			require.Equal(t, []string{"file.esp"}, rows)
		})
	}
}
