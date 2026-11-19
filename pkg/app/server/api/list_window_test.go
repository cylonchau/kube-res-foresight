package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A text search reads the days the parameter says and not the whole table, and the answer says so; a request with a range of its own reads that.
func TestATextSearchWithNoTimeRangeReadsTheLastDaysOnlyAndSaysSo(t *testing.T) {
	r, _ := forecastTestRouter(t)
	a := uint(createModule(t, r, map[string]any{"name": "a", "namespace": "p", "workload": "a"}))
	now := time.Now().UTC()
	for i, age := range []time.Duration{2 * time.Hour, 30 * 24 * time.Hour} {
		x := model.ScalingExecution{ModuleID: a, Namespace: "p", Scaler: "s", TargetKind: "Deployment", TargetName: "api", Current: 2, Desired: 4, Action: "scale_up", Result: "applied", Reason: "cooldown"}
		require.NoError(t, model.DB.Create(&x).Error, i)
		require.NoError(t, model.DB.Model(&x).Update("created_at", now.Add(-age)).Error)
	}
	get := func(path string) map[string]any {
		rec := performJSON(t, r, http.MethodGet, path, "", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return decodeData(t, rec.Body.Bytes())
	}
	searched := get("/scaling-decisions?keyword=cooldown")
	assert.EqualValues(t, 1, searched["total"], "the record of 30 days ago is not read")
	assert.EqualValues(t, 7, searched["search_window_days"], "the answer says what it read")

	own := get("/scaling-decisions?keyword=cooldown&from=" + now.Add(-60*24*time.Hour).Format(time.RFC3339))
	assert.EqualValues(t, 2, own["total"], "a range of its own is read as given")
	assert.EqualValues(t, 0, own["search_window_days"])

	plain := get("/scaling-decisions")
	assert.EqualValues(t, 2, plain["total"], "a list with no search is not cut")
	assert.EqualValues(t, 0, plain["search_window_days"])

	// the window is a system parameter
	require.NoError(t, model.DB.Create(&model.SysConfig{ConfigKey: "http.list_search_days", ConfigValue: "60"}).Error)
	assert.EqualValues(t, 2, get("/scaling-decisions?keyword=cooldown")["total"])
	assert.EqualValues(t, 60, get("/scaling-decisions?keyword=cooldown")["search_window_days"])
}

func TestTheCountsOfTheRunStatusPageReadTheLastDayUnlessTheRequestHasARange(t *testing.T) {
	r, _ := forecastTestRouter(t)
	module := uint(createModule(t, r, map[string]any{"name": "a", "namespace": "p", "workload": "a"}))
	now := time.Now().UTC()
	for _, age := range []time.Duration{time.Hour, 5 * 24 * time.Hour} {
		require.NoError(t, model.DB.Create(&model.TrainingRun{ModuleID: module, Metric: "cpu", AlgorithmID: "peak_margin", Status: "ok", PredictionTime: now.Add(-age)}).Error)
	}
	summary := func(path string) map[string]any {
		rec := performJSON(t, r, http.MethodGet, path, "", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return decodeData(t, rec.Body.Bytes())
	}
	day := summary("/training-runs/summary")
	assert.EqualValues(t, 1, day["total"])
	assert.EqualValues(t, 24, day["window_hours"])
	week := summary("/training-runs/summary?from=" + now.Add(-7*24*time.Hour).Format(time.RFC3339))
	assert.EqualValues(t, 2, week["total"])
	assert.EqualValues(t, 0, week["window_hours"])
}
