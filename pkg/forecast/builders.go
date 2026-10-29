package forecast

import (
	"fmt"
	"strings"
	"time"
)

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
	"model_service": func(p map[string]any) (Model, error) {
		return ModelService{Protocol: p["protocol"].(string), Endpoint: strings.TrimRight(p["endpoint"].(string), "/"), APIKeyEnv: p["api_key_env"].(string),
			Timeout: time.Duration(pInt(p, "timeout_seconds")) * time.Second, FitTimeout: 30 * time.Minute, Quantile: pFloat(p, "service_quantile"),
			Settings: TuneSettings{IntervalHours: pInt(p, "finetune_interval_hours"), Mode: p["finetune_mode"].(string), Steps: pInt(p, "finetune_steps"),
				LearningRate: pFloat(p, "finetune_learning_rate"), BatchSize: pInt(p, "finetune_batch_size"), ContextLength: pInt(p, "finetune_context_length"),
				MinPoints: pInt(p, "finetune_min_points")}}, nil
	},
	"theta": func(p map[string]any) (Model, error) {
		return Theta{Season: pInt(p, "season_length_steps"), Alpha: pFloat(p, "alpha")}, nil
	},
	"mstl": func(p map[string]any) (Model, error) {
		return MSTL{Daily: pInt(p, "daily_season_steps"), Weekly: pInt(p, "weekly_season_steps")}, nil
	},
	"seasonal_profile": func(p map[string]any) (Model, error) {
		return SeasonalProfile{Percentile: pFloat(p, "percentile"), BucketMinutes: pInt(p, "bucket_minutes"), ByWeekday: pBool(p, "by_weekday"),
			HalfLifeDays: pFloat(p, "half_life_days"), MinPerBucket: pInt(p, "min_samples_per_bucket"), UTCOffsetMinutes: pInt(p, "utc_offset_minutes"),
			SafetyMargin: pFloat(p, "safety_margin")}, nil
	},
	"peak_envelope": func(p map[string]any) (Model, error) {
		return PeakEnvelope{EnvelopeWindow: pInt(p, "envelope_window_steps"), FitSteps: pInt(p, "fit_steps"), AllowDecline: pBool(p, "allow_decline"),
			SafetyMargin: pFloat(p, "safety_margin")}, nil
	},
	"combination": func(p map[string]any) (Model, error) {
		members, err := ParseMembers(p["members"].(string))
		if err != nil {
			return nil, err
		}
		return Combination{Members: members, Mode: p["mode"].(string), Windows: pInt(p, "error_windows"), UnderWeight: pFloat(p, "under_weight"),
			Sharpness: pFloat(p, "sharpness"), TrimRatio: pFloat(p, "trim_ratio"), RecentSteps: pInt(p, "recent_steps")}, nil
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
	"model_service": func(raw map[string]any) error {
		if e, _ := raw["endpoint"].(string); e != "" && !strings.HasPrefix(e, "http://") && !strings.HasPrefix(e, "https://") {
			return fmt.Errorf("endpoint must start with http:// or https://")
		}
		if q, ok := rawNum(raw, "service_quantile"); ok && q != 0 && q < 0.5 {
			return fmt.Errorf("service_quantile must be 0 (the mean) or at least 0.5, got %v", q)
		}
		protocol, _ := raw["protocol"].(string)
		if protocol == "" {
			protocol = ProtocolFinetune
		}
		if hours, ok := rawNum(raw, "finetune_interval_hours"); ok && hours > 0 && protocol != ProtocolFinetune {
			return fmt.Errorf("protocol %s cannot be fine-tuned; use the finetune protocol or set finetune_interval_hours to 0", protocol)
		}
		return nil
	},
	"ewma": func(raw map[string]any) error {
		_, hasAlpha := rawNum(raw, "alpha")
		span, hasSpan := rawNum(raw, "span")
		if hasAlpha && hasSpan && span > 0 {
			return fmt.Errorf("alpha and span are two ways to give the same smoothing; set only one")
		}
		return nil
	},
	"seasonal_profile": func(raw map[string]any) error {
		if v, ok := rawNum(raw, "bucket_minutes"); ok && (v < 1 || v > 720 || v != float64(int(v)) || 1440%int(v) != 0) {
			return fmt.Errorf("bucket_minutes must be a divisor of 1440 (a day) between 1 and 720")
		}
		return nil
	},
	"combination": func(raw map[string]any) error {
		list, ok := raw["members"].(string)
		if !ok {
			list = defaultCombinationMembers
		}
		_, err := ParseMembers(list)
		return err
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
