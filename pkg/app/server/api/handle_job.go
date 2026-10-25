package api

import (
	"errors"
	"github.com/cylonchau/kube-resource-fcst/pkg/apis"
	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/job"
	"github.com/cylonchau/kube-resource-fcst/pkg/model"
	"github.com/gin-gonic/gin"
	"sort"
)

type JobRouter struct {
	jobDAO *rdb.JobDAO
}

func jobDTO(j *model.Job) *apis.JobDTO {
	if j == nil {
		return nil
	}
	return &apis.JobDTO{
		ID: j.ID, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt, JobType: j.JobType,
		TargetType: j.TargetType, TargetID: j.TargetID, Status: j.Status, RetryCount: j.RetryCount,
		MaxRetry: j.MaxRetry, RetryForever: j.RetryForever, RetryInitialSeconds: j.RetryInitialSeconds,
		RetryBackoffFactor: j.RetryBackoffFactor, RetryMaxSeconds: j.RetryMaxSeconds,
		ScheduledAt: j.ScheduledAt, StartedAt: j.StartedAt, FinishedAt: j.FinishedAt,
		IdempotencyKey: j.IdempotencyKey, ClaimedBy: j.ClaimedBy, ClaimExpiresAt: j.ClaimExpiresAt,
		Payload: j.Payload, Result: j.Result, TargetName: j.TargetName,
	}
}

func jobDTOs(jobs []model.Job) []*apis.JobDTO {
	dtos := make([]*apis.JobDTO, 0, len(jobs))
	for i := range jobs {
		dtos = append(dtos, jobDTO(&jobs[i]))
	}
	return dtos
}

func NewJobRouter(jobDAO *rdb.JobDAO) *JobRouter {
	return &JobRouter{jobDAO: jobDAO}
}

// List handles job log listing with filters
func (r *JobRouter) List(c *gin.Context) {
	var enconterError error
	req := &apis.JobLogReq{}
	if enconterError = c.ShouldBindQuery(req); enconterError != nil {
		errorMessage := query.GetErrorMsg(req, enconterError)
		query.BadRequest(c, errors.New(errorMessage))
		return
	}

	jobs, total, err := r.jobDAO.List(c.Request.Context(), req)
	if err != nil {
		query.InternalError(c, err)
		return
	}

	query.SuccessResponse(c, nil, map[string]interface{}{
		"list":  jobDTOs(jobs),
		"total": total,
	})
}

// Types lists the job types this deployment has: the ones with a registered task in this process and the ones
// already in the job table (a task registered in another process still leaves its jobs here).
func (r *JobRouter) Types(c *gin.Context) {
	seen := map[string]bool{}
	for jobType := range job.Registry {
		seen[jobType] = true
	}
	existing, err := r.jobDAO.JobTypes(c.Request.Context())
	if err != nil {
		query.InternalError(c, err)
		return
	}
	for _, jobType := range existing {
		seen[jobType] = true
	}
	types := make([]string, 0, len(seen))
	for jobType := range seen {
		types = append(types, jobType)
	}
	sort.Strings(types)
	query.SuccessResponse(c, nil, types)
}

// Reset handles manual job retry
// BatchReset puts several jobs back to pending. Jobs a worker already holds are skipped, and the response
// says how many were requested and how many moved.
func (r *JobRouter) BatchReset(c *gin.Context) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 {
		query.BadRequest(c, errors.New("ids are required"))
		return
	}
	reset, err := r.jobDAO.ResetMany(c.Request.Context(), req.IDs)
	if err != nil {
		query.HandleDBError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"requested": len(req.IDs), "reset": reset})
}

func (r *JobRouter) Reset(c *gin.Context) {
	var enconterError error
	req := &apis.JobRetryReq{}
	if enconterError = c.ShouldBindJSON(req); enconterError != nil {
		errorMessage := query.GetErrorMsg(req, enconterError)
		query.BadRequest(c, errors.New(errorMessage))
		return
	}

	if err := r.jobDAO.Reset(c.Request.Context(), req.ID); err != nil {
		query.HandleDBError(c, err)
		return
	}

	query.SuccessResponse(c, nil, "ok")
}
