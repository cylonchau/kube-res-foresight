package forecast

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

func formulaModel(t *testing.T, spec FormulaSpec, values map[string]any) Model {
	t.Helper()
	f, err := ParseFormula(spec)
	if err != nil {
		t.Fatal(err)
	}
	if values == nil {
		values = DefaultParamValues(spec.Params)
	}
	norm, err := NormalizeSpecs(spec.Params, values, "t")
	if err != nil {
		t.Fatal(err)
	}
	m, err := f.With(norm)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestFormulaEvaluates(t *testing.T) {
	spec := FormulaSpec{
		Expr:       "last(x) * (1 + g) + k * slope(x, 4)",
		Params:     []ParamSpec{{Name: "g", Type: "float", Default: 0.1, Min: f64(0), Max: f64(1), Description: "growth"}},
		MinSamples: 4,
	}
	m := formulaModel(t, spec, nil)
	in := Input{Values: []float64{1, 2, 3, 4}, Step: time.Minute}
	out, err := m.Forecast(context.Background(), in, 2)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(out[0]-5.4) > 1e-9 || math.Abs(out[1]-6.4) > 1e-9 {
		t.Fatalf("got %v", out)
	}
}

func TestFormulaRecursiveAndClamp(t *testing.T) {
	m := formulaModel(t, FormulaSpec{Expr: "last(x) - 3", Recursive: true, MinSamples: 1}, nil)
	out, err := m.Forecast(context.Background(), Input{Values: []float64{5}, Step: time.Minute}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if out[0] != 2 || out[1] != 0 || out[2] != 0 {
		t.Fatalf("got %v", out)
	}
}

func TestFormulaRejects(t *testing.T) {
	cases := map[string]string{
		"x":                             "result is a series",
		"foo(x)":                        "unknown function",
		"last(1)":                       "must be a series",
		"mean(x)":                       "takes 2",
		"y + 1":                         "not a variable",
		"last(x) +":                     "ends where",
		"(last(x)":                      "never closed",
		"last(x) $ 1":                   "unexpected character",
		"x + 1":                         "series is not a number",
		"last(x) 3":                     "after the end",
		"if(1,2)":                       "3 arguments",
		strings.Repeat("1+", 300) + "1": "too long",
	}
	for expr, want := range cases {
		_, err := ParseFormula(FormulaSpec{Expr: expr, MinSamples: 1})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", expr, err, want)
		}
	}
	if _, err := ParseFormula(FormulaSpec{Expr: "1", MinSamples: 1, Params: []ParamSpec{{Name: "k", Type: "int", Default: 1, Description: "d"}}}); err == nil {
		t.Error("a parameter named k must be refused")
	}
}

func TestFormulaRuntimeErrors(t *testing.T) {
	m := formulaModel(t, FormulaSpec{Expr: "1 / (last(x) - 2)", MinSamples: 1}, nil)
	if _, err := m.Forecast(context.Background(), Input{Values: []float64{2}, Step: time.Minute}, 1); err == nil {
		t.Fatal("division by zero must fail")
	}
	short := formulaModel(t, FormulaSpec{Expr: "last(x)", MinSamples: 5}, nil)
	if _, err := short.Forecast(context.Background(), Input{Values: []float64{1}, Step: time.Minute}, 1); err == nil {
		t.Fatal("short history must fail")
	}
}

func TestFormulaCovariateAndTry(t *testing.T) {
	hist, out, err := TryFormula(FormulaSpec{Expr: "mean(x, 8) + 0.5 * last(c_memory)", MinSamples: 10}, nil, 4)
	if err != nil || len(out) != 4 || len(hist) != 96 {
		t.Fatalf("%v %v", err, out)
	}
	if _, _, err := TryFormula(FormulaSpec{Expr: "1/0", MinSamples: 1}, nil, 2); err == nil {
		t.Fatal("1/0 must fail the dry run")
	}
}

func TestFormulaRegistered(t *testing.T) {
	d, ok := Lookup("formula")
	if !ok || !d.Dynamic {
		t.Fatal("formula is not registered as dynamic")
	}
	if _, err := Normalize("formula", nil); err == nil {
		t.Fatal("a dynamic implementation has no fixed schema")
	}
}

func TestCustomAlgorithmEntersTheRegistry(t *testing.T) {
	spec := CustomSpec{ID: "my_peak", Description: "peak of the window", Formula: FormulaSpec{
		Expr: "pct(x, w, p) * margin", MinSamples: 12,
		Params: []ParamSpec{
			{Name: "w", Type: "int", Default: 12, Min: f64(2), Max: f64(500), Unit: "steps", Description: "window"},
			{Name: "p", Type: "float", Default: 95.0, Min: f64(1), Max: f64(100), Description: "percentile"},
			{Name: "margin", Type: "float", Default: 1.1, Min: f64(1), Max: f64(3), Description: "headroom"},
		},
	}}
	if err := SetCustom(spec); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { RemoveCustom("my_peak") })
	d, ok := Lookup("my_peak")
	if !ok || !d.Custom || d.MinSamples != 12 || !strings.HasPrefix(d.ImplVersion, "formula-") {
		t.Fatalf("%+v %v", d, ok)
	}
	m, err := Build("my_peak", map[string]any{"w": 4})
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.Forecast(context.Background(), Input{Values: []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14}, Step: time.Minute}, 2)
	if err != nil || math.Abs(out[0]-13.85*1.1) > 1e-6 {
		t.Fatalf("%v %v", out, err)
	}
	if _, err := Build("my_peak", map[string]any{"w": 1}); err == nil {
		t.Fatal("w below the minimum must be refused")
	}
	found := false
	for _, d := range Definitions() {
		found = found || d.ID == "my_peak"
	}
	if !found {
		t.Fatal("not listed")
	}
	for _, id := range IDs() {
		if id == "my_peak" {
			t.Fatal("a custom algorithm is not a system-created setting")
		}
	}
	// editing the formula changes the model version
	spec.Formula.Expr = "pct(x, w, p) * margin + 1"
	if err := SetCustom(spec); err != nil {
		t.Fatal(err)
	}
	if d2, _ := Lookup("my_peak"); d2.ImplVersion == d.ImplVersion {
		t.Fatal("the version must follow the formula")
	}
	if err := SetCustom(CustomSpec{ID: "ewma", Formula: spec.Formula}); err == nil {
		t.Fatal("a built-in id must be refused")
	}
	if err := SetCustom(CustomSpec{ID: "bad_one", Formula: FormulaSpec{Expr: "1/0", MinSamples: 1}}); err == nil {
		t.Fatal("a formula that cannot run must be refused")
	}
	if _, ok := Lookup("bad_one"); ok {
		t.Fatal("a refused algorithm must not be registered")
	}
}
