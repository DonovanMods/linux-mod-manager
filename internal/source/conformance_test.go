package source_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/curseforge"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/internal/source/nexusmods"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTypeLabels pins TypeLabel() across the conformance matrix: built-ins
// report "built-in", custom sources report their declared type.
func TestTypeLabels(t *testing.T) {
	dir, err := custom.NewDirectory(custom.SourceDefinition{
		ID: "d", Name: "D", Type: custom.TypeDirectory,
		Directory: &custom.DirectoryConfig{Path: t.TempDir()},
	})
	require.NoError(t, err)

	man, err := custom.NewManifest(custom.SourceDefinition{
		ID: "m", Name: "M", Type: custom.TypeManifest,
		Manifest: &custom.ManifestConfig{URL: "https://unreachable.invalid/mods.yaml"},
	})
	require.NoError(t, err)

	api, err := custom.NewAPI(custom.SourceDefinition{
		ID: "a", Name: "A", Type: custom.TypeAPI,
		API: &custom.APIConfig{BaseURL: "https://unreachable.invalid"},
	})
	require.NoError(t, err)

	tests := []struct {
		name string
		src  source.TypeLabeler
		want string
	}{
		{"directory", dir, "directory"},
		{"manifest", man, "manifest"},
		{"api", api, "api"},
		{"nexusmods", nexusmods.New(nil, ""), "built-in"},
		{"curseforge", curseforge.New(nil, ""), "built-in"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.src.TypeLabel())
		})
	}
}

// TestBuiltinCapabilitiesExplicit pins that both built-ins declare
// Capabilities() explicitly (all true) rather than relying on the
// CapabilitiesOf default.
func TestBuiltinCapabilitiesExplicit(t *testing.T) {
	all := source.Capabilities{Search: true, Dependencies: true, Updates: true, Auth: true, Versions: true}

	nm, ok := source.ModSource(nexusmods.New(nil, "")).(source.CapabilityReporter)
	require.True(t, ok, "NexusMods must implement CapabilityReporter")
	assert.Equal(t, all, nm.Capabilities())

	cf, ok := source.ModSource(curseforge.New(nil, "")).(source.CapabilityReporter)
	require.True(t, ok, "CurseForge must implement CapabilityReporter")
	assert.Equal(t, all, cf.Capabilities())
}
