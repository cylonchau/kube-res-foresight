package rdb

import (
	"context"
	"fmt"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAScheduledTaskThatIsStillOpenIsNotQueuedAgainAndPiledUpRunsAreClosed(t *testing.T) {
	db := integrationDB(t, &model.Job{})
	dao := NewJobDAO(db)
	ctx := context.Background()
	open, err := dao.HasOpenJob(ctx, "signal.composite", "task_definition", 1)
	require.NoError(t, err)
	assert.False(t, open)
	for i := 0; i < 5; i++ {
		require.NoError(t, db.Create(&model.Job{JobType: "signal.composite", TargetType: "task_definition", TargetID: 1, Status: "pending", IdempotencyKey: fmt.Sprintf("task-1-%d", i)}).Error)
	}
	require.NoError(t, db.Create(&model.Job{JobType: "other", TargetType: "task_definition", TargetID: 2, Status: "pending", IdempotencyKey: "task-2-1"}).Error)
	require.NoError(t, db.Create(&model.Job{JobType: "notify", TargetType: "notify_channel", TargetID: 3, Status: "pending", IdempotencyKey: "n-1"}).Error)
	require.NoError(t, db.Create(&model.Job{JobType: "notify", TargetType: "notify_channel", TargetID: 3, Status: "pending", IdempotencyKey: "n-2"}).Error)
	open, _ = dao.HasOpenJob(ctx, "signal.composite", "task_definition", 1)
	assert.True(t, open)

	closed, err := dao.SupersedeStaleScheduled(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 4, closed, "of the five waiting runs of one task only the newest is kept")
	var pending int64
	db.Model(&model.Job{}).Where("job_type = ? AND status = ?", "signal.composite", "pending").Count(&pending)
	assert.EqualValues(t, 1, pending)
	db.Model(&model.Job{}).Where("job_type = ? AND status = ?", "notify", "pending").Count(&pending)
	assert.EqualValues(t, 2, pending, "jobs that are not scheduled task runs are not touched")
	db.Model(&model.Job{}).Where("job_type = ? AND status = ?", "other", "pending").Count(&pending)
	assert.EqualValues(t, 1, pending)
}
