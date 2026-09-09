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

// Unregister removes whatever source is registered under id, reporting
// whether one was there to remove. Removing an id nobody registered is not
// an error: the caller that deletes a user-defined source's definition file
// has no way to know in advance whether that definition ever constructed
// successfully (a broken one is listed by `lmm source list` as an error row
// and is registered nowhere), and both outcomes leave the registry in the
// state it asked for.
func (r *Registry) Unregister(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, existed := r.sources[id]
	delete(r.sources, id)
	return existed
}

// Replace registers src in place of whatever currently holds src.ID(),
// reporting whether it displaced an existing registration.
//
// Register already overwrites, so this adds no new capability to the map -
// it adds INTENT and an answer. A caller swapping a live source (a re-keyed
// built-in after `auth login`, an edited custom definition saved from the
// web UI) means something different from one populating an empty registry
// at startup, and it needs to know whether the swap actually replaced
// something or quietly introduced a source nobody asked for. Register
// stays the startup verb and keeps its "first registration wins" call-site
// convention in app.registerSource; this is the deliberate-swap verb.
//
// The swap is atomic with respect to Get/List (one write lock), but a
// source VALUE another goroutine already resolved out of the registry keeps
// being used by that goroutine until it is done. Serializing the swap
// against in-flight mutations is the caller's job - core.Service.ReplaceSource
// takes the mutation gate for exactly that reason.
func (r *Registry) Replace(src ModSource) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, existed := r.sources[src.ID()]
	r.sources[src.ID()] = src
	return existed
}
