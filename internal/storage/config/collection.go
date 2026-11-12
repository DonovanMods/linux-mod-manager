package config

// The lmm collection format's YAML codec (#621; the spec is
// docs/collection-format.md). A version-1 document is the profile export
// with the format's keys around it, and it keeps `name:` and `game_id:` with
// their version-0 meaning, so today's version-0 reader (ImportProfile, which
// ignores keys it does not know) still imports one by them.

import (
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/safeyaml"

	"go.yaml.in/yaml/v3"
)

// collectionDocYAML is a version-1 document's wire shape. The field order
// is the document's key order.
type collectionDocYAML struct {
	LMMCollection       int                             `yaml:"lmm_collection"`
	ID                  string                          `yaml:"id,omitempty"`
	Version             int                             `yaml:"version,omitempty"`
	Name                string                          `yaml:"name"`
	Title               string                          `yaml:"title,omitempty"`
	Author              string                          `yaml:"author,omitempty"`
	Description         string                          `yaml:"description,omitempty"`
	InstallInstructions string                          `yaml:"install_instructions,omitempty"`
	GameID              string                          `yaml:"game_id,omitempty"`
	Game                domain.CollectionGame           `yaml:"game"`
	Requires            []string                        `yaml:"requires,omitempty"`
	Origin              *domain.CollectionOrigin        `yaml:"origin,omitempty"`
	Signer              string                          `yaml:"signer,omitempty"`
	Mods                []collectionModYAML             `yaml:"mods"`
	External            []domain.CollectionExternalItem `yaml:"external,omitempty"`
	Hooks               profileFileHooksYAML            `yaml:"hooks,omitempty"`
	Overrides           map[string]string               `yaml:"overrides,omitempty"`
}

// collectionModYAML is one mods: entry: the profile's own ModReference
// (inline, so every field a profile reference gains is in the format with
// no second schema to keep in step) and what the format adds for a
// recipient.
type collectionModYAML struct {
	domain.ModReference `yaml:",inline"`
	Name                string                     `yaml:"name,omitempty"`
	Files               []domain.CollectionFileRef `yaml:"files,omitempty"`
}

// MarshalCollection writes doc as a version-1 collection document.
func MarshalCollection(doc *domain.CollectionDocument) ([]byte, error) {
	hooks := serializeProfileHooks(doc.Hooks, doc.HooksExplicit)
	wire := collectionDocYAML{
		LMMCollection:       doc.FormatVersion,
		ID:                  doc.Info.ID,
		Version:             doc.Info.Version,
		Name:                doc.Name,
		Title:               doc.Info.Title,
		Author:              doc.Info.Author,
		Description:         doc.Info.Description,
		InstallInstructions: doc.Info.InstallInstructions,
		GameID:              doc.GameID,
		Game:                doc.Game,
		Requires:            doc.Requires,
		Signer:              doc.Signer,
		External:            doc.External,
		Hooks:               profileFileHooksYAML{Install: fileHook(hooks.Install), Uninstall: fileHook(hooks.Uninstall)},
		Overrides:           doc.Overrides,
		Mods:                make([]collectionModYAML, len(doc.Items)),
	}
	if doc.Info.Origin != nil && !doc.Info.Origin.IsZero() {
		wire.Origin = doc.Info.Origin
	}
	for i, item := range doc.Items {
		wire.Mods[i] = collectionModYAML{ModReference: item.Ref, Name: item.Name, Files: item.Files}
	}
	data, err := yaml.Marshal(&wire)
	if err != nil {
		return nil, fmt.Errorf("marshaling collection document: %w", err)
	}
	return data, nil
}

// ParseCollection reads a version-1 collection document. It decodes; it
// does not judge - the version check, the caps and the sanitising are the
// importer's (core), which knows what it supports.
func ParseCollection(data []byte) (*domain.CollectionDocument, error) {
	var wire collectionDocYAML
	if err := safeyaml.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("parsing collection document: %w", err)
	}
	doc := &domain.CollectionDocument{
		FormatVersion: wire.LMMCollection,
		Name:          wire.Name,
		GameID:        wire.GameID,
		Info: domain.CollectionInfo{
			ID:                  wire.ID,
			Version:             wire.Version,
			Title:               wire.Title,
			Author:              wire.Author,
			Description:         wire.Description,
			InstallInstructions: wire.InstallInstructions,
		},
		Game:      wire.Game,
		Requires:  wire.Requires,
		Signer:    wire.Signer,
		External:  wire.External,
		Overrides: wire.Overrides,
	}
	if wire.Origin != nil && !wire.Origin.IsZero() {
		doc.Info.Origin = wire.Origin
	}
	doc.Hooks, doc.HooksExplicit = parseProfileHooks(ProfileHooksYAML{
		Install:   ProfileHookConfigYAML(wire.Hooks.Install),
		Uninstall: ProfileHookConfigYAML(wire.Hooks.Uninstall),
	})
	if len(wire.Mods) > 0 {
		doc.Items = make([]domain.CollectionItem, len(wire.Mods))
		for i, m := range wire.Mods {
			doc.Items[i] = domain.CollectionItem{Ref: m.ModReference, Name: m.Name, Files: m.Files}
		}
	}
	return doc, nil
}

// CollectionFormatOf reports a YAML document's collection format version:
// its `lmm_collection:` value, or 0 when it has none (a version-0 profile
// export). A document that does not parse, or whose value is not a whole
// number, is an error.
func CollectionFormatOf(data []byte) (int, error) {
	var head struct {
		Version int `yaml:"lmm_collection"`
	}
	if err := safeyaml.Unmarshal(data, &head); err != nil {
		return 0, fmt.Errorf("parsing profile document: %w", err)
	}
	return head.Version, nil
}
