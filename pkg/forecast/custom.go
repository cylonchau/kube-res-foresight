package forecast

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// A custom algorithm is a formula algorithm an administrator wrote on the pages: an id, a parameter schema and a
// formula. It is registered next to the built-in ones, so the lists, the validation, the forecast, the backtest and the
// scoring treat it exactly like them. The registry of customs is rebuilt from the database at start-up and changed when
// an algorithm is created, edited or deleted.
var (
	customMu sync.RWMutex
	customs  = map[string]Implementation{}

	customID = regexp.MustCompile(`^[a-z][a-z0-9_]{2,39}$`)
)

// CustomSpec is a custom algorithm as stored.
type CustomSpec struct {
	ID          string
	Category    string
	Description string
	Formula     FormulaSpec
}

// VariantSpec is an algorithm derived from a built-in one: the code and the parameter schema of the base, under an
// id of its own, so one algorithm can run several times with different parameters (an EWMA that reacts fast next to
// one that smooths).
type VariantSpec struct {
	ID          string
	Base        string
	Description string
}

// ValidCustomID reports whether id can name a custom algorithm.
func ValidCustomID(id string) error {
	if !customID.MatchString(id) {
		return fmt.Errorf("the id must be 3-40 characters: lowercase letters, digits and underscores, starting with a letter")
	}
	if _, builtin := byID[id]; builtin || id == "formula" {
		return fmt.Errorf("%q is the id of a built-in algorithm", id)
	}
	return nil
}

// SetCustom compiles a custom algorithm, dry-runs it on a synthetic series and registers it, replacing the algorithm
// with the same id. Nothing is registered when it fails.
func SetCustom(spec CustomSpec) error {
	if err := ValidCustomID(spec.ID); err != nil {
		return err
	}
	if _, _, err := TryFormula(spec.Formula, nil, 8); err != nil {
		return err
	}
	f, err := ParseFormula(spec.Formula)
	if err != nil {
		return err
	}
	cat := spec.Category
	if cat == "" {
		cat = "custom"
	}
	impl := Implementation{
		Definition: Definition{
			ID: spec.ID, Category: cat, Version: "1",
			// the formula is part of the model version: editing it makes a different model
			ImplVersion: FormulaVersion(spec.Formula),
			Covariates:  len(f.series) > 0, Custom: true, Formula: spec.Formula.Expr, Recursive: spec.Formula.Recursive,
			MinSamples:  spec.Formula.MinSamples,
			Description: spec.Description,
			Complexity:  "O(h x length of the history), no training",
			MinHistory:  fmt.Sprintf("%d observations", spec.Formula.MinSamples),
			Params:      append([]ParamSpec{}, spec.Formula.Params...),
		},
		Build: func(p map[string]any) (Model, error) { return f.With(p) },
	}
	customMu.Lock()
	customs[spec.ID] = impl
	customMu.Unlock()
	return nil
}

// SetVariant registers a variant of a built-in algorithm, replacing the variant with the same id. An algorithm without
// parameters cannot have one: a variant only differs by its parameters. A variant of a baseline (a seasonal naive with
// another period) is not itself the reference the other algorithms are measured against, so it is no baseline.
func SetVariant(spec VariantSpec) error {
	if err := ValidCustomID(spec.ID); err != nil {
		return err
	}
	i, ok := byID[spec.Base]
	if !ok || registry[i].Dynamic {
		return fmt.Errorf("%q is not a built-in algorithm a variant can be derived from", spec.Base)
	}
	base := registry[i]
	if len(base.Params) == 0 {
		return fmt.Errorf("%q has no parameters, so a variant of it would be the same algorithm", spec.Base)
	}
	impl := base
	impl.Definition.ID, impl.Definition.Base, impl.Definition.Baseline = spec.ID, spec.Base, false
	impl.Definition.Params = append([]ParamSpec{}, base.Params...)
	if d := strings.TrimSpace(spec.Description); d != "" {
		impl.Definition.Description = d
	}
	customMu.Lock()
	customs[spec.ID] = impl
	customMu.Unlock()
	return nil
}

// RemoveCustom unregisters a custom algorithm.
func RemoveCustom(id string) {
	customMu.Lock()
	delete(customs, id)
	customMu.Unlock()
}

func findCustom(id string) (Implementation, bool) {
	customMu.RLock()
	defer customMu.RUnlock()
	impl, ok := customs[id]
	return impl, ok
}

func customDefinitions() []Definition {
	customMu.RLock()
	defer customMu.RUnlock()
	out := make([]Definition, 0, len(customs))
	for _, impl := range customs {
		out = append(out, impl.Definition)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out
}

// FormulaVersion is the implementation version of a formula: it follows the formula and its parameter schema, so
// editing either makes a different model.
func FormulaVersion(f FormulaSpec) string {
	raw, _ := json.Marshal(f)
	sum := sha256.Sum256(raw)
	return "formula-" + hex.EncodeToString(sum[:])[:12]
}

// CustomIDs lists the ids of the registered custom algorithms.
func CustomIDs() []string {
	defs := customDefinitions()
	ids := make([]string, len(defs))
	for i, d := range defs {
		ids[i] = d.ID
	}
	return ids
}
