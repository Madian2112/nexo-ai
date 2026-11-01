// Package adapters is the registry of supported agents.
package adapters

import (
	"sort"

	"github.com/Madian2112/nexo-ai/internal/adapters/claude"
	"github.com/Madian2112/nexo-ai/internal/adapters/codex"
	"github.com/Madian2112/nexo-ai/internal/adapters/kiro"
	"github.com/Madian2112/nexo-ai/internal/adapters/opencode"
	"github.com/Madian2112/nexo-ai/internal/model"
	"github.com/Madian2112/nexo-ai/internal/plan"
)

type PlanFunc func(*model.Source, plan.Env) (plan.Plan, error)

var registry = map[string]PlanFunc{
	claude.Name:   claude.Plan,
	kiro.Name:     kiro.Plan,
	codex.Name:    codex.Plan,
	opencode.Name: opencode.Plan,
}

// ImportFunc reads an agent's current config back into the canonical model, plus warnings
// for everything that has no canonical equivalent.
type ImportFunc func(plan.Env) (*model.Source, []string, error)

var importers = map[string]ImportFunc{
	claude.Name:   claude.Import,
	kiro.Name:     kiro.Import,
	codex.Name:    codex.Import,
	opencode.Name: opencode.Import,
}

func Get(name string) (PlanFunc, bool) {
	f, ok := registry[name]
	return f, ok
}

func GetImport(name string) (ImportFunc, bool) {
	f, ok := importers[name]
	return f, ok
}

func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
