package custom

import (
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

// New constructs a ModSource from a validated definition. It returns an error
// for definition types whose implementation has not shipped yet, so startup
// can warn-and-skip instead of failing.
func New(def SourceDefinition) (source.ModSource, error) {
	switch def.Type {
	default:
		return nil, fmt.Errorf("source type %q is not yet supported", def.Type)
	}
}
