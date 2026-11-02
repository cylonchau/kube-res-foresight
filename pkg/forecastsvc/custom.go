package forecastsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cylonchau/kube-res-foresight/pkg/forecast"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// SpecOf is the custom algorithm stored in a config.
func SpecOf(c *model.AlgorithmConfig) (forecast.CustomSpec, error) {
	spec := forecast.CustomSpec{ID: c.AlgorithmID, Category: c.Category, Description: c.Description, Formula: forecast.FormulaSpec{
		Expr: c.Formula, MinSamples: c.MinSamples, Recursive: c.Recursive, Params: []forecast.ParamSpec{},
	}}
	if c.ParamSchema != "" {
		if err := json.Unmarshal([]byte(c.ParamSchema), &spec.Formula.Params); err != nil {
			return spec, fmt.Errorf("the parameter schema of %s is not valid: %w", c.AlgorithmID, err)
		}
	}
	return spec, nil
}

// SyncCustom registers the custom algorithms of the database and unregisters the ones that are gone, so every server
// of a deployment runs the same set. An algorithm whose formula did not change is left alone. A custom algorithm that
// cannot be compiled is skipped (it is reported by the error), never allowed to stop the others.
func (s *Service) SyncCustom(ctx context.Context) error {
	configs, err := s.Algorithms.Custom(ctx)
	if err != nil {
		return err
	}
	want := make(map[string]bool, len(configs))
	var first error
	for _, c := range configs {
		want[c.AlgorithmID] = true
		if c.Variant() {
			if def, ok := forecast.Lookup(c.AlgorithmID); ok && def.Base == c.Base && def.Description == variantDescription(c, def) {
				continue
			}
			if err := forecast.SetVariant(forecast.VariantSpec{ID: c.AlgorithmID, Base: c.Base, Description: c.Description}); err != nil && first == nil {
				first = fmt.Errorf("variant %s: %w", c.AlgorithmID, err)
			}
			continue
		}
		spec, err := SpecOf(c)
		if err == nil {
			if def, ok := forecast.Lookup(c.AlgorithmID); ok && def.Custom && def.ImplVersion == forecast.FormulaVersion(spec.Formula) && def.Description == spec.Description && def.Category == categoryOf(spec) {
				continue
			}
			err = forecast.SetCustom(spec)
		}
		if err != nil && first == nil {
			first = fmt.Errorf("custom algorithm %s: %w", c.AlgorithmID, err)
		}
	}
	for _, id := range forecast.CustomIDs() {
		if !want[id] {
			forecast.RemoveCustom(id)
		}
	}
	return first
}

func categoryOf(spec forecast.CustomSpec) string {
	if spec.Category == "" {
		return "custom"
	}
	return spec.Category
}

// variantDescription is the description a variant is registered with: its own when set, else the base's.
func variantDescription(c *model.AlgorithmConfig, def forecast.Definition) string {
	if d := strings.TrimSpace(c.Description); d != "" {
		return d
	}
	return def.Description
}
