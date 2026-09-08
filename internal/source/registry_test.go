package source_test

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

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
func (m *mockSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, nil
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

func TestRegistryUnregister(t *testing.T) {
	r := source.NewRegistry()
	r.Register(&mockSource{id: "a"})

	assert.True(t, r.Unregister("a"), "removing a registered source reports the removal")
	_, err := r.Get("a")
	require.Error(t, err)

	assert.False(t, r.Unregister("a"), "removing an id nobody registered is a tolerated no-op")
}

func TestRegistryReplace(t *testing.T) {
	r := source.NewRegistry()
	r.Register(&mockSource{id: "a"})

	assert.True(t, r.Replace(&mockSource{id: "a"}), "the swap displaced the existing registration")
	assert.False(t, r.Replace(&mockSource{id: "b"}), "nothing was registered under b")

	got, err := r.Get("b")
	require.NoError(t, err)
	assert.Equal(t, "b", got.ID())
	assert.Len(t, r.List(), 2)
}
