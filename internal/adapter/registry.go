package adapter

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
)

// Registry holds the adapters a running lmm knows about. The composition
// root (internal/app) is the only thing that registers concrete adapters;
// internal/core holds a Registry and never learns a concrete type.
type Registry struct {
	mu       sync.RWMutex
	adapters map[string]GameAdapter
}

// NewRegistry creates a registry holding the built-in identity adapter,
// and nothing else.
//
// The default is BUILT IN rather than registered by the composition root so
// that Resolve can never hand core a nil adapter: an implicit "nil means
// identity" contract is one every future seam call would have to remember,
// which is the class of bug this design exists to prevent (coordinator
// ruling, 2026-09-10). It also means a Service constructed WITHOUT the
// composition root - every core test - resolves the same identity the
// product does, instead of a second, subtly different empty path.
func NewRegistry() *Registry {
	r := &Registry{adapters: make(map[string]GameAdapter)}
	r.Register(Generic{})
	return r
}

// Register adds an adapter under its own ID, replacing any previous
// registration of that name.
func (r *Registry) Register(a GameAdapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.adapters[a.ID()] = a
}

// Get returns the adapter registered under name, reporting whether one was.
func (r *Registry) Get(name string) (GameAdapter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.adapters[name]
	return a, ok
}

// Names lists the registered adapter IDs, sorted - the vocabulary a
// frontend validates `--adapter` against and an error message names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.adapters))
	for id := range r.adapters {
		names = append(names, id)
	}
	sort.Strings(names)
	return names
}

// Resolve returns the adapter selected by name. The empty name is the
// default (GenericID), which is always registered.
//
// An unregistered name IS an error, naming the registered set: a games.yaml
// asking for an adapter this build does not ship is a misconfiguration lmm
// must fail loud on rather than silently downgrade - the same treatment the
// compile path gave an unconfigured game before #353, at a better moment.
// Resolve never returns a nil adapter with a nil error.
func (r *Registry) Resolve(name string) (GameAdapter, error) {
	if name == "" {
		name = GenericID
	}
	if a, ok := r.Get(name); ok {
		return a, nil
	}
	return nil, fmt.Errorf("unknown adapter %q (registered: %s)", name, strings.Join(r.Names(), ", "))
}

// Has reports whether name is registered. Core uses it for the ONE question
// that must not fail loud: whether the `deploy_mode: compile` migration has
// an icarus adapter to migrate to yet (design §2, OQ1).
func (r *Registry) Has(name string) bool {
	_, ok := r.Get(name)
	return ok
}

// ValidName reports whether s is syntactically a legal adapter name: a
// non-empty lowercase slug of letters, digits and single interior hyphens.
// It is a SYNTAX check only, deliberately ignorant of the registry, so
// internal/storage/config can validate games.yaml without learning which
// adapters this build ships (design §2, "validation splits by layer").
func ValidName(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") {
		return false
	}
	if strings.Contains(s, "--") {
		return false
	}
	return !slices.ContainsFunc([]byte(s), func(c byte) bool {
		lower := c >= 'a' && c <= 'z'
		digit := c >= '0' && c <= '9'
		return !lower && !digit && c != '-'
	})
}
