package api

import (
	"errors"
	"fmt"
	"github.com/cylonchau/kube-resource-fcst/pkg/apis"
	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/job"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/gin-gonic/gin"
)

type TaskRouter struct {
	DAO *rdb.TaskDefinitionDAO
}

func taskDefinitionDTO(t *model.TaskDefinition) *apis.TaskDefinitionDTO {
	if t == nil {
		return nil
	}
	return &apis.TaskDefinitionDTO{
		ID:                       t.ID,
		CreatedAt:                t.CreatedAt,
		UpdatedAt:                t.UpdatedAt,
		TaskType:                 t.TaskType,
		Description:              t.Description,
		CronExpr:                 t.CronExpr,
		Enabled:                  t.Enabled,
		Params:                   t.Params,
		MaxRetry:                 t.MaxRetry,
		RetryForever:             t.RetryForever,
		RetryInitialSeconds:      t.RetryInitialSeconds,
		RetryBackoffFactor:       t.RetryBackoffFactor,
		RetryMaxSeconds:          t.RetryMaxSeconds,
		ExecutionIntervalSeconds: t.ExecutionIntervalSeconds,
		WatchIntervalSeconds:     t.WatchIntervalSeconds,
		LastRunDisplay:           t.LastRunDisplay,
		NextRunDisplay:           t.NextRunDisplay,
	}
}

func taskDefinitionDTOs(tasks []model.TaskDefinition) []*apis.TaskDefinitionDTO {
	dtos := make([]*apis.TaskDefinitionDTO, 0, len(tasks))
	for i := range tasks {
		dtos = append(dtos, taskDefinitionDTO(&tasks[i]))
	}
	return dtos
}

// List gets a list of task definitions
func (tr *TaskRouter) List(c *gin.Context) {
	var enconterError error
	taskListReq := &apis.TaskListReq{}
	if enconterError = c.ShouldBindQuery(taskListReq); enconterError != nil {
		errorMessage := query.GetErrorMsg(taskListReq, enconterError)
		query.BadRequest(c, errors.New(errorMessage))
		return
	}

	tasks, total, err := tr.DAO.List(c.Request.Context(), &taskListReq.ListOptions)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, map[string]interface{}{
		"list":  taskDefinitionDTOs(tasks),
		"total": total,
	})
}

// Update updates a task definition
func (tr *TaskRouter) Update(c *gin.Context) {
	var enconterError error
	taskQueryByID := &apis.QueryByID{}
	if enconterError = c.ShouldBindUri(taskQueryByID); enconterError != nil {
		errorMessage := query.GetErrorMsg(taskQueryByID, enconterError)
		query.BadRequest(c, errors.New(errorMessage))
		return
	}

	taskUpdateReq := &apis.TaskUpdateReq{}
	if enconterError = c.ShouldBindJSON(taskUpdateReq); enconterError != nil {
		errorMessage := query.GetErrorMsg(taskUpdateReq, enconterError)
		query.BadRequest(c, errors.New(errorMessage))
		return
	}
	if taskUpdateReq.CronExpr != nil {
		if _, err := job.ParseCron(*taskUpdateReq.CronExpr); err != nil {
			query.BadRequest(c, fmt.Errorf("invalid cron expression: %w", err))
			return
		}
	}
	if taskUpdateReq.RetryInitialSeconds != nil && taskUpdateReq.RetryMaxSeconds != nil && *taskUpdateReq.RetryInitialSeconds > *taskUpdateReq.RetryMaxSeconds {
		query.BadRequest(c, errors.New("initial retry delay cannot exceed maximum retry delay"))
		return
	}

	if err := tr.DAO.Update(c.Request.Context(), taskQueryByID.ID, taskUpdateReq); err != nil {
		query.HandleDBError(c, err)
		return
	}
	if taskUpdateReq.CronExpr != nil {
		if err := tr.DAO.ResetSchedule(c.Request.Context(), taskQueryByID.ID); err != nil {
			query.HandleDBError(c, err)
			return
		}
	}
	query.SuccessResponse(c, nil, "ok")
}
