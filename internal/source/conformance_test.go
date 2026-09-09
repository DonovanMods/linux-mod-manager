package source_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/curseforge"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/nexusmods"
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

	// #121 returns a WRAPPER (custom.NewAPISource -> apiKeyValidator) for
	// any api definition declaring an auth.validate probe, so the type this
	// suite holds to the contract must be that wrapper too, not only the
	// concrete *API it embeds (Track C review, finding 11).
	validating, err := custom.NewAPISource(custom.SourceDefinition{
		ID: "av", Name: "AV", Type: custom.TypeAPI,
		API: &custom.APIConfig{
			BaseURL: "https://unreachable.invalid",
			Auth: &custom.AuthConfig{
				APIKey:   &custom.APIKeyConfig{In: "header", Name: "X-API-Key"},
				Validate: &custom.AuthValidateConfig{Path: "/me"},
			},
		},
	})
	require.NoError(t, err)
	validatingLabeler, ok := validating.(source.TypeLabeler)
	require.True(t, ok, "the key-validating api wrapper must still report its type label")
	_, ok = validating.(source.KeyValidator)
	require.True(t, ok, "a declared probe is what the wrapper exists to expose")

	tests := []struct {
		name string
		src  source.TypeLabeler
		want string
	}{
		{"directory", dir, "directory"},
		{"manifest", man, "manifest"},
		{"api", api, "api"},
		{"api with a key-validation probe", validatingLabeler, "api"},
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

// TestAPIKeyValidatorWrapperKeepsTheAPIContract holds #121's wrapper to the
// same contract as the concrete type it embeds: the wrapper is what
// custom.New hands the registry for any definition declaring a probe, so
// every promoted method the rest of the app reaches for through a type
// assertion has to survive the wrapping (Track C review, finding 11).
func TestAPIKeyValidatorWrapperKeepsTheAPIContract(t *testing.T) {
	def := custom.SourceDefinition{
		ID: "av", Name: "AV", Type: custom.TypeAPI,
		API: &custom.APIConfig{
			BaseURL:   "https://unreachable.invalid",
			Auth:      &custom.AuthConfig{APIKey: &custom.APIKeyConfig{In: "header", Name: "X-API-Key"}, Validate: &custom.AuthValidateConfig{Path: "/me"}},
			Endpoints: custom.APIEndpoints{Search: &custom.EndpointConfig{Path: "/mods", List: "data"}},
			Mappings:  custom.APIMappings{Mod: map[string]string{"id": "id", "name": "name"}},
		},
	}
	wrapped, err := custom.NewAPISource(def)
	require.NoError(t, err)
	concrete, err := custom.NewAPI(def)
	require.NoError(t, err)

	assert.Equal(t, concrete.ID(), wrapped.ID())
	assert.Equal(t, concrete.Name(), wrapped.Name())

	wrappedCaps, ok := wrapped.(source.CapabilityReporter)
	require.True(t, ok, "the wrapper must still report capabilities")
	assert.Equal(t, concrete.Capabilities(), wrappedCaps.Capabilities(),
		"wrapping must not change what the source can do")

	// The two duck-typed methods app/sources.go asserts for when it stores
	// or resolves a key; losing either to the wrapper would silently stop
	// custom api sources from authenticating at all.
	keyed, ok := wrapped.(interface{ SetAPIKey(string) })
	require.True(t, ok, "SetAPIKey must stay in the wrapper's method set")
	keyed.SetAPIKey("k")
	authed, ok := wrapped.(interface{ IsAuthenticated() bool })
	require.True(t, ok, "IsAuthenticated must stay in the wrapper's method set")
	assert.True(t, authed.IsAuthenticated(), "the key just set must be visible through the wrapper")
}
