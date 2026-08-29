// Package custom implements user-defined mod sources configured declaratively
// via YAML files in <configDir>/sources/. See the design doc:
// docs/plans/2026-07-13-custom-sources-design.md
package custom

import "github.com/DonovanMods/linux-mod-manager/internal/source"

// SourceDefinition and its Type* constants moved to internal/source (v2
// Phase 2 Task 22): the interface package a frontend may import without
// pulling in custom's concrete source implementations. These aliases keep
// every existing custom.SourceDefinition/custom.Type*/custom.*Config
// reference - in this package and its tests - compiling unchanged.
type (
	SourceDefinition = source.SourceDefinition
	DirectoryConfig  = source.DirectoryConfig
	ManifestConfig   = source.ManifestConfig
	AuthConfig       = source.AuthConfig
	APIKeyConfig     = source.APIKeyConfig
	APIConfig        = source.APIConfig
	APIEndpoints     = source.APIEndpoints
	EndpointConfig   = source.EndpointConfig
	APIMappings      = source.APIMappings
)

// Source type identifiers for SourceDefinition.Type.
const (
	TypeDirectory = source.TypeDirectory
	TypeManifest  = source.TypeManifest
	TypeAPI       = source.TypeAPI
)
