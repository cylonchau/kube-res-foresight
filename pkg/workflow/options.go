package workflow

import (
	"context"
	"fmt"
)

// SelectOption is one choice offered for a ManualFieldDef whose Type is
// "select" — the shape the admin console renders a dropdown from.
type SelectOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// OptionSource resolves the choices for a select field at the moment the
// form is opened, rather than from a list frozen into the pipeline JSON.
// That matters for anything backed by live data: a pipeline should not
// hardcode records that change at runtime or ask for raw database IDs.
//
// A source is registered once at startup by whichever package owns the data
// (see pkg/app/server/api.RegisterWorkflowExtensions), the same
// compile-time registration pattern as RegisterCommandHandler and
// RegisterOutputActionHandler. A pipeline then references it declaratively
// via ManualFieldDef.OptionsSource.
type OptionSource func(ctx context.Context) ([]SelectOption, error)

// RegisterOptionSource registers src under name. A ManualFieldDef with
// OptionsSource == name will be populated from it.
func (e *Engine) RegisterOptionSource(name string, src OptionSource) {
	e.optionSources[name] = src
}

// ResolveOptionSource returns the current choices for the named source.
// An unregistered name is an error rather than an empty list: silently
// rendering an empty dropdown is exactly the dead end this mechanism
// exists to remove, and it would leave a required field unfillable with no
// indication of why.
func (e *Engine) ResolveOptionSource(ctx context.Context, name string) ([]SelectOption, error) {
	src, ok := e.optionSources[name]
	if !ok {
		return nil, fmt.Errorf("no option source registered for %q", name)
	}
	return src(ctx)
}

// RegisteredOptionSourceNames lists every option source currently
// registered, so the pipeline editor can offer a real catalog instead of
// the author having to guess a name that silently resolves to nothing.
func (e *Engine) RegisteredOptionSourceNames() []string {
	names := make([]string, 0, len(e.optionSources))
	for name := range e.optionSources {
		names = append(names, name)
	}
	return names
}
