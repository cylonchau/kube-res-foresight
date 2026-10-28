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
	"macd_trend": func(p map[string]any) (Model, error) {
		return MACDTrend{Fast: pInt(p, "fast_span"), Slow: pInt(p, "slow_span"), Signal: pInt(p, "signal_span"), MaxAdjustment: pFloat(p, "max_adjustment")}, nil
	},
	"holt_winters": func(p map[string]any) (Model, error) {
		return HoltWinters{Season: pInt(p, "season_length_steps"), Trend: pBool(p, "trend"), Damped: pBool(p, "damped"),
			Alpha: pFloat(p, "alpha"), Beta: pFloat(p, "beta"), Gamma: pFloat(p, "gamma"),
			Multiplicative: p["seasonal_mode"].(string) == "multiplicative", DampingPhi: pFloat(p, "damping_phi"), NonNegative: pBool(p, "non_negative")}, nil
	},
	"theta": func(p map[string]any) (Model, error) {
		return Theta{Season: pInt(p, "season_length_steps"), Alpha: pFloat(p, "alpha")}, nil
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
		return KNN{Window: pInt(p, "window"), K: pInt(p, "k"), Normalize: p["normalize"].(string)}, nil
	},
	"naive_bayes": func(p map[string]any) (Model, error) {
		return NaiveBayes{Lags: pInt(p, "lags"), Bins: pInt(p, "bins"), CovLags: pInt(p, "covariate_lags"), Calendar: pBool(p, "calendar_features"),
			Output: p["output"].(string), MaxTrainRows: pInt(p, "training_window_steps")}, nil
	},
	"rnn":  recurrent("rnn"),
	"gru":  recurrent("gru"),
	"lstm": recurrent("lstm"),
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

func recurrent(cell string) Builder {
	return func(p map[string]any) (Model, error) {
		return Recurrent{Cell: cell, Lags: pInt(p, "lags"), Hidden: pInt(p, "hidden_size"), Epochs: pInt(p, "epochs"), LearningRate: pFloat(p, "learning_rate"),
			MaxSamples: pInt(p, "max_samples"), Calendar: pBool(p, "calendar_features"), UseCovariate: pBool(p, "use_covariates"), Seed: int64(pInt(p, "seed"))}, nil
	}
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
	"macd_trend": func(raw map[string]any) error {
		fast, hasFast := rawNum(raw, "fast_span")
		slow, hasSlow := rawNum(raw, "slow_span")
		if !hasFast {
			fast = 12
		}
		if !hasSlow {
			slow = 26
		}
		if (hasFast || hasSlow) && fast >= slow {
			return fmt.Errorf("fast_span (%v) must be smaller than slow_span (%v)", fast, slow)
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
