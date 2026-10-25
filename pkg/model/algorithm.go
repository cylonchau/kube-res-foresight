package model

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func init() {
	RegisterModel(&AlgorithmConfig{})
}

// AlgorithmConfig is the platform-wide setting of one catalog algorithm: whether it is enabled, the parameters it runs
// with and how it is trusted. There is one row per catalog algorithm, created by the system; it applies to every
// module. A module is associated with every enabled algorithm automatically, so nothing is created or bound by hand.
type AlgorithmConfig struct {
	ID        uint      `gorm:"primaryKey" json:"id" column_setting:"true"`
	CreatedAt time.Time `json:"created_at" column_setting:"false"`
	UpdatedAt time.Time `json:"updated_at" column_setting:"true"`

	// AlgorithmID is the catalog algorithm; the results of the algorithm are stored under it.
	AlgorithmID string `gorm:"type:varchar(40);uniqueIndex;not null" json:"algorithm_id" column_setting:"true"`
	Description string `gorm:"type:varchar(255)" json:"description" column_setting:"false"`
	Enabled     bool   `gorm:"not null" json:"enabled" column_setting:"true"`
	// Params is a JSON object validated against the algorithm schema; empty means every default.
	Params string `gorm:"type:text" json:"params" column_setting:"false"`
	// Weight multiplies the score of the algorithm when a module ranks the algorithms (1 leaves it as is).
	Weight float64 `gorm:"not null;default:1" json:"weight" column_setting:"true"`
	// TrainBudgetSeconds stops a model that takes longer than this (the forecast then counts as failed and the
	// fallback applies); 0 = a default.
	TrainBudgetSeconds int `gorm:"not null;default:0" json:"train_budget_seconds" column_setting:"false"`
	// The promotion fields are the rule by which a backtest lets an algorithm other than a baseline be used.
	PromoteMinPoints      int     `gorm:"not null;default:0" json:"promote_min_points" column_setting:"false"`
	PromoteMinImprovement float64 `gorm:"not null;default:0" json:"promote_min_improvement" column_setting:"false"`
	PromotePeakTolerance  float64 `gorm:"not null;default:0" json:"promote_peak_tolerance" column_setting:"false"`

	// The fields below describe a custom algorithm, one an administrator wrote as a formula; a built-in algorithm has
	// none of them. Formula is the expression, ParamSchema the JSON list of its parameters (name, type, default, range,
	// unit, description), MinSamples the shortest history it accepts and Recursive whether each forecast step is fed
	// back as history for the next.
	//
	// Base is set instead of a formula for a variant: an algorithm derived from a built-in one (the same code under an id of
	// its own, with its own parameter values).
	Base        string `gorm:"type:varchar(40)" json:"base" column_setting:"false"`
	Category    string `gorm:"type:varchar(30)" json:"category" column_setting:"false"`
	Formula     string `gorm:"type:text" json:"formula" column_setting:"false"`
	ParamSchema string `gorm:"type:text" json:"param_schema" column_setting:"false"`
	MinSamples  int    `gorm:"not null;default:0" json:"min_samples" column_setting:"false"`
	Recursive   bool   `gorm:"not null;default:false" json:"recursive" column_setting:"false"`
}

// Custom reports whether the algorithm is one an administrator wrote.
func (c *AlgorithmConfig) Custom() bool { return strings.TrimSpace(c.Formula) != "" }

// Variant reports whether the algorithm is derived from a built-in one.
func (c *AlgorithmConfig) Variant() bool { return strings.TrimSpace(c.Base) != "" }

// UserDefined reports whether an administrator created the algorithm (a formula or a variant), which is what can be
// edited beyond its parameters and deleted.
func (c *AlgorithmConfig) UserDefined() bool { return c.Custom() || c.Variant() }

func (AlgorithmConfig) TableName() string { return "algorithm_config" }

// Validate checks the numbers of the config; the algorithm and its parameters are checked by the caller against the
// catalog.
func (c *AlgorithmConfig) Validate() error {
	if strings.TrimSpace(c.AlgorithmID) == "" {
		return fmt.Errorf("algorithm_id is required")
	}
	if c.Weight == 0 {
		c.Weight = 1
	}
	if c.Weight < 0 || c.Weight > 100 {
		return fmt.Errorf("weight must be between 0 and 100")
	}
	if c.TrainBudgetSeconds < 0 || c.TrainBudgetSeconds > 3600 {
		return fmt.Errorf("train_budget_seconds must be between 0 and 3600")
	}
	if c.PromoteMinPoints < 0 || c.PromoteMinImprovement < 0 || c.PromoteMinImprovement >= 1 || c.PromotePeakTolerance < 0 {
		return fmt.Errorf("invalid promotion policy")
	}
	return nil
}

// AlgorithmRun is what one enabled algorithm does for one module: the algorithm with its parameters, reading the
// module's metric sources for the module's workload. It is derived, never stored: the module owns the history (ID is
// the module's id, the owner of the series, predictions, backtests and evaluations) and the results are filed under
// the algorithm id (Name).
type AlgorithmRun struct {
	// ID is the id of the module; Name the algorithm id.
	ID   uint
	Name string

	AlgorithmID string
	Params      string
	Weight      float64

	// What the PromQL templates of the sources are rendered with.
	Namespace string
	Workload  string
	Service   string
	Container string

	LookbackHours  int
	HorizonMinutes int
	MaxFillSteps   int
	// StepSeconds is the sampling granularity of the dataset (0 = the step of each source); MinPoints the fewest
	// points a source needs before it is forecast.
	StepSeconds int
	MinPoints   int
	// WindowSteps is the training window in steps (0 = the whole history range).
	WindowSteps int

	TrainBudgetSeconds      int
	FallbackMinHistoryRatio float64
	MaxMissingRatio         float64
	PromoteMinPoints        int
	PromoteMinImprovement   float64
	PromotePeakTolerance    float64

	// Sources are the metric sources of the module (shared by every algorithm of the module).
	Sources []*MetricSource
}

// NewRun combines a module and an algorithm config; the sources are the module's.
func NewRun(m *ForecastModule, c *AlgorithmConfig) *AlgorithmRun {
	return &AlgorithmRun{ID: m.ID, Name: c.AlgorithmID, AlgorithmID: c.AlgorithmID, Params: c.Params, Weight: c.Weight,
		Namespace: m.Namespace, Workload: m.Workload, Service: m.Service, Container: m.Container, LookbackHours: m.LookbackHours, HorizonMinutes: m.HorizonMinutes, MaxFillSteps: m.MaxFillSteps, StepSeconds: m.StepSeconds, MinPoints: m.MinPoints, WindowSteps: m.WindowSteps,
		TrainBudgetSeconds: c.TrainBudgetSeconds, FallbackMinHistoryRatio: m.FallbackMinHistoryRatio, MaxMissingRatio: m.MaxMissingRatio,
		PromoteMinPoints: c.PromoteMinPoints, PromoteMinImprovement: c.PromoteMinImprovement, PromotePeakTolerance: c.PromotePeakTolerance,
		Sources: m.Sources}
}

// Key is the name under which the algorithm's results are stored.
func (a *AlgorithmRun) Key() string { return a.Name }

// SourceKeys are the keys of the enabled sources that are forecast, in a stable order.
func (a *AlgorithmRun) SourceKeys() []string {
	var keys []string
	for _, s := range a.ActiveSources() {
		if s.Basis == BasisForecast {
			keys = append(keys, s.Key)
		}
	}
	return keys
}

// Covers reports whether the instance forecasts the source.
func (a *AlgorithmRun) Covers(key string) bool {
	if s := a.Source(key); s != nil {
		return s.Enabled && s.Basis == BasisForecast
	}
	return false
}

// SortedKeys returns the keys of all sources, sorted (for signatures).
func (a *AlgorithmRun) SortedKeys() []string {
	keys := make([]string, 0, len(a.Sources))
	for _, s := range a.Sources {
		keys = append(keys, s.Key)
	}
	sort.Strings(keys)
	return keys
}

// ---- the run's view of the module's sources

// Source returns the source with the key, or nil.
func (a *AlgorithmRun) Source(key string) *MetricSource {
	for _, s := range a.Sources {
		if s.Key == key {
			return s
		}
	}
	return nil
}

// ActiveSources are the enabled sources, in a stable order: resource order, then priority, then id.
func (a *AlgorithmRun) ActiveSources() []*MetricSource {
	out := make([]*MetricSource, 0, len(a.Sources))
	for _, s := range a.Sources {
		if s.Enabled {
			out = append(out, s)
		}
	}
	rank := map[string]int{}
	for i, r := range SourceResources {
		rank[r] = i
	}
	sort.SliceStable(out, func(a, b int) bool {
		if rank[out[a].Resource] != rank[out[b].Resource] {
			return rank[out[a].Resource] < rank[out[b].Resource]
		}
		if out[a].Priority != out[b].Priority {
			return out[a].Priority < out[b].Priority
		}
		return out[a].ID < out[b].ID
	})
	return out
}

// MappingID returns the metric mapping of an enabled source, 0 when there is none.
func (a *AlgorithmRun) MappingID(key string) uint {
	if s := a.Source(key); s != nil && s.Enabled {
		return s.MappingID
	}
	return 0
}

// ConfiguredMetrics lists the keys of the enabled sources in a stable order.
func (a *AlgorithmRun) ConfiguredMetrics() []string {
	sources := a.ActiveSources()
	keys := make([]string, len(sources))
	for i, s := range sources {
		keys[i] = s.Key
	}
	return keys
}

// KindOf returns the kind of the source with the key ("" when unknown).
func (a *AlgorithmRun) KindOf(key string) string {
	if s := a.Source(key); s != nil {
		return s.Kind
	}
	return ""
}

// FirstOfKind returns the enabled source of a kind with the lowest priority number, or nil.
func (a *AlgorithmRun) FirstOfKind(kind string) *MetricSource {
	for _, s := range a.ActiveSources() {
		if s.Kind == kind {
			return s
		}
	}
	return nil
}

// OfResource returns the enabled sources feeding a resource.
func (a *AlgorithmRun) OfResource(resource string) []*MetricSource {
	var out []*MetricSource
	for _, s := range a.ActiveSources() {
		if s.Resource == resource {
			out = append(out, s)
		}
	}
	return out
}
