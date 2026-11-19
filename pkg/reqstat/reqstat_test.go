package reqstat

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var seq int32

func testService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:reqstat%d?mode=memory&cache=shared", atomic.AddInt32(&seq, 1))), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.RequestStat{}, &model.ProcessBoot{}))
	svc := New(rdb.NewRequestStatDAO(db), rdb.NewProcessBootDAO(db), NewRecorder(), "test")
	svc.Instance = "pod-a"
	return svc, db
}

func obs(route string, took time.Duration, status int) Observation {
	return Observation{Method: "GET", Route: route, Status: status, Took: took}
}

func TestThePercentileIsTheUpperEdgeOfTheBucketTheRankFallsIn(t *testing.T) {
	rec := NewRecorder()
	for i := 0; i < 90; i++ {
		rec.Observe(obs("/a", 40*time.Millisecond, 200))
	}
	for i := 0; i < 9; i++ {
		rec.Observe(obs("/a", 4*time.Second, 200))
	}
	rec.Observe(obs("/a", 100*time.Second, 200))
	hours := rec.TakeChanged()
	require.Len(t, hours, 1)
	h := hours[0]
	assert.EqualValues(t, 100, h.Count)
	assert.EqualValues(t, 50, h.Percentile(0.50), "40 ms falls in the bucket that ends at 50")
	assert.EqualValues(t, 5000, h.Percentile(0.95), "the 95th request took 4 s: the bucket that ends at 5 s")
	assert.EqualValues(t, 120000, h.Percentile(0.999), "the slowest took 100 s: the bucket that ends at 120 s")
	assert.EqualValues(t, 100000, h.MaxMs)
	assert.Empty(t, rec.TakeChanged(), "what was taken is not taken twice")
}

func TestTheRecorderKeepsTheSlowestRequestsOfAnHourAsExamplesToLookForInTheLogs(t *testing.T) {
	rec := NewRecorder()
	for i, ms := range []int{100, 9000, 300, 7000, 8000} {
		o := obs("/a", time.Duration(ms)*time.Millisecond, 200)
		o.RequestID = fmt.Sprintf("op-%d", i)
		rec.Observe(o)
	}
	h := rec.TakeChanged()[0]
	require.Len(t, h.Examples, MaxExamples)
	assert.Equal(t, "op-1", h.Examples[0].RequestID)
	assert.Equal(t, "op-4", h.Examples[1].RequestID)
	assert.Equal(t, "op-3", h.Examples[2].RequestID)
}

func TestTheTimeOfARouteIsAttributedToTheLayerThatHoldsMostOfIt(t *testing.T) {
	assert.Equal(t, LayerDatabase, layerOf(0.8, 0.05))
	assert.Equal(t, LayerPrometheus, layerOf(0.1, 0.7))
	assert.Equal(t, LayerHandler, layerOf(0.1, 0.1), "neither holds as much as the platform's own work")
	assert.Equal(t, LayerDatabase, layerOf(0.5, 0.1), "half of the time in the database is more than the rest")
	assert.Equal(t, LayerHandler, layerOf(0.3, 0.1), "30% in the database against 60% of the platform")
}

func TestARouteThatTakesHalfTheGatewayTimeIsNamedWithItsLayerAndItsFigures(t *testing.T) {
	svc, _ := testService(t)
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		o := obs("/api/v1/modules/:id/compare", 40*time.Second, 200)
		o.DBMs, o.DBCalls = 36000, 4
		o.RequestID = fmt.Sprintf("op-%d", i)
		svc.Recorder.Observe(o)
		svc.Recorder.Observe(obs("/api/v1/modules", 30*time.Millisecond, 200))
	}
	rep, err := svc.Read(ctx, 24, Limits{Gateway: 60 * time.Second, ListBudget: 50 * time.Second})
	require.NoError(t, err)
	require.Len(t, rep.Routes, 2)
	assert.Equal(t, "/api/v1/modules/:id/compare", rep.Routes[0].Route, "the slowest route is first")
	assert.Equal(t, LayerDatabase, rep.Routes[0].Layer)
	assert.InDelta(t, 0.9, rep.Routes[0].DBShare, 0.001)
	assert.EqualValues(t, 45000, rep.Routes[0].P95)
	var found *Finding
	for i := range rep.Findings {
		if rep.Findings[i].Code == FindingHalfGateway {
			found = &rep.Findings[i]
		}
	}
	require.NotNil(t, found, "%+v", rep.Findings)
	assert.Equal(t, "critical", found.Level)
	assert.Equal(t, "GET /api/v1/modules/:id/compare", found.Route)
	assert.Equal(t, LayerDatabase, found.Layer)
	assert.Equal(t, 90, found.Params["db_share"])
	assert.Equal(t, 60, found.Params["gateway_s"])
	for _, f := range rep.Findings {
		assert.NotEqual(t, "GET /api/v1/modules", f.Route, "a fast route has no finding")
	}
}

func TestStopsAtThePlatformsTimeLimitAndRequestsTheOtherSideGaveUpOnAreToldApart(t *testing.T) {
	routes := Summarize(rowsOf(t, func(rec *Recorder) {
		for i := 0; i < 10; i++ {
			o := obs("/api/v1/recommendations", 50*time.Second, 504)
			o.PlatformTimeout, o.DBMs = true, 49000
			rec.Observe(o)
		}
		for i := 0; i < 6; i++ {
			rec.Observe(obs("/api/v1/recommendations", 100*time.Millisecond, 200))
		}
		o := obs("/api/v1/scaling-decisions", 60*time.Second, 200)
		o.Abandoned, o.RequestID = true, "op-gone"
		rec.Observe(o)
		for i := 0; i < 6; i++ {
			rec.Observe(obs("/api/v1/scaling-decisions", 100*time.Millisecond, 200))
		}
	}))
	findings := Diagnose(routes, nil, Limits{Gateway: 60 * time.Second, ListBudget: 50 * time.Second})
	codes := map[string]Finding{}
	for _, f := range findings {
		codes[f.Code+"|"+f.Route] = f
	}
	stop, ok := codes[FindingPlatformStop+"|GET /api/v1/recommendations"]
	require.True(t, ok, "%+v", findings)
	assert.Equal(t, 10, int(stop.Params["count"].(int64)))
	assert.Equal(t, 50, stop.Params["budget_s"])
	assert.Equal(t, LayerDatabase, stop.Layer)
	gone, ok := codes[FindingAbandoned60+"|GET /api/v1/scaling-decisions"]
	require.True(t, ok, "a request given up on at 60 s is the browser's limit or the gateway's default: %+v", findings)
	assert.EqualValues(t, 60000, gone.Params["took_ms"])
	assert.Equal(t, "critical", findings[0].Level, "the serious ones come first")
}

func rowsOf(t *testing.T, fill func(*Recorder)) []model.RequestStat {
	t.Helper()
	rec := NewRecorder()
	fill(rec)
	var rows []model.RequestStat
	for _, h := range rec.TakeChanged() {
		rows = append(rows, toRow("boot", h))
	}
	return rows
}

func TestARestartWithoutAStopIsAKillAndOneWithAStopIsAShutdown(t *testing.T) {
	now := time.Now()
	boots := []model.ProcessBoot{
		{Instance: "pod-a", Unexpected: true, LastSeen: now.Add(-time.Hour), SysBytes: 940 << 20, MemLimit: 1024 << 20},
		{Instance: "pod-b", Unexpected: true, LastSeen: now.Add(-2 * time.Hour), SysBytes: 200 << 20, MemLimit: 1024 << 20},
		{Instance: "pod-c", Graceful: true, LastSeen: now},
	}
	findings := Diagnose(nil, boots, Limits{Gateway: time.Minute})
	byCode := map[string][]Finding{}
	for _, f := range findings {
		byCode[f.Code] = append(byCode[f.Code], f)
	}
	require.Len(t, byCode[FindingKilled], 1)
	assert.Equal(t, 2, byCode[FindingKilled][0].Params["count"])
	require.Len(t, byCode[FindingMemoryNear], 1, "only the run that was at 92% of its limit when last seen")
	assert.Equal(t, "pod-a", byCode[FindingMemoryNear][0].Params["instance"])
	require.Len(t, byCode[FindingGraceful], 1)
	assert.Equal(t, "info", byCode[FindingGraceful][0].Level)
}

func TestWithTooFewRequestsTheDiagnosisSaysSoInsteadOfAnConclusion(t *testing.T) {
	findings := Diagnose(Summarize(rowsOf(t, func(rec *Recorder) { rec.Observe(obs("/a", time.Second, 200)) })), nil, Limits{Gateway: time.Minute})
	require.Len(t, findings, 1)
	assert.Equal(t, FindingNoData, findings[0].Code)
}

func TestTheHoursAreStoredAgainWithNewFiguresNotAddedTwiceAndTwoBootsDoNotOverwriteEachOther(t *testing.T) {
	svc, db := testService(t)
	ctx := context.Background()
	svc.Recorder.Observe(obs("/a", 10*time.Millisecond, 200))
	svc.Flush(ctx)
	svc.Recorder.Observe(obs("/a", 10*time.Millisecond, 200))
	svc.Flush(ctx)
	var rows []model.RequestStat
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 1, "the same boot and hour is one row")
	assert.EqualValues(t, 2, rows[0].Count, "with the figures of now")

	other := New(rdb.NewRequestStatDAO(db), rdb.NewProcessBootDAO(db), NewRecorder(), "test")
	other.Recorder.Observe(obs("/a", 10*time.Millisecond, 200))
	other.Flush(ctx)
	rep, err := svc.Read(ctx, 24, Limits{Gateway: time.Minute})
	require.NoError(t, err)
	require.Len(t, rep.Routes, 1)
	assert.EqualValues(t, 3, rep.Routes[0].Count, "the two runs are added up in the baseline")
}

func TestARunThatWroteNoStopIsFoundGoneWhenTheSameInstanceStartsAgainOrWhenItIsNotSeenForLong(t *testing.T) {
	svc, db := testService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	svc.Now = func() time.Time { return now }
	require.NoError(t, svc.Boots.Start(ctx, &model.ProcessBoot{BootID: "first", Instance: "pod-a", StartedAt: now.Add(-time.Hour), LastSeen: now.Add(-50 * time.Minute)}))
	require.NoError(t, svc.Boots.Start(ctx, &model.ProcessBoot{BootID: "elsewhere", Instance: "pod-z", StartedAt: now.Add(-time.Hour), LastSeen: now.Add(-30 * time.Minute)}))
	svc.Start(ctx)

	var first, elsewhere, mine model.ProcessBoot
	require.NoError(t, db.Where("boot_id = ?", "first").First(&first).Error)
	assert.True(t, first.Unexpected, "the container of pod-a started again though the run never wrote its stop")
	require.NotNil(t, first.StoppedAt)
	assert.WithinDuration(t, now.Add(-50*time.Minute), *first.StoppedAt, time.Second, "it ended when it was last seen")

	svc.Beat(ctx)
	require.NoError(t, db.Where("boot_id = ?", "elsewhere").First(&elsewhere).Error)
	assert.True(t, elsewhere.Unexpected, "a Pod that is replaced writes no stop and is not seen again")
	require.NoError(t, db.Where("boot_id = ?", svc.BootID).First(&mine).Error)
	assert.False(t, mine.Unexpected)
	assert.Nil(t, mine.StoppedAt)

	svc.Stop()
	svc.Stop() // twice is once
	require.NoError(t, db.Where("boot_id = ?", svc.BootID).First(&mine).Error)
	assert.True(t, mine.Graceful)
	require.NotNil(t, mine.StoppedAt)

	rep, err := svc.Read(ctx, 24, Limits{Gateway: time.Minute})
	require.NoError(t, err)
	codes := map[string]bool{}
	for _, f := range rep.Findings {
		codes[f.Code] = true
	}
	assert.True(t, codes[FindingKilled])
	assert.True(t, codes[FindingGraceful])
}

func TestTheSpanOfARequestCollectsTheTimeOfTheLayersFromAnyGoroutine(t *testing.T) {
	ctx, span := WithSpan(context.Background())
	done := make(chan struct{})
	go func() {
		AddDB(ctx, 30*time.Millisecond)
		AddDB(ctx, 20*time.Millisecond)
		AddProm(ctx, 200*time.Millisecond)
		close(done)
	}()
	<-done
	dbMs, dbCalls, promMs, promCalls := span.Totals()
	assert.EqualValues(t, 50, dbMs)
	assert.EqualValues(t, 2, dbCalls)
	assert.EqualValues(t, 200, promMs)
	assert.EqualValues(t, 1, promCalls)
	AddDB(context.Background(), time.Second) // work with no request is not an error
	stop := TimeProm(context.Background())
	stop()
}
