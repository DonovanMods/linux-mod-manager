package core_test

// #582: an update check a source refused for want of a key names that
// source as data on the report, so the web UI can link it to the key's
// settings instead of showing only the joined error text.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keylessSource refuses every update check for want of a key.
type keylessSource struct{ *mockSource }

func (s *keylessSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, fmt.Errorf("%w: %s API key required", domain.ErrAuthRequired, s.id)
}

func TestCheckGameUpdateReport_NamesSourcesThatNeedAKey(t *testing.T) {
	svc, game, _ := newCatalogMissingService(t, map[string]string{"live": "Live Mod"}, nil, nil)
	for _, id := range []string{"keyless-b", "keyless-a"} {
		svc.RegisterSource(&keylessSource{mockSource: newMockSource(id)})
		require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
			Mod:         domain.Mod{ID: "m-" + id, SourceID: id, Name: "Mod " + id, Version: "1.0", GameID: game.ID},
			ProfileName: "default", UpdatePolicy: domain.UpdateNotify, Enabled: true, Deployed: true,
		}))
		seedProfileWithMod(t, svc, game.ID, "default", id, "m-"+id, "1.0")
	}

	report, err := svc.CheckGameUpdateReport(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
	require.Error(t, err)
	assert.Equal(t, err.Error(), report.ErrorMessage, "the error text is unchanged")
	assert.Equal(t, []string{"keyless-a", "keyless-b"}, report.AuthRequired, "every refusing source, sorted")
	require.Len(t, report.Updates, 1, "the other source's check stands")
}

func TestCheckGameUpdateReport_NoAuthRequiredWithoutARefusal(t *testing.T) {
	svc, game, _ := newCatalogMissingService(t, map[string]string{"live": "Live Mod", "flaky": "Flaky"}, nil, []string{"flaky"})
	report, err := svc.CheckGameUpdateReport(context.Background(), game, "default", installedFor(t, svc, game), nil, core.UpdateCheckOptions{})
	require.Error(t, err)
	assert.Empty(t, report.AuthRequired)
}

func TestAuthRequiredSources_WalksAJoinedError(t *testing.T) {
	err := fmt.Errorf("update check had 3 source error(s): %w", errors.Join(
		&core.SourceAuthError{SourceID: "nexusmods", Err: domain.ErrAuthRequired},
		errors.New("source flaky: boom"),
		&core.SourceAuthError{SourceID: "curseforge", Err: domain.ErrAuthRequired},
		&core.SourceAuthError{SourceID: "curseforge", Err: domain.ErrAuthRequired},
	))
	assert.Equal(t, []string{"curseforge", "nexusmods"}, core.AuthRequiredSources(err))
	assert.Nil(t, core.AuthRequiredSources(errors.New("boom")))
	assert.Nil(t, core.AuthRequiredSources(nil))
}
