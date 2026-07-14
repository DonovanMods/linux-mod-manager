package source

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/assert"
)

// fullSource implements ModSource but not CapabilityReporter.
type fullSource struct{}

func (fullSource) ID() string                                            { return "full" }
func (fullSource) Name() string                                          { return "Full" }
func (fullSource) AuthURL() string                                       { return "" }
func (fullSource) ExchangeToken(context.Context, string) (*Token, error) { return nil, nil }
func (fullSource) Search(context.Context, SearchQuery) (SearchResult, error) {
	return SearchResult{}, nil
}
func (fullSource) GetMod(context.Context, string, string) (*domain.Mod, error) { return nil, nil }
func (fullSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (fullSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (fullSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}
func (fullSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

// partialSource additionally reports limited capabilities.
type partialSource struct{ fullSource }

func (partialSource) Capabilities() Capabilities {
	return Capabilities{Search: true, Updates: true}
}

func TestCapabilitiesOf(t *testing.T) {
	t.Run("defaults to fully capable", func(t *testing.T) {
		caps := CapabilitiesOf(fullSource{})
		assert.Equal(t, Capabilities{Search: true, Dependencies: true, Updates: true, Auth: true}, caps)
	})

	t.Run("uses CapabilityReporter when implemented", func(t *testing.T) {
		caps := CapabilitiesOf(partialSource{})
		assert.Equal(t, Capabilities{Search: true, Dependencies: false, Updates: true, Auth: false}, caps)
	})
}
