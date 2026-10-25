package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kubeforecast/pkg/model"
)

func seedSnapshot(t *testing.T, moduleID uint, version int, at time.Time, mutate func(*model.Recommendation)) {
	t.Helper()
	rec := &model.Recommendation{ModuleID: moduleID, Version: version, Mode: model.ModeRecommend, ValidUntil: at.Add(time.Hour),
		CPUSet: true, CPUMilli: 500, CPUSource: "algorithm", ReplicasSet: true, Replicas: 3, ReplicasSource: "algorithm", Detail: "{}"}
	if mutate != nil {
		mutate(rec)
	}
	require.NoError(t, model.DB.Create(rec).Error)
	require.NoError(t, model.DB.Model(rec).Update("created_at", at).Error)
}

func TestRecommendationSearchFiltersAndSorts(t *testing.T) {
	r, _ := forecastTestRouter(t)
	a := uint(createModule(t, r, map[string]any{"name": "a", "namespace": "p", "workload": "a"}))
	b := uint(createModule(t, r, map[string]any{"name": "b", "namespace": "p", "workload": "b"}))
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	seedSnapshot(t, a, 1, base, nil)
	seedSnapshot(t, a, 2, base.Add(time.Hour), func(x *model.Recommendation) {
		x.Degraded, x.DegradeReasons, x.CPUSource, x.CPUMilli = true, `{"cpu":"no_data"}`, "fallback", 200
	})
	seedSnapshot(t, b, 1, base.Add(2*time.Hour), func(x *model.Recommendation) { x.OOMFloor = true; x.Replicas = 9 })

	count := func(path string) (int, []any) {
		rec := performJSON(t, r, http.MethodGet, path, "", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		data := decodeData(t, rec.Body.Bytes())
		return int(data["total"].(float64)), data["list"].([]any)
	}
	n, _ := count("/recommendations")
	assert.Equal(t, 3, n)
	n, _ = count("/recommendations?degraded=true")
	assert.Equal(t, 1, n)
	n, _ = count("/recommendations?oom_floor=true")
	assert.Equal(t, 1, n)
	n, _ = count("/recommendations?source=fallback")
	assert.Equal(t, 1, n, "any of the three fields")
	n, _ = count("/recommendations?module_id=" + itoa(int(a)))
	assert.Equal(t, 2, n)
	n, _ = count("/recommendations?from=2026-10-01T01:30:00Z")
	assert.Equal(t, 1, n)
	_, list := count("/recommendations?sort_by=replicas&sort_order=desc")
	assert.EqualValues(t, 9, list[0].(map[string]any)["replicas"])
	_, list = count("/recommendations?sort_by=created_at&sort_order=asc")
	assert.EqualValues(t, 1, list[0].(map[string]any)["version"])
	_, list = count("/recommendations?sort_by=detail;drop")
	assert.Len(t, list, 3, "an unknown sort falls back instead of reaching SQL")
}

func TestFallbackRecordsCarryReasonsAndRecoveryTime(t *testing.T) {
	r, _ := forecastTestRouter(t)
	a := uint(createModule(t, r, map[string]any{"name": "a", "namespace": "p", "workload": "a"}))
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	seedSnapshot(t, a, 1, base, nil)
	seedSnapshot(t, a, 2, base.Add(10*time.Minute), func(x *model.Recommendation) {
		x.Degraded, x.DegradeReasons = true, `{"cpu":"insufficient_history"}`
		x.Detail = `{"degrade_notes":{"cpu":"only 40% contiguous"}}`
	})
	seedSnapshot(t, a, 3, base.Add(20*time.Minute), func(x *model.Recommendation) { x.Degraded, x.DegradeReasons = true, `{"cpu":"recovering"}` })
	seedSnapshot(t, a, 4, base.Add(30*time.Minute), nil)
	seedSnapshot(t, a, 5, base.Add(40*time.Minute), func(x *model.Recommendation) {
		x.OOMFloor = true
		x.Detail = `{"oom":{"active":true,"reason":"1 OOM kill(s)"}}`
	})

	rec := performJSON(t, r, http.MethodGet, "/fallbacks?sort_by=created_at&sort_order=asc", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeData(t, rec.Body.Bytes())
	assert.EqualValues(t, 3, data["total"], "healthy snapshots are not records")
	list := data["list"].([]any)
	first := list[0].(map[string]any)
	assert.Equal(t, map[string]any{"cpu": "insufficient_history"}, first["reasons"])
	assert.Equal(t, "only 40% contiguous", first["notes"].(map[string]any)["cpu"])
	assert.Equal(t, false, first["ongoing"])
	assert.EqualValues(t, 20*60, first["duration_seconds"], "until the first healthy snapshot (v4), not the first non-failing one")
	assert.NotNil(t, first["recovered_at"])
	last := list[2].(map[string]any)
	assert.Equal(t, true, last["ongoing"], "the OOM floor has not ended")
	assert.Equal(t, "oom_floor", last["reasons"].(map[string]any)["memory_floor"])
	assert.Contains(t, last["notes"].(map[string]any)["memory_floor"], "OOM kill")

	rec = performJSON(t, r, http.MethodGet, "/fallbacks?reason=recovering", "", nil)
	assert.EqualValues(t, 1, decodeData(t, rec.Body.Bytes())["total"])
	rec = performJSON(t, r, http.MethodGet, "/fallbacks?reason=oom_floor", "", nil)
	assert.EqualValues(t, 1, decodeData(t, rec.Body.Bytes())["total"])
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodGet, "/fallbacks?reason=%25", "", nil).Code)

	// the export carries the same filter and the degradation columns
	rec = performJSON(t, r, http.MethodGet, "/audit/export?kind=fallbacks", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "degrade_reasons")
	assert.Equal(t, 4, strings.Count(strings.TrimSpace(body), "\n")+1, "header and three records (detail cells hold no raw newlines)")
}

func TestExecutionSearch(t *testing.T) {
	r, _ := forecastTestRouter(t)
	a := uint(createModule(t, r, map[string]any{"name": "a", "namespace": "p", "workload": "a"}))
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i, x := range []model.ScalingExecution{
		{ModuleID: a, Namespace: "p", Scaler: "s", TargetKind: "Deployment", TargetName: "api", Current: 2, Desired: 4, Action: "scale_up", Result: "applied"},
		{ModuleID: a, Namespace: "p", Scaler: "s", TargetKind: "Deployment", TargetName: "web", Current: 4, Desired: 3, Action: "hold", Result: "skipped", Reason: "cooldown"},
	} {
		x := x
		require.NoError(t, model.DB.Create(&x).Error)
		require.NoError(t, model.DB.Model(&x).Update("created_at", base.Add(time.Duration(i)*time.Hour)).Error)
	}
	total := func(path string) int {
		rec := performJSON(t, r, http.MethodGet, path, "", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return int(decodeData(t, rec.Body.Bytes())["total"].(float64))
	}
	assert.Equal(t, 2, total("/scaling-decisions"))
	assert.Equal(t, 1, total("/scaling-decisions?result=skipped"))
	assert.Equal(t, 1, total("/scaling-decisions?action=scale_up"))
	assert.Equal(t, 1, total("/scaling-decisions?keyword=cooldown"))
	assert.Equal(t, 1, total("/scaling-decisions?from=2026-10-01T00:30:00Z"))
}

func TestModuleListCarriesWhatTheModuleHasProduced(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	a := moduleWithCPU(t, r, cpu, map[string]any{"name": "a", "namespace": "p", "workload": "a"})
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	seedSnapshot(t, uint(a), 1, base, nil)
	seedSnapshot(t, uint(a), 2, base.Add(time.Hour), func(x *model.Recommendation) { x.Degraded, x.DegradeReasons = true, `{"cpu":"no_data"}` })
	require.NoError(t, model.DB.Create(&model.ModuleRunState{ModuleID: uint(a), ConsecutiveFailures: 3, LastError: "boom"}).Error)

	rec := performJSON(t, r, http.MethodGet, "/modules", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	list := decodeData(t, rec.Body.Bytes())["list"].([]any)
	require.Len(t, list, 1)
	module := list[0].(map[string]any)
	assert.Equal(t, "a", module["name"], "the module's own fields are still at the top level")
	assert.Len(t, module["sources"], 1, "the module lists the metrics it reads")
	summary := module["summary"].(map[string]any)
	assert.EqualValues(t, 2, summary["latest_version"])
	assert.Equal(t, true, summary["degraded"])
	assert.JSONEq(t, `{"cpu":"no_data"}`, summary["degrade_reasons"].(string))
	assert.EqualValues(t, 3, summary["consecutive_failures"])
	assert.Equal(t, "boom", summary["last_error"])
}

func TestHistoryOfAnUncollectedSourceIsEmptyNotAnError(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	a := moduleWithCPU(t, r, cpu, map[string]any{"name": "uncollected"})
	rec := performJSON(t, r, http.MethodGet, "/modules/"+itoa(a)+"/history?metric=cpu", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	data := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, true, data["not_collected"])
	assert.Empty(t, data["points"])
	// a metric the module has no source for is still a mistake of the caller
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodGet, "/modules/"+itoa(a)+"/history?metric=memory", "", nil).Code)
}
