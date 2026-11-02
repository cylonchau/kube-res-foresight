package forecastsvc

import (
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/stretchr/testify/assert"
)

func TestARecommendationIsASnapshotWhenItsOutcomeDiffersOrAnHourHasPassed(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	base := &model.Recommendation{Mode: "auto", ReplicasSet: true, Replicas: 4, CPUSet: true, CPUMilli: 500, MemorySet: true, MemoryBytes: 512 << 20,
		CPUSource: "algorithm", MemorySource: "algorithm", ReplicasSource: "algorithm", Algorithm: "naive", ResourceVersion: 1, EffectiveMaxReplicas: 10}
	same := *base
	recent := now.Add(-10 * time.Minute)

	keep, why := snapshotWorthKeeping(&same, nil, nil, now)
	assert.True(t, keep)
	assert.Contains(t, why, "first")

	keep, _ = snapshotWorthKeeping(&same, base, &recent, now)
	assert.False(t, keep, "the same answer again inside the hour is not history")

	for name, mutate := range map[string]func(r *model.Recommendation){
		"replicas":  func(r *model.Recommendation) { r.Replicas = 5 },
		"spec":      func(r *model.Recommendation) { r.ResourceVersion = 2; r.CPUMilli = 700 },
		"cpu":       func(r *model.Recommendation) { r.CPUMilli = 600 },
		"algorithm": func(r *model.Recommendation) { r.Algorithm = "ewma" },
		"source":    func(r *model.Recommendation) { r.ReplicasSource = "fixed_period" },
		"degraded":  func(r *model.Recommendation) { r.Degraded = true },
		"oom":       func(r *model.Recommendation) { r.OOMFloor = true },
		"ceiling":   func(r *model.Recommendation) { r.EffectiveMaxReplicas = 12 },
		"mode":      func(r *model.Recommendation) { r.Mode = "admission" },
	} {
		changed := *base
		mutate(&changed)
		keep, why = snapshotWorthKeeping(&changed, base, &recent, now)
		assert.True(t, keep, name)
		assert.NotEmpty(t, why, name)
	}

	old := now.Add(-61 * time.Minute)
	keep, why = snapshotWorthKeeping(&same, base, &old, now)
	assert.True(t, keep)
	assert.Contains(t, why, "periodic")
	keep, _ = snapshotWorthKeeping(&same, base, nil, now)
	assert.True(t, keep, "no snapshot yet")
}

func TestSnapshotStateFollowsWhatTheOperatorAndAdmissionReported(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	rec := func(mode string, validFor time.Duration) *model.Recommendation {
		return &model.Recommendation{Mode: mode, ValidUntil: now.Add(validFor)}
	}
	for _, tc := range []struct {
		name    string
		rec     *model.Recommendation
		out     rdb.SnapshotOutcome
		current bool
		want    string
	}{
		{"the operator scaled", rec("auto", time.Hour), rdb.SnapshotOutcome{Applied: 1}, true, SnapshotApplied},
		{"the replicas were already right", rec("auto", time.Hour), rdb.SnapshotOutcome{Observed: 3}, false, SnapshotApplied},
		{"an admission carried the resources", rec("admission", time.Hour), rdb.SnapshotOutcome{Admitted: 2}, false, SnapshotApplied},
		{"an error and nothing applied", rec("auto", time.Hour), rdb.SnapshotOutcome{Error: 2, LastError: "forbidden"}, true, SnapshotFailed},
		{"a conflict and nothing applied", rec("auto", time.Hour), rdb.SnapshotOutcome{Conflict: 1}, false, SnapshotFailed},
		{"an error after it was applied is still applied", rec("auto", time.Hour), rdb.SnapshotOutcome{Applied: 1, Error: 1}, true, SnapshotApplied},
		{"current and unused", rec("auto", time.Hour), rdb.SnapshotOutcome{}, true, SnapshotPending},
		{"the operator is holding back", rec("auto", time.Hour), rdb.SnapshotOutcome{Held: 2}, true, SnapshotPending},
		{"current, valid, in recommend mode", rec("recommend", time.Hour), rdb.SnapshotOutcome{}, true, SnapshotAdvisory},
		{"current but past its validity", rec("auto", -time.Minute), rdb.SnapshotOutcome{}, true, SnapshotExpired},
		{"replaced before anything used it", rec("auto", time.Hour), rdb.SnapshotOutcome{}, false, SnapshotExpired},
		{"a recommend snapshot that was replaced", rec("recommend", time.Hour), rdb.SnapshotOutcome{}, false, SnapshotExpired},
	} {
		state, reason := SnapshotState(tc.rec, tc.out, tc.current, now)
		assert.Equal(t, tc.want, state, tc.name)
		assert.NotEmpty(t, reason, tc.name)
	}
}
