package rdb

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func snapshotDB(t *testing.T) (*PredictionDAO, *gorm.DB) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Recommendation{}, &model.ScalingExecution{}, &model.AdmissionDecision{}))
	return NewPredictionDAO(db), db
}

func addRec(t *testing.T, dao *PredictionDAO, module uint, at time.Time, replicas int, snapshot bool) *model.Recommendation {
	rec := &model.Recommendation{ModuleID: module, Mode: "auto", ValidUntil: at.Add(time.Hour), ReplicasSet: true, Replicas: replicas, Snapshot: snapshot}
	require.NoError(t, dao.SaveRecommendation(context.Background(), rec))
	require.NoError(t, dao.db.Model(rec).Update("created_at", at).Error)
	return rec
}

func TestSnapshotsAreTheHistoryAndTheirSpanRunsToTheNextOne(t *testing.T) {
	dao, db := snapshotDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	var recs []*model.Recommendation
	for i := 0; i < 8; i++ {
		recs = append(recs, addRec(t, dao, 1, t0.Add(time.Duration(i)*5*time.Minute), 3+i/4, i == 0 || i == 4)) // snapshots: versions 1 and 5
	}
	addRec(t, dao, 2, t0, 9, true)

	at, err := dao.LastSnapshotAt(ctx, 1)
	require.NoError(t, err)
	require.NotNil(t, at)
	assert.True(t, at.Equal(t0.Add(20*time.Minute)), "the newest snapshot of the module, not the newest recommendation")
	none, err := dao.LastSnapshotAt(ctx, 99)
	require.NoError(t, err)
	assert.Nil(t, none)

	list, total, err := dao.SearchSnapshots(ctx, SnapshotFilter{ModuleID: 1, WithoutBody: true}, 1, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 2, total, "only the snapshots, not the repeats between them")
	assert.Equal(t, []int{5, 1}, []int{list[0].Version, list[1].Version}, "newest first")
	all, total, err := dao.SearchSnapshots(ctx, SnapshotFilter{}, 1, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 3, total)
	assert.Len(t, all, 3)

	next, err := dao.NextSnapshotVersion(ctx, 1, 1)
	require.NoError(t, err)
	assert.Equal(t, 5, next)
	next, err = dao.NextSnapshotVersion(ctx, 1, 5)
	require.NoError(t, err)
	assert.Zero(t, next, "the newest snapshot is still current")

	// what the operator and the admission reported for the versions of the span
	exec := func(version int, result string, reason string) {
		require.NoError(t, db.Create(&model.ScalingExecution{ModuleID: 1, Namespace: "n", Scaler: "s", TargetKind: "Deployment", TargetName: "w", Action: "scale", Result: result, Reason: reason, DecisionVersion: int64(version)}).Error)
	}
	exec(2, model.ExecHeld, "cooldown")
	exec(3, model.ExecApplied, "")
	exec(4, model.ExecError, "forbidden")
	exec(5, model.ExecObserved, "")
	require.NoError(t, db.Create(&model.AdmissionDecision{ModuleID: 1, Applied: true, Version: 2}).Error)
	require.NoError(t, db.Create(&model.AdmissionDecision{ModuleID: 1, Applied: false, Version: 2}).Error)

	first, err := dao.OutcomeOfSpan(ctx, 1, 1, 5)
	require.NoError(t, err)
	assert.EqualValues(t, 1, first.Applied)
	assert.EqualValues(t, 1, first.Held)
	assert.EqualValues(t, 1, first.Error)
	assert.EqualValues(t, 0, first.Observed, "version 5 belongs to the next span")
	assert.EqualValues(t, 1, first.Admitted, "only the answers that changed a Pod")
	assert.Equal(t, "forbidden", first.LastError)
	second, err := dao.OutcomeOfSpan(ctx, 1, 5, 0)
	require.NoError(t, err)
	assert.EqualValues(t, 1, second.Observed)
	assert.EqualValues(t, 0, second.Applied)

	execs, err := dao.SnapshotExecutions(ctx, 1, 1, 5, 10)
	require.NoError(t, err)
	assert.Len(t, execs, 3)

	got, err := dao.SnapshotByID(ctx, recs[4].ID)
	require.NoError(t, err)
	assert.Equal(t, 5, got.Version)
	_, err = dao.SnapshotByID(ctx, recs[1].ID)
	assert.Error(t, err, "a repeat is not a snapshot")
}

func TestRetentionKeepsTheNewestSnapshotsAndTrimsTheRepeats(t *testing.T) {
	dao, db := snapshotDB(t)
	ctx := context.Background()
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	// 30 recommendations a few minutes apart, every fifth a snapshot: 6 snapshots (versions 1, 6, 11, 16, 21, 26)
	for i := 0; i < 30; i++ {
		addRec(t, dao, 1, now.Add(-time.Duration(30-i)*5*time.Minute), 3, i%5 == 0)
	}
	removed, err := dao.Prune(ctx, now, Retention{Recommendations: 365 * 24 * time.Hour, RecommendationVersions: 10, Snapshots: 3})
	require.NoError(t, err)
	assert.NotZero(t, removed["recommendations"])

	var versions []int
	require.NoError(t, db.Model(&model.Recommendation{}).Where("module_id = 1").Order("version").Pluck("version", &versions).Error)
	var snaps []int
	require.NoError(t, db.Model(&model.Recommendation{}).Where("module_id = 1 AND snapshot = ?", true).Order("version").Pluck("version", &snaps).Error)
	assert.Equal(t, []int{16, 21, 26}, snaps, "the newest three snapshots stay, the older ones go")
	assert.Contains(t, versions, 30, "the current recommendation is never removed")
	for v := 21; v <= 30; v++ {
		assert.Contains(t, versions, v, "the newest ten recommendations stay whatever they are: %d", v)
	}
	for v := 11; v < 16; v++ {
		assert.NotContains(t, versions, v, "a repeat older than the recent ten goes: %d", v)
	}
	assert.EqualValues(t, 3, removed["snapshots"])
}

func TestTheHistoryIsBackfilledOnceForAnInstallationFromBeforeSnapshots(t *testing.T) {
	dao, db := snapshotDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	for i, replicas := range []int{2, 2, 2, 4, 4, 4, 4, 3} {
		addRec(t, dao, 1, t0.Add(time.Duration(i)*5*time.Minute), replicas, false)
	}
	marked, err := dao.BackfillSnapshots(ctx)
	require.NoError(t, err)
	var snaps []int
	require.NoError(t, db.Model(&model.Recommendation{}).Where("snapshot = ?", true).Order("version").Pluck("version", &snaps).Error)
	assert.Equal(t, []int{1, 4, 8}, snaps, "the first, the change to 4 and the current one")
	assert.EqualValues(t, 3, marked)
	again, err := dao.BackfillSnapshots(ctx)
	require.NoError(t, err)
	assert.Zero(t, again, "repeating changes nothing")
}
