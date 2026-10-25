package api

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/dao/rdb"
)

// ListTrainings serves the training records: one per run of an algorithm on a source of a module, with the window it
// read, its parameters (and formula, for a custom algorithm), the duration and the outcome.
// @Summary Training records
// @Tags Forecast
// @Router /api/v1/training-runs [get]
func (r *ForecastRouter) ListTrainings(c *gin.Context) {
	page, size := pageParams(c)
	list, total, err := r.Predictions.ListTrainings(c.Request.Context(), rdb.TrainingFilter{
		ModuleID: uintQuery(c, "module_id"), Metric: c.Query("metric"), AlgorithmID: c.Query("algorithm_id"), Status: c.Query("status"),
		From: timeQuery(c, "from"), To: timeQuery(c, "to"),
	}, page, size)
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, gin.H{"list": list, "total": total, "page": page, "pageSize": size})
}

// TrainingSummary serves the counts behind the training records page: how many runs succeeded, were skipped or failed,
// per algorithm, and the most common reasons a run did not produce a forecast.
func (r *ForecastRouter) TrainingSummary(c *gin.Context) {
	summary, err := r.Predictions.SummarizeTrainings(c.Request.Context(), rdb.TrainingFilter{
		ModuleID: uintQuery(c, "module_id"), Metric: c.Query("metric"), AlgorithmID: c.Query("algorithm_id"), Status: c.Query("status"),
		From: timeQuery(c, "from"), To: timeQuery(c, "to"),
	})
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, summary)
}

// GetTraining serves one training record.
func (r *ForecastRouter) GetTraining(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		query.BadRequest(c, errors.New("invalid id"))
		return
	}
	t, err := r.Predictions.GetTraining(c.Request.Context(), uint(id))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		query.NotFound(c, query.ErrRecordNotFound)
		return
	}
	if err != nil {
		query.InternalError(c, err)
		return
	}
	query.SuccessResponse(c, nil, t)
}
