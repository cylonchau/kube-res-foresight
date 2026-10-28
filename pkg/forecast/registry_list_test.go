package forecast

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The algorithms the platform has, as a list a person can check the algorithm page against. Adding an algorithm means adding it here.
func TestTheRegistryHoldsTheImplementedAlgorithms(t *testing.T) {
	want := []string{"naive", "seasonal_naive", "ewma", "macd_trend", "hist_percentile", "holt_winters", "theta", "mstl", "ridge_ar", "knn",
		"random_forest", "naive_bayes", "gbdt", "rnn", "gru", "lstm"}
	var have []string
	for _, d := range Definitions() {
		// the formula implementation is the base of the administrator's own algorithms, not an algorithm of its own
		if !d.Custom && !d.Dynamic {
			have = append(have, d.ID)
		}
	}
	assert.ElementsMatch(t, want, have, "every built-in algorithm is in the list above, and the list has no algorithm that is not registered")
}

// An algorithm that shows only a forecast time and a number cannot be checked: the baselines and the statistical algorithms say how
// they compute, and naive says what it is for.
func TestTheBaselinesExplainTheirCalculation(t *testing.T) {
	for _, id := range []string{"naive", "seasonal_naive", "ewma", "hist_percentile", "holt_winters", "theta", "mstl", "macd_trend"} {
		d, ok := Lookup(id)
		require.True(t, ok, id)
		assert.NotEmpty(t, d.Method, id)
	}
	d, _ := Lookup("naive")
	assert.Contains(t, d.Method, "ŷ(T+h) = y(T)")
	assert.Contains(t, d.Method, "基线")
}
