package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/cylonchau/kube-resource-fcst/pkg/app/query"
	"github.com/cylonchau/kube-resource-fcst/pkg/dao/rdb"
	"github.com/cylonchau/kube-resource-fcst/pkg/forecast"
)

// batchFailure is the reason one item of a batch was left alone; the others are done.
type batchFailure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// batchResult is the outcome of a batch: what was done and what was refused with the reason, so a batch never fails as a
// whole because of one row.
type batchResult struct {
	Done   int            `json:"done"`
	Failed []batchFailure `json:"failed"`
}

const maxBatch = 500

func (b *batchResult) fail(id any, err error) {
	b.Failed = append(b.Failed, batchFailure{ID: fmt.Sprint(id), Error: err.Error()})
}

// BatchModules enables, disables or deletes several modules. A module has to be disabled to be deleted.
// @Summary Batch operation on modules
// @Router /api/v1/modules/batch [post]
func (r *ForecastRouter) BatchModules(c *gin.Context) {
	var req struct {
		Action string `json:"action" binding:"required"`
		IDs    []uint `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 || len(req.IDs) > maxBatch {
		query.BadRequest(c, errors.New("action and 1 to 500 ids are required"))
		return
	}
	if req.Action != "enable" && req.Action != "disable" && req.Action != "delete" {
		query.BadRequest(c, errors.New("action must be enable, disable or delete"))
		return
	}
	ctx := c.Request.Context()
	out := batchResult{Failed: []batchFailure{}}
	for _, id := range req.IDs {
		switch req.Action {
		case "delete":
			if err := r.Modules.Delete(ctx, id); err != nil {
				out.fail(id, err)
				continue
			}
		default:
			m, err := r.Modules.Get(ctx, id)
			if err != nil {
				out.fail(id, err)
				continue
			}
			m.Enabled = req.Action == "enable"
			if err := m.Validate(); err != nil {
				out.fail(id, err)
				continue
			}
			if err := r.Modules.Update(ctx, m); err != nil {
				out.fail(id, err)
				continue
			}
		}
		out.Done++
	}
	query.SuccessResponse(c, nil, out)
}

// BatchAlgorithms enables, disables or deletes several algorithms. Only an algorithm an administrator created (a formula or
// a derived one) can be deleted, and only while it is disabled.
// @Summary Batch operation on algorithms
// @Router /api/v1/algorithms/batch [post]
func (r *ForecastRouter) BatchAlgorithms(c *gin.Context) {
	var req struct {
		Action string   `json:"action" binding:"required"`
		IDs    []string `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 || len(req.IDs) > maxBatch {
		query.BadRequest(c, errors.New("action and 1 to 500 ids are required"))
		return
	}
	if req.Action != "enable" && req.Action != "disable" && req.Action != "delete" {
		query.BadRequest(c, errors.New("action must be enable, disable or delete"))
		return
	}
	ctx := c.Request.Context()
	if err := r.Service.EnsureAlgorithms(ctx); err != nil {
		query.InternalError(c, err)
		return
	}
	out := batchResult{Failed: []batchFailure{}}
	for _, id := range req.IDs {
		cfg, err := r.Configs.Get(ctx, id)
		if err != nil {
			out.fail(id, err)
			continue
		}
		switch req.Action {
		case "delete":
			if !cfg.UserDefined() {
				out.fail(id, errors.New("a built-in algorithm cannot be deleted; disable it instead"))
				continue
			}
			if cfg.Enabled {
				out.fail(id, errors.New("disable the algorithm before deleting it"))
				continue
			}
			if err := r.Configs.Delete(ctx, id); err != nil {
				out.fail(id, err)
				continue
			}
			forecast.RemoveCustom(id)
		default:
			cfg.Enabled = req.Action == "enable"
			if err := cfg.Validate(); err != nil {
				out.fail(id, err)
				continue
			}
			if err := r.Configs.Save(ctx, cfg); err != nil {
				out.fail(id, err)
				continue
			}
		}
		out.Done++
	}
	query.SuccessResponse(c, nil, out)
}

// BatchDeleteRecords deletes rows of a record list (scores, backtests, evaluations, trainings, executions, admissions).
// @Summary Batch delete records
// @Router /api/v1/records/batch-delete [post]
func (r *ForecastRouter) BatchDeleteRecords(c *gin.Context) {
	var req struct {
		Kind string `json:"kind" binding:"required"`
		IDs  []uint `json:"ids" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || len(req.IDs) == 0 || len(req.IDs) > 5000 {
		query.BadRequest(c, errors.New("kind and 1 to 5000 ids are required"))
		return
	}
	if _, ok := rdb.RecordKinds[req.Kind]; !ok {
		query.ErrorResponse(c, http.StatusBadRequest, fmt.Errorf("records of kind %q cannot be deleted in a batch", req.Kind))
		return
	}
	n, err := r.Predictions.DeleteRecords(c.Request.Context(), req.Kind, req.IDs)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			query.NotFound(c, query.ErrRecordNotFound)
			return
		}
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"done": n})
}
