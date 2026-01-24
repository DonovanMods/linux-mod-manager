package source

import (
	"fmt"
	"sync"
)

// Registry manages available mod sources
type Registry struct {
	mu      sync.RWMutex
	sources map[string]ModSource
}

// NewRegistry creates a new source registry
func NewRegistry() *Registry {
	return &Registry{
		sources: make(map[string]ModSource),
	}
}

// Register adds a source to the registry
func (r *Registry) Register(source ModSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sources[source.ID()] = source
}

// Get retrieves a source by ID
func (r *Registry) Get(id string) (ModSource, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	source, ok := r.sources[id]
	if !ok {
		return nil, fmt.Errorf("source not found: %s", id)
	}
	return source, nil
}

// List returns all registered sources
func (r *Registry) List() []ModSource {
	r.mu.RLock()
	defer r.mu.RUnlock()

	sources := make([]ModSource, 0, len(r.sources))
	for _, s := range r.sources {
		sources = append(sources, s)
	}
	return sources
}
