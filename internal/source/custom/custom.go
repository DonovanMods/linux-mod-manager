package custom

import (
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

// New constructs a ModSource from a validated definition. The default case
// is defensive: SourceDefinition.Validate already rejects unknown type
// strings, so it should be unreachable in practice, but New still returns a
// clear error instead of panicking if it is ever reached.
func New(def SourceDefinition) (source.ModSource, error) {
	switch def.Type {
	case TypeDirectory:
		return NewDirectory(def)
	case TypeManifest:
		return NewManifest(def)
	case TypeAPI:
		return NewAPI(def)
	default:
		return nil, fmt.Errorf("source type %q is not yet supported", def.Type)
	}
}
