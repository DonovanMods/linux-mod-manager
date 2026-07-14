package metadata

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
)

// ModInfoXML reads 7 Days to Die ModInfo.xml files. Two layouts exist:
// V2 puts fields directly under <xml>; V1 nests them in <ModInfo>.
type ModInfoXML struct{}

// Detect implements Reader.
func (ModInfoXML) Detect(modDir string) string {
	path := filepath.Join(modDir, "ModInfo.xml")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

type modInfoFields struct {
	Name        attrValue `xml:"Name"`
	DisplayName attrValue `xml:"DisplayName"`
	Version     attrValue `xml:"Version"`
	Description attrValue `xml:"Description"`
	Author      attrValue `xml:"Author"`
}

type modInfoDoc struct {
	modInfoFields
	ModInfo *modInfoFields `xml:"ModInfo"`
}

type attrValue struct {
	Value string `xml:"value,attr"`
}

// Read implements Reader.
func (ModInfoXML) Read(path string) (*Info, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading ModInfo.xml: %w", err)
	}

	var doc modInfoDoc
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing ModInfo.xml: %w", err)
	}

	fields := doc.modInfoFields
	if doc.ModInfo != nil && doc.ModInfo.Name.Value != "" {
		fields = *doc.ModInfo // V1 layout
	}
	if fields.Name.Value == "" {
		return nil, fmt.Errorf("ModInfo.xml has no Name element")
	}

	return &Info{
		Name:        fields.Name.Value,
		DisplayName: fields.DisplayName.Value,
		Version:     fields.Version.Value,
		Summary:     fields.Description.Value,
		Author:      fields.Author.Value,
	}, nil
}
