package job

import (
	"context"
	"errors"
	"fmt"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"testing"
	"time"
)

type schedulerTask struct {
	kind string
	err  error
}

func (t schedulerTask) Type() string                              { return t.kind }
func (t schedulerTask) Execute(context.Context, *model.Job) error { return t.err }

func schedulerFixture(t *testing.T) (*Scheduler, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Job{}))
	return NewScheduler(rdb.NewJobDAO(db), 2, time.Millisecond), db
}

func claimedJob(t *testing.T, db *gorm.DB, worker, kind string, retry, max int) *model.Job {
	t.Helper()
	expiry := time.Now().Add(time.Minute)
	job := &model.Job{JobType: kind, Status: "pending", ClaimedBy: worker, ClaimExpiresAt: &expiry, RetryCount: retry, MaxRetry: max, IdempotencyKey: fmt.Sprintf("%s-%d", kind, time.Now().UnixNano())}
	require.NoError(t, db.Create(job).Error)
	return job
}

func TestSchedulerExecutesSuccessRetryFailureAndUnknown(t *testing.T) {
	s, db := schedulerFixture(t)
	previous := Registry
	Registry = map[string]Task{}
	t.Cleanup(func() { Registry = previous })
	Register(schedulerTask{kind: "success"})
	Register(schedulerTask{kind: "retry", err: errors.New("temporary")})
	Register(schedulerTask{kind: "fail", err: errors.New("permanent")})

	for _, tc := range []struct {
		kind       string
		retry, max int
		want       string
	}{{"success", 0, 3, "success"}, {"retry", 0, 3, "pending"}, {"fail", 0, 1, "failed"}, {"missing", 0, 1, "failed"}} {
		job := claimedJob(t, db, s.workerID, tc.kind, tc.retry, tc.max)
		s.execute(context.Background(), job)
		var got model.Job
		require.NoError(t, db.First(&got, job.ID).Error)
		assert.Equal(t, tc.want, got.Status, tc.kind)
	}
	assert.NotEmpty(t, jobLogFields(&model.Job{ID: 1, JobType: "x"}))
}

func TestSchedulerPollAndStartRespectCapacityAndCancellation(t *testing.T) {
	s, db := schedulerFixture(t)
	previous := Registry
	Registry = map[string]Task{"success": schedulerTask{kind: "success"}}
	t.Cleanup(func() { Registry = previous })
	job := &model.Job{JobType: "success", Status: "pending", IdempotencyKey: "poll-success", MaxRetry: 1}
	require.NoError(t, db.Create(job).Error)
	s.poll(context.Background())
	require.Eventually(t, func() bool { var got model.Job; return db.First(&got, job.ID).Error == nil && got.Status == "success" }, time.Second, 10*time.Millisecond)
	s.workerPool <- struct{}{}
	s.workerPool <- struct{}{}
	s.poll(context.Background())
	<-s.workerPool
	<-s.workerPool

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Start(ctx); close(done) }()
	time.Sleep(3 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
	heartbeatCtx, stop := context.WithCancel(context.Background())
	stop()
	s.heartbeat(heartbeatCtx, job.ID)
	recoveryCtx, stopRecovery := context.WithCancel(context.Background())
	stopRecovery()
	s.runRecovery(recoveryCtx)
}
