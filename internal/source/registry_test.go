package source_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockSource struct {
	id string
}

func (m *mockSource) ID() string                                                   { return m.id }
func (m *mockSource) Name() string                                                 { return "Mock" }
func (m *mockSource) AuthURL() string                                              { return "" }
func (m *mockSource) ExchangeToken(context.Context, string) (*source.Token, error) { return nil, nil }
func (m *mockSource) Search(context.Context, source.SearchQuery) ([]domain.Mod, error) {
	return nil, nil
}
func (m *mockSource) GetMod(context.Context, string, string) (*domain.Mod, error) { return nil, nil }
func (m *mockSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, nil
}
func (m *mockSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, nil
}
func (m *mockSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", nil
}
func (m *mockSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, nil
}

func TestRegistry_Register(t *testing.T) {
	reg := source.NewRegistry()
	mock := &mockSource{id: "mock"}

	reg.Register(mock)

	src, err := reg.Get("mock")
	require.NoError(t, err)
	assert.Equal(t, "mock", src.ID())
}

func TestRegistry_Get_NotFound(t *testing.T) {
	reg := source.NewRegistry()

	_, err := reg.Get("nonexistent")
	assert.Error(t, err)
}

func TestRegistry_List(t *testing.T) {
	reg := source.NewRegistry()
	reg.Register(&mockSource{id: "source1"})
	reg.Register(&mockSource{id: "source2"})

	sources := reg.List()
	assert.Len(t, sources, 2)
}
