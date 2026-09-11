package app

import (
	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter/icarus"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// registerAdapters registers the built-in game adapters (#353). This layer is
// the only one that names a concrete adapter: internal/core resolves a game's
// adapter through the registry and never imports one
// (internal/adapter/boundary_test.go ratchets both halves).
//
// The identity adapter needs no line here - adapter.NewRegistry pre-registers
// it, so Resolve can never hand core a nil adapter.
//
// Registering icarus is what makes U1's `deploy_mode: compile` => `icarus`
// migration LIVE (design §2, OQ1, and the U1 amendment): the derivation in
// Service.AdapterName fires only for an adapter that is actually registered,
// so an existing Icarus games.yaml starts compiling through its adapter with
// no user action and no file rewritten. It is also the user-visible
// improvement this unit carries - compilation is now a property of the GAME,
// so an Icarus .pak or .exmodz compiles whichever source served it.
func registerAdapters(svc *core.Service) {
	svc.RegisterAdapter(icarus.New())
}
