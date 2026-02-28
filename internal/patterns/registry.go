package patterns

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Registry holds all loaded patterns (built-in + user-defined) and matches processes against them.
type Registry struct {
	patterns []Pattern
}

// NewRegistry creates a Registry pre-loaded with all built-in patterns.
func NewRegistry() (*Registry, error) {
	r := &Registry{}

	if err := r.loadBuiltin(); err != nil {
		return nil, fmt.Errorf("loading builtin patterns: %w", err)
	}

	return r, nil
}

func (r *Registry) loadBuiltin() error {
	entries, err := builtinPatterns.ReadDir(".")
	if err != nil {
		return fmt.Errorf("reading embedded patterns dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := builtinPatterns.ReadFile(entry.Name())
		if err != nil {
			return fmt.Errorf("reading %s: %w", entry.Name(), err)
		}

		var pf PatternFile
		if err := yaml.Unmarshal(data, &pf); err != nil {
			return fmt.Errorf("parsing %s: %w", entry.Name(), err)
		}
		r.patterns = append(r.patterns, pf.Patterns...)
	}

	return nil
}

// LoadExtra loads additional pattern files from the given paths.
func (r *Registry) LoadExtra(paths []string) error {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}

		var pf PatternFile
		if err := yaml.Unmarshal(data, &pf); err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		r.patterns = append(r.patterns, pf.Patterns...)
	}
	return nil
}

// All returns all loaded patterns.
func (r *Registry) All() []Pattern {
	return r.patterns
}

// Count returns the number of loaded patterns.
func (r *Registry) Count() int {
	return len(r.patterns)
}
