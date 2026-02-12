package core

import (
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// DependencyResolver resolves mod dependencies and detects cycles
type DependencyResolver struct{}

// NewDependencyResolver creates a new dependency resolver
func NewDependencyResolver() *DependencyResolver {
	return &DependencyResolver{}
}

// modKey generates a unique key for a mod (source:id).
// Delegates to domain.ModKey.
func modKey(sourceID, modID string) string {
	return domain.ModKey(sourceID, modID)
}

// Resolve returns mods in dependency order (dependencies first)
// Returns ErrDependencyLoop if a circular dependency is detected
func (r *DependencyResolver) Resolve(mods []domain.Mod) ([]domain.Mod, error) {
	// Build lookup map
	modMap := make(map[string]*domain.Mod)
	for i := range mods {
		key := modKey(mods[i].SourceID, mods[i].ID)
		modMap[key] = &mods[i]
	}

	// Track visit state for cycle detection
	// 0 = unvisited, 1 = visiting (in stack), 2 = visited
	state := make(map[string]int)
	var result []domain.Mod

	// DFS with cycle detection
	var visit func(key string) error
	visit = func(key string) error {
		switch state[key] {
		case 2:
			// Already processed
			return nil
		case 1:
			// Currently in stack - cycle detected
			return domain.ErrDependencyLoop
		}

		mod, exists := modMap[key]
		if !exists {
			return fmt.Errorf("missing dependency: %s", key)
		}

		state[key] = 1 // Mark as visiting

		// Visit dependencies first
		for _, dep := range mod.Dependencies {
			depKey := modKey(dep.SourceID, dep.ModID)
			if err := visit(depKey); err != nil {
				return err
			}
		}

		state[key] = 2 // Mark as visited
		result = append(result, *mod)
		return nil
	}

	// Visit all mods
	for i := range mods {
		key := modKey(mods[i].SourceID, mods[i].ID)
		if state[key] == 0 {
			if err := visit(key); err != nil {
				return nil, err
			}
		}
	}

	return result, nil
}

// GetDependencyTree returns all dependencies for a mod (including transitive)
func (r *DependencyResolver) GetDependencyTree(mod *domain.Mod, modMap map[string]*domain.Mod) ([]domain.Mod, error) {
	visited := make(map[string]bool)
	var deps []domain.Mod

	var collect func(m *domain.Mod) error
	collect = func(m *domain.Mod) error {
		for _, ref := range m.Dependencies {
			key := modKey(ref.SourceID, ref.ModID)
			if visited[key] {
				continue
			}
			visited[key] = true

			dep, exists := modMap[key]
			if !exists {
				return fmt.Errorf("missing dependency: %s", key)
			}

			// Collect transitive dependencies first
			if err := collect(dep); err != nil {
				return err
			}
			deps = append(deps, *dep)
		}
		return nil
	}

	if err := collect(mod); err != nil {
		return nil, err
	}

	return deps, nil
}

// ValidateDependencies checks if all dependencies are satisfied
func (r *DependencyResolver) ValidateDependencies(mods []domain.Mod) error {
	available := make(map[string]bool)
	for _, mod := range mods {
		key := modKey(mod.SourceID, mod.ID)
		available[key] = true
	}

	for _, mod := range mods {
		for _, dep := range mod.Dependencies {
			key := modKey(dep.SourceID, dep.ModID)
			if !available[key] {
				return fmt.Errorf("mod %s requires missing dependency: %s", mod.ID, key)
			}
		}
	}

	return nil
}
