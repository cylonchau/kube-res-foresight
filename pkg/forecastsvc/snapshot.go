package forecastsvc

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// SnapshotInterval is the longest a module goes without a snapshot even when nothing changed: the history then still shows
// that the recommendation was checked and stood.
const SnapshotInterval = time.Hour

// snapshotWorthKeeping says whether a new recommendation is a snapshot of the module's history, and why: the first one, one
// whose outcome differs from the previous recommendation, or one that comes an hour after the last snapshot. A recommendation
// that is the same answer as the one before it only keeps the current recommendation valid.
func snapshotWorthKeeping(rec, prev *model.Recommendation, lastSnapshot *time.Time, now time.Time) (bool, string) {
	if prev == nil {
		return true, "first recommendation of the module"
	}
	switch {
	case rec.Mode != prev.Mode:
		return true, fmt.Sprintf("mode %s -> %s", prev.Mode, rec.Mode)
	case rec.ReplicasSet != prev.ReplicasSet || rec.Replicas != prev.Replicas:
		return true, fmt.Sprintf("replicas %d -> %d", prev.Replicas, rec.Replicas)
	case rec.ResourceVersion != prev.ResourceVersion:
		return true, fmt.Sprintf("Pod resource spec v%d -> v%d", prev.ResourceVersion, rec.ResourceVersion)
	case rec.CPUSet != prev.CPUSet || rec.CPUMilli != prev.CPUMilli || rec.MemorySet != prev.MemorySet || rec.MemoryBytes != prev.MemoryBytes:
		return true, "CPU or memory changed"
	case rec.GPUSet != prev.GPUSet || rec.GPU != prev.GPU:
		return true, "GPU changed"
	case rec.Algorithm != prev.Algorithm:
		return true, fmt.Sprintf("algorithm %s -> %s", orNone(prev.Algorithm), orNone(rec.Algorithm))
	case rec.CPUSource != prev.CPUSource || rec.MemorySource != prev.MemorySource || rec.ReplicasSource != prev.ReplicasSource || rec.GPUSource != prev.GPUSource:
		return true, "source of a value changed"
	case rec.Degraded != prev.Degraded:
		if rec.Degraded {
			return true, "degraded"
		}
		return true, "recovered from degradation"
	case rec.OOMFloor != prev.OOMFloor:
		return true, "OOM protection changed"
	case rec.EffectiveMaxReplicas != prev.EffectiveMaxReplicas:
		return true, "replica ceiling changed"
	}
	if lastSnapshot == nil || now.Sub(*lastSnapshot) >= SnapshotInterval {
		return true, "periodic: the recommendation stood for an hour"
	}
	return false, ""
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// Snapshot states: what became of a recommendation as the system acted on it.
const (
	SnapshotPending  = "pending"  // current and valid: nothing has used it yet
	SnapshotApplied  = "applied"  // the operator scaled to it (or found the replicas already right) or an admission gave Pods its resources
	SnapshotFailed   = "failed"   // the operator reported an error or a conflict and nothing was applied
	SnapshotExpired  = "expired"  // no longer current, or past its validity, and never applied
	SnapshotAdvisory = "advisory" // current and valid, in a mode that only recommends: nothing will apply it
)

// SnapshotState is the state of a snapshot from what was reported for its span. current says it is still the newest snapshot of
// its module. The reason is a sentence that names the figures behind the state.
func SnapshotState(rec *model.Recommendation, out rdb.SnapshotOutcome, current bool, now time.Time) (state, reason string) {
	done := out.Applied + out.Observed + out.Admitted
	switch {
	case done > 0:
		parts := []string{}
		if out.Applied > 0 {
			parts = append(parts, fmt.Sprintf("the operator scaled %d time(s)", out.Applied))
		}
		if out.Observed > 0 {
			parts = append(parts, fmt.Sprintf("the replicas were already right %d time(s)", out.Observed))
		}
		if out.Admitted > 0 {
			parts = append(parts, fmt.Sprintf("%d admission answer(s) gave Pods its resources", out.Admitted))
		}
		return SnapshotApplied, strings.Join(parts, "; ")
	case out.Error+out.Conflict > 0:
		reason = fmt.Sprintf("%d error(s) and %d conflict(s) reported by the operator, nothing applied", out.Error, out.Conflict)
		if out.LastError != "" {
			reason += ": " + out.LastError
		}
		return SnapshotFailed, reason
	case current && !rec.ValidUntil.IsZero() && now.After(rec.ValidUntil):
		return SnapshotExpired, "valid until " + rec.ValidUntil.UTC().Format(time.RFC3339) + " and nothing used it"
	case current && rec.Mode == model.ModeRecommend:
		return SnapshotAdvisory, "the module only recommends: nothing applies the snapshot"
	case current:
		if out.Held > 0 {
			return SnapshotPending, fmt.Sprintf("the operator is holding back (%d report(s)): cooldown, step limit or no fresh decision", out.Held)
		}
		return SnapshotPending, "current: nothing has used it yet"
	default:
		return SnapshotExpired, "replaced by a newer snapshot before anything used it"
	}
}

// ConfigSnapshotMax is the system parameter that caps how many snapshots of the history a module keeps.
const ConfigSnapshotMax = "forecast.snapshot_max_per_module"

// ConfigScoreMax is the system parameter that caps how many algorithm evaluations a module keeps.
const ConfigScoreMax = "forecast.score_max_per_module"

// ConfigHistoryDays is the system parameter that sets how many days the history is kept: predictions, trainings, backtests, evaluations,
// recommendations, the operator's reports, the admission records and the scale-up records. 0 keeps the built-in periods (30 days for the
// bulky ones, 180 for the summaries).
const ConfigHistoryDays = "forecast.history_retention_days"

// ConfigEvaluationMax is the system parameter that caps how many measured evaluations (the comparison of a finished forecast with the
// load that came) a module keeps.
const ConfigEvaluationMax = "forecast.evaluation_max_per_module"

// HistoryDays is the number of days the history is kept when the system parameter sets it, else fallback.
func (s *Service) HistoryDays(ctx context.Context, fallback int) int {
	if s.Config != nil {
		if v, err := s.Config.GetValueOrDefault(ctx, ConfigHistoryDays, ""); err == nil {
			if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 3650 {
				return n
			}
		}
	}
	return fallback
}

// RetentionPolicy is the retention in force: the defaults, with the snapshot cap of the system parameters. A parameter that
// is missing or not a number leaves the default.
func (s *Service) RetentionPolicy(ctx context.Context) rdb.Retention {
	r := rdb.DefaultRetention
	if s.Config != nil {
		if v, err := s.Config.GetValueOrDefault(ctx, ConfigSnapshotMax, ""); err == nil {
			if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 100000 {
				r.Snapshots = n
			}
		}
		if days := s.HistoryDays(ctx, 0); days > 0 {
			d := time.Duration(days) * 24 * time.Hour
			r.Predictions, r.Backtests, r.Recommendations, r.Evaluations, r.Trainings = d, d, d, d, d
		}
		if v, err := s.Config.GetValueOrDefault(ctx, ConfigEvaluationMax, ""); err == nil {
			if n, err := strconv.Atoi(v); err == nil && n >= 100 && n <= 10000000 {
				r.EvaluationsPerModule = n
			}
		}
		if v, err := s.Config.GetValueOrDefault(ctx, ConfigScoreMax, ""); err == nil {
			if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 100000 {
				r.Scores = n
			}
		}
	}
	return r
}
