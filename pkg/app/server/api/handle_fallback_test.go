package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

func seedSnapshot(t *testing.T, moduleID uint, version int, at time.Time, mutate func(*model.Recommendation)) {
	t.Helper()
	rec := &model.Recommendation{ModuleID: moduleID, Version: version, Mode: model.ModeRecommend, ValidUntil: at.Add(time.Hour),
		CPUSet: true, CPUMilli: 500, CPUSource: "algorithm", ReplicasSet: true, Replicas: 3, ReplicasSource: "algorithm", Detail: "{}", Snapshot: true}
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
	assert.EqualValues(t, 2, data["total"], "healthy snapshots are not records, and v3 is the same episode as v2")
	list := data["list"].([]any)
	first := list[0].(map[string]any)
	assert.Equal(t, "degrade", first["kind"])
	changes := first["changes"].([]any)
	require.Len(t, changes, 1)
	change := changes[0].(map[string]any)
	assert.Equal(t, "cpu", change["field"])
	assert.Equal(t, "insufficient_history", change["trigger"])
	assert.EqualValues(t, 500, change["original"].(map[string]any)["value"], "the configuration in force before the degradation")
	assert.EqualValues(t, 500, change["target"].(map[string]any)["value"])
	assert.NotEmpty(t, first["result"], "what became of the configuration it switched to")
	assert.Equal(t, "expired", first["result_state"], "nothing used the snapshot and it was replaced")
	assert.Equal(t, map[string]any{"cpu": "insufficient_history"}, first["reasons"])
	assert.Equal(t, "only 40% contiguous", first["notes"].(map[string]any)["cpu"])
	assert.Equal(t, false, first["ongoing"])
	assert.EqualValues(t, 20*60, first["duration_seconds"], "until the first healthy snapshot (v4), not the first non-failing one")
	assert.NotNil(t, first["recovered_at"])
	last := list[1].(map[string]any)
	assert.Equal(t, true, last["ongoing"], "the OOM floor has not ended")
	assert.Equal(t, "oom_floor", last["reasons"].(map[string]any)["memory_floor"])
	assert.Contains(t, last["notes"].(map[string]any)["memory_floor"], "OOM kill")

	rec = performJSON(t, r, http.MethodGet, "/fallbacks?reason=insufficient_history", "", nil)
	assert.EqualValues(t, 1, decodeData(t, rec.Body.Bytes())["total"])
	rec = performJSON(t, r, http.MethodGet, "/fallbacks?reason=recovering", "", nil)
	assert.EqualValues(t, 0, decodeData(t, rec.Body.Bytes())["total"], "an episode is listed under the reason it started with: v3 only continues the one of v2")
	rec = performJSON(t, r, http.MethodGet, "/fallbacks?reason=oom_floor", "", nil)
	assert.EqualValues(t, 1, decodeData(t, rec.Body.Bytes())["total"])
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodGet, "/fallbacks?reason=%25", "", nil).Code)
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

// A change that was decided but not applied is a rollback (the workload stays on the configuration that was valid); a hold because the
// operator had no usable decision is a degradation. Pacing (a cooldown) is neither.
func TestDegradationAndRollbackAreDifferentRecords(t *testing.T) {
	r, _ := forecastTestRouter(t)
	a := uint(createModule(t, r, map[string]any{"name": "a", "namespace": "p", "workload": "a"}))
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i, x := range []model.ScalingExecution{
		{ModuleID: a, Namespace: "p", Scaler: "s", TargetKind: "Deployment", TargetName: "api", Current: 4, Desired: 6, Action: "scale_up", Result: model.ExecError, Reason: "forbidden: scale not allowed", DecisionVersion: 7},
		{ModuleID: a, Namespace: "p", Scaler: "s", TargetKind: "Deployment", TargetName: "api", Current: 4, Desired: 5, Action: "none", Result: model.ExecConflict, Reason: "another autoscaler controls this target", DecisionVersion: 8},
		{ModuleID: a, Namespace: "p", Scaler: "s", TargetKind: "Deployment", TargetName: "api", Current: 4, Desired: 4, Action: "none", Result: model.ExecHeld, Reason: "decision unavailable: connection refused; using fallback replicas", DecisionVersion: 0},
		{ModuleID: a, Namespace: "p", Scaler: "s", TargetKind: "Deployment", TargetName: "api", Current: 4, Desired: 3, Action: "none", Result: model.ExecHeld, Reason: "cooldown: 30s since the last scaling action, 2m required", DecisionVersion: 9},
		{ModuleID: a, Namespace: "p", Scaler: "s", TargetKind: "Deployment", TargetName: "api", Current: 4, Desired: 6, Action: "scale_up", Result: model.ExecApplied, DecisionVersion: 10},
	} {
		x := x
		require.NoError(t, model.DB.Create(&x).Error)
		require.NoError(t, model.DB.Model(&x).Update("created_at", base.Add(time.Duration(i)*time.Hour)).Error)
	}
	seedSnapshot(t, a, 1, base, nil)
	seedSnapshot(t, a, 2, base.Add(30*time.Minute), func(x *model.Recommendation) {
		x.Degraded, x.DegradeReasons, x.CPUSource, x.CPUMilli = true, `{"cpu":"no_data"}`, "fallback", 200
	})

	get := func(path string) (int, []map[string]any) {
		rec := performJSON(t, r, http.MethodGet, path, "", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		data := decodeData(t, rec.Body.Bytes())
		var list []map[string]any
		for _, item := range data["list"].([]any) {
			list = append(list, item.(map[string]any))
		}
		return int(data["total"].(float64)), list
	}
	total, list := get("/fallbacks?sort_by=created_at&sort_order=asc")
	assert.Equal(t, 4, total, "an error, a conflict, a decision-less hold and the degradation of the forecast; a cooldown and a success are not records")

	n, rollbacks := get("/fallbacks?kind=rollback&sort_by=created_at&sort_order=asc")
	require.Equal(t, 2, n)
	first := rollbacks[0]
	assert.Equal(t, "rollback", first["kind"])
	change := first["changes"].([]any)[0].(map[string]any)
	assert.Equal(t, "replicas", change["field"])
	assert.Equal(t, "error", change["trigger"])
	assert.EqualValues(t, 6, change["original"].(map[string]any)["value"], "what was wanted")
	assert.EqualValues(t, 4, change["target"].(map[string]any)["value"], "what the workload stays on")
	assert.Equal(t, "not_applied", first["result_state"])
	assert.Contains(t, first["result"], "stays on 4")
	assert.Contains(t, change["note"], "forbidden")
	assert.NotNil(t, first["recovered_at"], "a later successful execution ends the problem")
	assert.Equal(t, false, first["ongoing"])
	assert.Equal(t, "conflict", rollbacks[1]["changes"].([]any)[0].(map[string]any)["trigger"])

	n, degrades := get("/fallbacks?kind=degrade&sort_by=created_at&sort_order=asc")
	require.Equal(t, 2, n)
	kinds := map[string]string{}
	for _, d := range degrades {
		kinds[d["id"].(string)[:1]] = d["changes"].([]any)[0].(map[string]any)["trigger"].(string)
	}
	assert.Equal(t, "no_data", kinds["d"], "the forecast degradation")
	assert.Equal(t, "decision_unavailable", kinds["e"], "the operator without a decision")
	n, _ = get("/fallbacks?reason=decision_unavailable")
	assert.Equal(t, 1, n)
	n, _ = get("/fallbacks?reason=no_data")
	assert.Equal(t, 1, n, "a forecast reason does not list operator reports")
	_ = list

	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodGet, "/fallbacks?kind=other", "", nil).Code)
}
