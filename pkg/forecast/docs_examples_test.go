package forecast

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The worked examples of the algorithm pages of the website (website/content/*/algorithm-*.html). The numbers on the pages are these: if an
// algorithm changes and an example no longer comes out as printed, this test fails and the page has to be updated with it.

func exIn(vals []float64, step time.Duration) Input {
	return Input{Values: vals, End: time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC), Step: step}
}

func exModel(t *testing.T, id string, params map[string]any) Model {
	t.Helper()
	m, err := Build(id, params)
	require.NoError(t, err)
	return m
}

func exRun(t *testing.T, m Model, in Input, horizon int) []float64 {
	t.Helper()
	out, err := m.Forecast(context.Background(), in, horizon)
	require.NoError(t, err)
	return out
}

const exFive = 5 * time.Minute

func TestDocsExampleNaiveAndSeasonalNaive(t *testing.T) {
	assert.Equal(t, []float64{1.1, 1.1, 1.1}, exRun(t, exModel(t, "naive", nil), exIn([]float64{0.8, 1.0, 1.2, 1.1}, exFive), 3))
	got := exRun(t, exModel(t, "seasonal_naive", map[string]any{"season_length_steps": 4}),
		exIn([]float64{1.0, 2.0, 3.0, 2.0, 1.2, 2.2, 3.2, 2.1}, exFive), 6)
	assert.Equal(t, []float64{1.2, 2.2, 3.2, 2.1, 1.2, 2.2}, got)
}

func TestDocsExampleEWMA(t *testing.T) {
	v := []float64{10, 12, 11, 15, 14, 16}
	level := v[0]
	want := []float64{10.6, 10.72, 12.004, 12.6028, 13.62196}
	for i, x := range v[1:] {
		level = 0.3*x + 0.7*level
		assert.InDelta(t, want[i], level, 1e-9)
	}
	assert.InDelta(t, 13.62196, exRun(t, exModel(t, "ewma", map[string]any{"alpha": 0.3}), exIn(v, exFive), 2)[1], 1e-9)
	assert.InDelta(t, 14.1049, exRun(t, exModel(t, "ewma", map[string]any{"alpha": 0.3, "bias_correction": true}), exIn(v, exFive), 1)[0], 5e-5)
	assert.InDelta(t, 13.8601, exRun(t, exModel(t, "ewma", map[string]any{"span": 5}), exIn(v, exFive), 1)[0], 5e-5)
}

func TestDocsExampleHistPercentile(t *testing.T) {
	v := []float64{40, 42, 45, 41, 60, 44, 43, 46}
	params := map[string]any{"percentile": 90, "half_life_hours": 0.5, "safety_margin": 0.1}
	assert.InDelta(t, 66.0, exRun(t, exModel(t, "hist_percentile", params), exIn(v, exFive), 2)[0], 1e-9)
	// the weights and the cumulative sum of the page
	var total float64
	for i := range v {
		total += math.Pow(0.5, float64(len(v)-1-i)/6)
	}
	assert.InDelta(t, 5.5283, total, 5e-5)
	assert.InDelta(t, 4.9755, total*0.9, 5e-5)
	median := exModel(t, "hist_percentile", map[string]any{"percentile": 50, "half_life_hours": 0.5, "safety_margin": 0})
	assert.InDelta(t, 44.0, exRun(t, median, exIn(v, exFive), 1)[0], 1e-9)
	assert.InDelta(t, 60.0, exRun(t, exModel(t, "hist_percentile", map[string]any{"percentile": 90, "half_life_hours": 0, "safety_margin": 0}), exIn(v, exFive), 1)[0], 1e-9)
}

func TestDocsExampleHoltWinters(t *testing.T) {
	v := []float64{10, 11, 12.5, 13, 14.5, 15}
	got := exRun(t, exModel(t, "holt_winters", map[string]any{"trend": true, "alpha": 0.5, "beta": 0.3}), exIn(v, exFive), 3)
	for i, w := range []float64{16.0119, 16.9379, 17.8639} {
		assert.InDelta(t, w, got[i], 5e-5)
	}
	level, trend := v[0], olsSlope(v)
	assert.InDelta(t, 1.0286, trend, 5e-5)
	wantLevel := []float64{10.5143, 11.1943, 12.2551, 13.0723, 14.2200, 15.0859}
	wantTrend := []float64{0.8743, 0.8160, 0.8895, 0.8678, 0.9518, 0.9260}
	for i, y := range v {
		base := level + trend
		prev := level
		level = 0.5*y + 0.5*base
		trend = 0.3*(level-prev) + 0.7*trend
		assert.InDelta(t, wantLevel[i], level, 5e-5)
		assert.InDelta(t, wantTrend[i], trend, 5e-5)
	}
}

func TestDocsExampleTheta(t *testing.T) {
	v := []float64{10, 12, 11, 13, 14, 16}
	got := exRun(t, exModel(t, "theta", map[string]any{"alpha": 0.5}), exIn(v, exFive), 3)
	for i, w := range []float64{15.5687, 16.1116, 16.6545} {
		assert.InDelta(t, w, got[i], 5e-5)
	}
	level, _ := sesLevel(v, 0.5)
	assert.InDelta(t, 14.5, level, 1e-9)
	assert.InDelta(t, 1.0857, olsSlope(v), 5e-5)
	assert.InDelta(t, level+olsSlope(v)/2*(0+1/0.5-math.Pow(0.5, 6)/0.5), got[0], 1e-9, "forecast(1) = level + slope/2 * (1/alpha - (1-alpha)^n / alpha)")
}

func TestDocsExampleMSTL(t *testing.T) {
	v := make([]float64, 12)
	shape := []float64{-2, 0, 3, -1}
	for i := range v {
		v[i] = 10 + 0.5*float64(i) + shape[i%4]
	}
	m := MSTL{Daily: 4}
	got := exRun(t, m, exIn(v, exFive), 4)
	for i, w := range []float64{13.3812, 15.7672, 19.1292, 15.4747} {
		assert.InDelta(t, w, got[i], 5e-5)
	}
	for i, w := range []float64{-2.1913, -0.0491, 3.0693, -0.8289} {
		assert.InDelta(t, w, m.decompose(v)[0][i], 5e-5)
	}
}

func TestDocsExampleSeasonalProfile(t *testing.T) {
	v := []float64{20, 35, 60, 30, 22, 38, 55, 32, 21, 36, 70, 29}
	params := map[string]any{"percentile": 90, "bucket_minutes": 360, "by_weekday": false, "half_life_days": 0, "min_samples_per_bucket": 2, "utc_offset_minutes": 0}
	in := Input{Values: v, End: time.Date(2026, 3, 6, 18, 0, 0, 0, time.UTC), Step: 6 * time.Hour}
	assert.Equal(t, []float64{22, 38, 70, 32}, exRun(t, exModel(t, "seasonal_profile", params), in, 4))
}

func TestDocsExamplePeakEnvelope(t *testing.T) {
	v := []float64{100, 104, 108, 112, 90, 104, 108, 112, 116, 94, 108, 112, 116, 120, 98, 112}
	env, first := envelope(v, 4)
	assert.Equal(t, []float64{112, 112, 112, 112, 112, 116, 116, 116, 116, 116, 120, 120, 120}, env)
	assert.Equal(t, 3, first)
	m := PeakEnvelope{EnvelopeWindow: 4}
	slope, intercept, now, err := m.trendLine(exIn(v, exFive))
	require.NoError(t, err)
	assert.InDelta(t, 0.7636, slope, 5e-5)
	assert.InDelta(t, 108.5455, intercept, 5e-5)
	assert.Equal(t, 120.0, now)
	got := exRun(t, m, exIn(v, exFive), 3)
	for i, w := range []float64{120.7636, 121.5273, 122.2909} {
		assert.InDelta(t, w, got[i], 5e-5)
	}
	after, ok, err := m.TimeToLimit(exIn(v, exFive), 150)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.InDelta(t, 196.4, after.Minutes(), 0.05, "(150 - 108.5455) / 0.7636 - 15 = 39.29 steps of 5 minutes")
}

func TestDocsExampleDampedTrend(t *testing.T) {
	v := []float64{50, 51, 53, 52, 55, 57, 56, 59}
	m := DampedTrend{Alpha: 0.3, Window: 6, Damping: 0.9, DownShare: 0, CapRatio: 1.5}
	level := v[0]
	for _, x := range v[1:] {
		level = 0.3*x + 0.7*level
	}
	assert.InDelta(t, 55.8341, level, 5e-5)
	assert.InDelta(t, 1.3333, theilSen(v[len(v)-6:]), 5e-5)
	got := exRun(t, m, exIn(v, exFive), 3)
	for i, w := range []float64{57.0341, 58.1141, 59.0861} {
		assert.InDelta(t, w, got[i], 5e-5)
	}
}

func TestDocsExampleRidgeAR(t *testing.T) {
	v := []float64{10, 12, 13, 15, 14, 16, 18, 17, 19, 21}
	m := RidgeAR{Lags: 2, L2Lambda: 0.1}
	x, y := m.spec().training(exIn(v, exFive), 0)
	require.Len(t, x, 8)
	assert.Equal(t, []float64{12, 10}, x[0])
	assert.Equal(t, 13.0, y[0])
	fit, err := fitRidge(context.Background(), x, y, 2, 0.1)
	require.NoError(t, err)
	assert.InDelta(t, 1.0902, fit.coef[0], 5e-5)
	assert.InDelta(t, 1.1073, fit.coef[1], 5e-5)
	assert.InDelta(t, 15.5, fit.mean[0], 1e-9)
	assert.InDelta(t, 14.375, fit.mean[1], 1e-9)
	assert.InDelta(t, 2.2913, fit.std[0], 5e-5)
	assert.InDelta(t, 2.4969, fit.std[1], 5e-5)
	assert.InDelta(t, 16.625, fit.yMean, 1e-9)
	assert.InDelta(t, 21.2931, exRun(t, m, exIn(v, exFive), 2)[0], 5e-5)
}

func TestDocsExampleKNN(t *testing.T) {
	v := []float64{10, 20, 30, 10, 20, 30, 12, 24, 36, 12, 23, 35, 12, 22}
	m := KNN{Window: 3, K: 2, Normalize: "relative"}
	cands, err := m.analogues(context.Background(), exIn(v, exFive), 3)
	require.NoError(t, err)
	require.Len(t, cands, 2)
	assert.Equal(t, 10, cands[0].end)
	assert.Equal(t, 4, cands[1].end)
	got := exRun(t, m, exIn(v, exFive), 3)
	for i, w := range []float64{34.1526, 12.2714, 23.1532} {
		assert.InDelta(t, w, got[i], 5e-5)
	}
}

func TestDocsExampleTreesDoNotExtrapolate(t *testing.T) {
	v := make([]float64, 20)
	for i := range v {
		v[i] = 10 + float64(i) // 10 .. 29
	}
	rf := RandomForest{Lags: 2, Trees: 20, MaxDepth: 3, MinLeaf: 2, FeatureRatio: 1, SampleRatio: 1, Seed: 42}
	for _, x := range exRun(t, rf, exIn(v, exFive), 3) {
		assert.InDelta(t, 27.9125, x, 5e-5)
		assert.Less(t, x, 29.0, "a tree forecast stays inside the range of the targets it was trained on")
	}

	g := []float64{10, 12, 13, 15, 14, 16, 18, 17, 19, 21}
	one := GBDT{Lags: 2, Trees: 1, LearningRate: 0.5, MaxDepth: 1, MinLeaf: 1}
	x, y := one.spec().training(exIn(g, exFive), 0)
	var base float64
	for _, v := range y {
		base += v
	}
	base /= float64(len(y))
	assert.InDelta(t, 16.625, base, 1e-9)
	resid := make([]float64, len(y))
	for i := range y {
		resid[i] = y[i] - base
	}
	b := &gbdtBuilder{d: newGBDTData(x, y), grad: resid, resid: resid, depth: 1, minLeaf: 1}
	all := make([]int, len(y))
	for i := range all {
		all[i] = i
	}
	tree := b.build(all, 0)
	assert.Equal(t, 0, tree.feature)
	assert.Equal(t, 15.0, tree.threshold)
	assert.InDelta(t, -2.125, tree.left.value, 1e-9)
	assert.InDelta(t, 2.125, tree.right.value, 1e-9)
	got := exRun(t, one, exIn(g, exFive), 2)
	assert.InDelta(t, base+0.5*2.125, got[0], 1e-9)
	assert.InDelta(t, 17.6875, got[0], 5e-5)
	two := GBDT{Lags: 2, Trees: 2, LearningRate: 0.5, MaxDepth: 1, MinLeaf: 1}
	assert.InDelta(t, 18.84375, exRun(t, two, exIn(g, exFive), 2)[0], 5e-5)
}

func TestDocsExampleCombination(t *testing.T) {
	v := make([]float64, 30)
	for i := range v {
		v[i] = 50 + 0.8*float64(i) + 3*math.Sin(float64(i))
	}
	m := exModel(t, "combination", map[string]any{"members": "ewma,holt_winters,theta"}).(Combination)
	in := exIn(v, exFive)
	wantMember := map[string][]float64{
		"ewma":         {71.3678, 71.3678, 71.3678},
		"holt_winters": {72.3166, 73.1204, 73.9242},
		"theta":        {71.6032, 71.9972, 72.3913},
	}
	wantErr := map[string]float64{"ewma": 6.5656, "holt_winters": 3.4951, "theta": 3.8190}
	var level float64
	for _, x := range v {
		level += math.Abs(x)
	}
	level /= float64(len(v))
	eps := 1e-3*level + 1e-12
	var num, den float64
	for _, member := range m.Members {
		got := exRun(t, member, in, 3)
		for i, w := range wantMember[member.ID()] {
			assert.InDelta(t, w, got[i], 5e-5)
		}
		e, ok := m.memberError(context.Background(), member, in, 3)
		require.True(t, ok)
		assert.InDelta(t, wantErr[member.ID()], e, 5e-5)
		weight := math.Pow(1/(e+eps), 2)
		num += weight * got[0]
		den += weight
	}
	assert.InDelta(t, 71.9065, exRun(t, m, in, 3)[0], 5e-5)
	assert.InDelta(t, 71.9065, num/den, 5e-5, "the first step is the weighted average of the three members")
}

func TestDocsExampleJSONParametersOfTheTreePages(t *testing.T) {
	v := make([]float64, 20)
	for i := range v {
		v[i] = 10 + float64(i)
	}
	rf := exModel(t, "random_forest", map[string]any{"lags": 2, "n_estimators": 20, "max_depth": 3, "min_samples_leaf": 2, "feature_ratio": 1, "sample_ratio": 1, "seed": 42})
	for _, x := range exRun(t, rf, exIn(v, exFive), 3) {
		assert.InDelta(t, 27.9125, x, 5e-5)
	}
	// a linear model keeps rising on the same data, which is the contrast the page draws
	ridge := exModel(t, "ridge_ar", map[string]any{"lags": 2})
	lin := exRun(t, ridge, exIn(v, exFive), 3)
	assert.Greater(t, lin[0], 29.5)
	assert.Greater(t, lin[2], lin[0])
	t.Logf("ridge on the rising series: %v", lin)

	g := []float64{10, 12, 13, 15, 14, 16, 18, 17, 19, 21}
	one := exModel(t, "gbdt", map[string]any{"lags": 2, "n_estimators": 1, "learning_rate": 0.5, "max_depth": 1, "min_samples_leaf": 1, "objective": "squared"})
	assert.InDelta(t, 17.6875, exRun(t, one, exIn(g, exFive), 2)[0], 5e-5)

	k := exModel(t, "knn", map[string]any{"window": 3, "k": 2, "normalize": "relative", "exclusion": "window"})
	kv := []float64{10, 20, 30, 10, 20, 30, 12, 24, 36, 12, 23, 35, 12, 22}
	assert.InDelta(t, 34.1526, exRun(t, k, exIn(kv, exFive), 3)[0], 5e-5)

	r := exModel(t, "ridge_ar", map[string]any{"lags": 2, "calendar_features": false, "l2_lambda": 0.1, "multi_step": "recursive"})
	rv := []float64{10, 12, 13, 15, 14, 16, 18, 17, 19, 21}
	got := exRun(t, r, exIn(rv, exFive), 2)
	assert.InDelta(t, 21.2931, got[0], 5e-5)
	assert.InDelta(t, 22.3195, got[1], 5e-5)
}

func TestDocsExampleCombinationParametersAndMedian(t *testing.T) {
	v := make([]float64, 30)
	for i := range v {
		v[i] = 50 + 0.8*float64(i) + 3*math.Sin(float64(i))
	}
	in := exIn(v, exFive)
	// the parameters printed on the page, all at their defaults
	full := exModel(t, "combination", map[string]any{"members": "ewma,holt_winters,theta", "mode": "inverse_error", "error_windows": 4,
		"under_weight": 2, "sharpness": 2, "recent_steps": 3, "trim_ratio": 3})
	got := exRun(t, full, in, 3)
	for i, w := range []float64{71.9065, 72.4398, 72.9731} {
		assert.InDelta(t, w, got[i], 5e-5)
	}
	median := exModel(t, "combination", map[string]any{"members": "ewma,holt_winters,theta", "mode": "median"})
	for i, w := range []float64{71.6032, 71.9972, 72.3913} {
		assert.InDelta(t, w, exRun(t, median, in, 3)[i], 5e-5)
	}
}

// The request and the reading of the answer shown on the model service page, against a stand-in service (nothing here is a real model).
func TestDocsExampleModelServiceRequestAndAnswer(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotBody))
		_, _ = w.Write([]byte(`{"mean":[1.2,1.3,1.4],"quantiles":{"0.9":[1.8,-0.2,2.4]}}`))
	}))
	defer srv.Close()
	t.Setenv("DOCS_EXAMPLE_KEY", "secret")

	values := make([]float64, 30)
	for i := range values {
		values[i] = 1 + 0.01*float64(i)
	}
	m := exModel(t, "model_service", map[string]any{"endpoint": srv.URL, "protocol": "finetune", "api_key_env": "DOCS_EXAMPLE_KEY", "timeout_seconds": 30, "service_quantile": 0.9})
	got := exRun(t, m, exIn(values, exFive), 3)
	assert.Equal(t, "/v1/predict", gotPath)
	assert.Equal(t, "Bearer secret", gotAuth)
	assert.EqualValues(t, 3, gotBody["horizon"])
	assert.Equal(t, []any{0.9}, gotBody["quantiles"])
	assert.Len(t, gotBody["values"], 30)
	assert.NotContains(t, gotBody, "model_key", "the key of a fine-tuned copy is sent only when the forecast is a live one")
	assert.Equal(t, []float64{1.8, 0, 2.4}, got, "the 0.9 quantile is read from the answer and the negative step is cut to 0")

	mean := exModel(t, "model_service", map[string]any{"endpoint": srv.URL, "protocol": "finetune", "service_quantile": 0})
	assert.Equal(t, []float64{1.2, 1.3, 1.4}, exRun(t, mean, exIn(values, exFive), 3))
	assert.EqualValues(t, []any{0.5}, gotBody["quantiles"], "without a quantile the service is asked for the median and the mean is used")
}
