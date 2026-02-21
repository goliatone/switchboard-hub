package tunnel

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

type Factory func() Provider

type Registry struct {
	mu        sync.RWMutex
	factories map[string]Factory
}

func NewRegistry() *Registry {
	return &Registry{
		factories: map[string]Factory{},
	}
}

func (r *Registry) Register(name string, factory Factory) error {
	if r == nil {
		return fmt.Errorf("registry is nil")
	}
	key := normalizeProviderName(name)
	if key == "" {
		return fmt.Errorf("provider name is required")
	}
	if factory == nil {
		return fmt.Errorf("provider %q factory is nil", key)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.factories[key]; exists {
		return fmt.Errorf("provider %q already registered", key)
	}
	r.factories[key] = factory
	return nil
}

func (r *Registry) Resolve(name string) (Provider, error) {
	if r == nil {
		return nil, fmt.Errorf("registry is nil")
	}
	key := normalizeProviderName(name)
	if key == "" {
		return nil, fmt.Errorf("provider name is required")
	}

	r.mu.RLock()
	factory := r.factories[key]
	r.mu.RUnlock()
	if factory == nil {
		return nil, fmt.Errorf("provider %q is not registered", key)
	}
	return factory(), nil
}

func (r *Registry) Providers() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	out := make([]string, 0, len(r.factories))
	for k := range r.factories {
		out = append(out, k)
	}
	r.mu.RUnlock()
	sort.Strings(out)
	return out
}

func normalizeProviderName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
