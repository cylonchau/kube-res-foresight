package rdb

import (
	"context"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"github.com/cylonchau/kubeforecast/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestWorkflowDAOsLifecycleAndConcurrency(t *testing.T) {
	db := integrationDB(t, &model.BotCommandDef{}, &model.WorkflowDef{}, &model.WorkflowInstance{})
	ctx := context.Background()
	commands, defs, instances := NewBotCommandDAO(db), NewWorkflowDefDAO(db), NewWorkflowInstanceDAO(db)

	def := &model.WorkflowDef{WorkflowKey: "approval", Name: "Approval", InitialState: "pending", PipelineJSON: `{}`, Enabled: true}
	require.NoError(t, defs.Create(ctx, def))
	gotDef, err := defs.GetByKey(ctx, "approval")
	require.NoError(t, err)
	assert.Equal(t, def.ID, gotDef.ID)
	allDefs, err := defs.ListAll(ctx)
	require.NoError(t, err)
	assert.Len(t, allDefs, 1)
	pageDefs, total, err := defs.ListPaginated(ctx, 0, 0, "proval")
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Len(t, pageDefs, 1)
	affected, err := defs.Update(ctx, "approval", map[string]interface{}{"name": "Updated"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, affected)
	affected, err = defs.Update(ctx, "missing", map[string]interface{}{"name": "Nope"})
	require.NoError(t, err)
	assert.Zero(t, affected)

	cmd := &model.BotCommandDef{Command: "/approve", Name: "Approve", WorkflowKey: "approval", Enabled: true}
	require.NoError(t, commands.Create(ctx, cmd))
	_, err = commands.GetByCommand(ctx, "/approve")
	require.NoError(t, err)
	_, err = commands.GetByID(ctx, cmd.ID)
	require.NoError(t, err)
	allCommands, err := commands.ListAll(ctx)
	require.NoError(t, err)
	assert.Len(t, allCommands, 1)
	pageCommands, total, err := commands.ListPaginated(ctx, 1, 10, "approve")
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Len(t, pageCommands, 1)
	require.NoError(t, commands.Update(ctx, cmd.ID, map[string]interface{}{"name": "Updated"}))
	assert.Error(t, commands.Update(ctx, 999, map[string]interface{}{"name": "Missing"}))

	inst := &model.WorkflowInstance{WorkflowKey: "approval", CurrentState: "pending", Status: "running", BusinessType: "request", BusinessID: 7, RequesterName: "alice", ContextDataJSON: `{}`, HistoryJSON: `[]`}
	require.NoError(t, instances.Create(ctx, inst))
	_, err = instances.GetByID(ctx, inst.ID)
	require.NoError(t, err)
	_, err = instances.GetByBusiness(ctx, "request", 7)
	require.NoError(t, err)
	byKey, err := instances.ListByWorkflowKey(ctx, "approval")
	require.NoError(t, err)
	assert.Len(t, byKey, 1)
	pageInstances, total, err := instances.ListPaginated(ctx, 1, 10, "approval", "running", "ali")
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Len(t, pageInstances, 1)
	require.NoError(t, instances.UpdateState(ctx, inst.ID, "pending", "approved", "completed", `[]`, `{"ok":"1"}`, nil))
	assert.ErrorIs(t, instances.UpdateState(ctx, inst.ID, "pending", "rejected", "rejected", `[]`, `{}`, nil), ErrInstanceStateChanged)

	extra := &model.WorkflowInstance{WorkflowKey: "approval", CurrentState: "pending", Status: "running", BusinessType: "request", BusinessID: 8}
	require.NoError(t, instances.Create(ctx, extra))
	deleted, err := instances.BatchDelete(ctx, []int64{extra.ID})
	require.NoError(t, err)
	assert.EqualValues(t, 1, deleted)
	_, err = instances.BatchDelete(ctx, []int64{999})
	assert.Error(t, err)
	require.NoError(t, instances.Delete(ctx, inst.ID))
	assert.Error(t, instances.Delete(ctx, inst.ID))
	require.NoError(t, commands.BatchDelete(ctx, []int64{cmd.ID}))
	assert.NoError(t, commands.BatchDelete(ctx, nil))
	require.NoError(t, defs.BatchDelete(ctx, []string{"approval"}))
	assert.NoError(t, defs.BatchDelete(ctx, nil))
	assert.Error(t, defs.Delete(ctx, "approval"))
}

func TestJobDAOExecutionLifecycle(t *testing.T) {
	db := integrationDB(t, &model.Job{})
	dao, ctx := NewJobDAO(db), context.Background()
	job := &model.Job{JobType: "notify", TargetType: "channel", TargetID: 1, TargetName: "alerts", Status: "pending", IdempotencyKey: "notify-1", MaxRetry: 3, Payload: `{}`}
	created, err := dao.Create(ctx, job)
	require.NoError(t, err)
	assert.NotZero(t, created.ID)
	require.NoError(t, dao.CreateIdempotent(ctx, &model.Job{JobType: "notify", Status: "pending", IdempotencyKey: "notify-1"}))
	claimed, err := dao.Claim(ctx, "worker-1", 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, dao.MarkRunning(ctx, job.ID, "worker-1"))
	assert.ErrorIs(t, dao.MarkSuccess(ctx, job.ID, "other", "no"), ErrJobNotOwned)
	require.NoError(t, dao.ExtendClaim(ctx, job.ID, "worker-1"))
	require.NoError(t, dao.ScheduleRetry(ctx, job.ID, "worker-1", 1, time.Now().Add(time.Minute), "temporary"))

	claimed, err = dao.Claim(ctx, "worker-2", 10)
	require.NoError(t, err)
	assert.Empty(t, claimed)
	require.NoError(t, db.Model(&model.Job{}).Where("id = ?", job.ID).Updates(map[string]interface{}{"scheduled_at": time.Now().Add(-time.Minute)}).Error)
	claimed, err = dao.Claim(ctx, "worker-2", 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, dao.MarkRunning(ctx, job.ID, "worker-2"))
	require.NoError(t, dao.MarkSuccess(ctx, job.ID, "worker-2", "ok"))

	listed, total, err := dao.List(ctx, &apis.JobLogReq{ListOptions: apis.ListOptions{Page: 1, PageSize: 10, Keyword: "ok"}, JobType: "notify", TargetType: "channel", TargetID: 1, TargetKeyword: "alert", Status: "success"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)
	assert.Len(t, listed, 1)
	_, err = dao.GetByID(ctx, job.ID)
	require.NoError(t, err)
	require.NoError(t, dao.Reset(ctx, job.ID))

	staleExpiry := time.Now().Add(-time.Minute)
	require.NoError(t, db.Model(&model.Job{}).Where("id = ?", job.ID).Updates(map[string]interface{}{"claimed_by": "dead", "claim_expires_at": staleExpiry, "status": "running"}).Error)
	recovered, err := dao.RecoverStaleJobs(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, recovered)
	require.NoError(t, dao.MarkObsoleteByTargets(ctx, "notify", "channel", []int64{1}, "obsolete:test"))
	require.NoError(t, dao.MarkObsoleteByTargets(ctx, "notify", "channel", nil, "none"))

	manual, err := dao.UpsertManual(ctx, &model.Job{JobType: "notify", TargetType: "channel", TargetID: 2, Status: "pending", IdempotencyKey: "manual-2", MaxRetry: 2})
	require.NoError(t, err)
	_, err = dao.UpsertManual(ctx, &model.Job{JobType: "notify", TargetType: "channel", TargetID: 2, Status: "pending", IdempotencyKey: "manual-2", Payload: "new"})
	require.NoError(t, err)
	_, err = dao.UpsertCoalesced(ctx, &model.Job{JobType: "notify", TargetType: "channel", TargetID: 2, Status: "pending", IdempotencyKey: "coalesced-2", Payload: "coalesced"})
	require.NoError(t, err)
	require.NoError(t, dao.UpsertRecurring(ctx, &model.Job{JobType: "notify", Status: "pending", IdempotencyKey: "recurring"}))
	require.NoError(t, dao.UpsertRecurringCoalesced(ctx, &model.Job{JobType: "notify", TargetType: "channel", TargetID: 3, Status: "pending", IdempotencyKey: "recurring-3"}))
	assert.NotZero(t, manual.ID)
}
