package tool

import (
	"fmt"
	"slices"
	"strings"

	"github.com/xzwache/loom/internal/llm"
)

type Registry struct {
	tools map[string]Tool
}

func NewRegistry(tools ...Tool) (*Registry, error) {
	r := &Registry{tools: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		if err := r.Register(t); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Registry) Register(t Tool) error {
	name := t.Spec().Name
	if name == "" {
		return fmt.Errorf("tool %T has no name", t)
	}
	if _, dup := r.tools[name]; dup {
		return fmt.Errorf("duplicate tool name %q", name)
	}
	r.tools[name] = t
	return nil
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) Specs() []llm.ToolSpec {
	specs := make([]llm.ToolSpec, 0, len(r.tools))
	for _, t := range r.tools {
		specs = append(specs, t.Spec())
	}
	slices.SortFunc(specs, toolComparerFunc)
	return specs
}

func toolComparerFunc(a, b llm.ToolSpec) int { return strings.Compare(a.Name, b.Name) }
