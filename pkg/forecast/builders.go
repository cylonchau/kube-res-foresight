package forecast

import "fmt"

// builtinBuilders construct the models of the algorithms that ship with the platform. The parameters are already
// validated and complete, so a lookup cannot miss.
var builtinBuilders = map[string]Builder{
	"naive": func(map[string]any) (Model, error) { return Naive{}, nil },
	"seasonal_naive": func(p map[string]any) (Model, error) {
		return SeasonalNaive{Season: pInt(p, "season_length_steps")}, nil
	},
	"ewma": func(p map[string]any) (Model, error) {
		return EWMA{Alpha: pFloat(p, "alpha"), Span: pInt(p, "span"), BiasCorrection: pBool(p, "bias_correction")}, nil
	},
	"holt_winters": func(p map[string]any) (Model, error) {
		return HoltWinters{Season: pInt(p, "season_length_steps"), Trend: pBool(p, "trend"), Damped: pBool(p, "damped"),
			Alpha: pFloat(p, "alpha"), Beta: pFloat(p, "beta"), Gamma: pFloat(p, "gamma"),
			Multiplicative: p["seasonal_mode"].(string) == "multiplicative", DampingPhi: pFloat(p, "damping_phi"), NonNegative: pBool(p, "non_negative")}, nil
	},
	"theta": func(p map[string]any) (Model, error) {
		return Theta{Season: pInt(p, "season_length_steps"), Alpha: pFloat(p, "alpha")}, nil
	},
	"mstl": func(p map[string]any) (Model, error) {
		return MSTL{Daily: pInt(p, "daily_season_steps"), Weekly: pInt(p, "weekly_season_steps")}, nil
	},
	"hist_percentile": func(p map[string]any) (Model, error) {
		return HistPercentile{Percentile: pFloat(p, "percentile"), HalfLifeHours: pFloat(p, "half_life_hours"), SafetyMargin: pFloat(p, "safety_margin")}, nil
	},
	"random_forest": func(p map[string]any) (Model, error) {
		return RandomForest{Lags: pInt(p, "lags"), Calendar: pBool(p, "calendar_features"), Trees: pInt(p, "n_estimators"), MaxDepth: pInt(p, "max_depth"),
			MinLeaf: pInt(p, "min_samples_leaf"), FeatureRatio: pFloat(p, "feature_ratio"), SampleRatio: pFloat(p, "sample_ratio"), MaxTrainRows: pInt(p, "training_window_steps"),
			CovLags: pInt(p, "covariate_lags"), Seed: int64(pInt(p, "seed"))}, nil
	},
	"knn": func(p map[string]any) (Model, error) {
		return KNN{Window: pInt(p, "window"), K: pInt(p, "k"), Normalize: p["normalize"].(string),
			AllowOverlap: p["exclusion"].(string) == "none", SameTimeOfDay: pBool(p, "same_time_of_day"), TimeToleranceMinutes: pInt(p, "time_tolerance_minutes")}, nil
	},
	"ridge_ar": func(p map[string]any) (Model, error) {
		return RidgeAR{Lags: pInt(p, "lags"), Calendar: pBool(p, "calendar_features"), L2Lambda: pFloat(p, "l2_lambda"), MaxTrainRows: pInt(p, "training_window_steps"),
			CovLags: pInt(p, "covariate_lags"), Direct: p["multi_step"].(string) == "direct", DirectAnchors: pInt(p, "direct_anchors")}, nil
	},
	"gbdt": func(p map[string]any) (Model, error) {
		return GBDT{Lags: pInt(p, "lags"), Calendar: pBool(p, "calendar_features"), Trees: pInt(p, "n_estimators"), LearningRate: pFloat(p, "learning_rate"),
			MaxDepth: pInt(p, "max_depth"), MinLeaf: pInt(p, "min_samples_leaf"), Quantile: p["objective"].(string) == "quantile",
			Alpha: pFloat(p, "alpha"), MaxTrainRows: pInt(p, "training_window_steps"), CovLags: pInt(p, "covariate_lags"),
			Direct: p["multi_step"].(string) == "direct", DirectAnchors: pInt(p, "direct_anchors")}, nil
	},
}

// builtinChecks reject combinations of parameters that are each valid alone.
var builtinChecks = map[string]func(raw map[string]any) error{
	"ewma": func(raw map[string]any) error {
		_, hasAlpha := rawNum(raw, "alpha")
		span, hasSpan := rawNum(raw, "span")
		if hasAlpha && hasSpan && span > 0 {
			return fmt.Errorf("alpha and span are two ways to give the same smoothing; set only one")
		}
		return nil
	},
	"holt_winters": func(raw map[string]any) error {
		if mode, _ := raw["seasonal_mode"].(string); mode == "multiplicative" {
			if season, ok := rawNum(raw, "season_length_steps"); !ok || season < 2 {
				return fmt.Errorf("multiplicative seasonality needs season_length_steps of at least 2")
			}
		}
		return nil
	},
}

func rawNum(raw map[string]any, name string) (float64, bool) {
	switch n := raw[name].(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func pInt(p map[string]any, k string) int       { return p[k].(int) }
func pFloat(p map[string]any, k string) float64 { return p[k].(float64) }
func pBool(p map[string]any, k string) bool     { return p[k].(bool) }
