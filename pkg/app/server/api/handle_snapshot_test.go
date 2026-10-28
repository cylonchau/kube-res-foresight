package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-resource-fcst/pkg/model"
)

// The snapshots are the history of what was recommended and what became of it. A repeat of the same answer is not a snapshot; the
// state of a snapshot is what the operator and the admission reported for the span it covered.
func TestSnapshotsCarryWhatBecameOfThem(t *testing.T) {
	r, _ := forecastTestRouter(t)
	a := uint(createModule(t, r, map[string]any{"name": "a", "namespace": "p", "workload": "a"}))
	base := time.Now().UTC().Add(-6 * time.Hour)
	mode := func(m string) func(x *model.Recommendation) { return func(x *model.Recommendation) { x.Mode = m } }

	seedSnapshot(t, a, 1, base, mode(model.ModeAuto))                                                                          // applied by the operator
	seedSnapshot(t, a, 2, base.Add(10*time.Minute), func(x *model.Recommendation) { x.Snapshot = false })                      // a repeat: not history
	seedSnapshot(t, a, 3, base.Add(20*time.Minute), func(x *model.Recommendation) { x.Mode = model.ModeAuto; x.Replicas = 5 }) // the operator failed
	seedSnapshot(t, a, 4, base.Add(40*time.Minute), func(x *model.Recommendation) { x.Mode = model.ModeAuto; x.Replicas = 6 }) // replaced unused
	seedSnapshot(t, a, 5, base.Add(60*time.Minute), func(x *model.Recommendation) {
		x.Mode, x.Replicas, x.ValidUntil, x.ResourceVersion = model.ModeAuto, 7, time.Now().UTC().Add(time.Hour), 2
	}) // current
	for _, e := range []model.ScalingExecution{
		{ModuleID: a, Namespace: "p", Scaler: "s", TargetKind: "Deployment", TargetName: "w", Current: 2, Desired: 3, Action: "scale_up", Result: model.ExecApplied, DecisionVersion: 2}, // version 2 belongs to the span of snapshot 1
		{ModuleID: a, Namespace: "p", Scaler: "s", TargetKind: "Deployment", TargetName: "w", Current: 3, Desired: 5, Action: "scale_up", Result: model.ExecError, Reason: "forbidden", DecisionVersion: 3},
	} {
		e := e
		require.NoError(t, model.DB.Create(&e).Error)
	}

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
	total, list := get("/snapshots?module_id=" + itoa(int(a)))
	assert.Equal(t, 4, total, "version 2 is a repeat and not listed")
	states := map[int]string{}
	for _, item := range list {
		states[int(item["version"].(float64))] = item["state"].(string)
	}
	assert.Equal(t, map[int]string{1: "applied", 3: "failed", 4: "expired", 5: "pending"}, states)
	assert.Equal(t, true, list[0]["current"], "the newest snapshot is the current one")
	assert.EqualValues(t, 5, list[0]["version"], "newest first")
	assert.NotEmpty(t, list[0]["state_reason"])
	assert.Empty(t, list[0]["detail"], "the list leaves the stored decision out")

	n, only := get("/snapshots?state=failed")
	assert.Equal(t, 1, n)
	assert.EqualValues(t, 3, only[0]["version"])
	n, _ = get("/snapshots?state=expired")
	assert.Equal(t, 1, n)
	n, page := get("/snapshots?module_id=" + itoa(int(a)) + "&page=2&page_size=3")
	assert.Equal(t, 4, n)
	assert.Len(t, page, 1)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodGet, "/snapshots?state=done", "", nil).Code)

	// the detail has the stored decision and the executions of the span
	var failedID float64
	for _, item := range list {
		if item["version"].(float64) == 3 {
			failedID = item["id"].(float64)
		}
	}
	rec := performJSON(t, r, http.MethodGet, "/snapshots/"+itoa(int(failedID)), "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	detail := decodeData(t, rec.Body.Bytes())
	assert.Equal(t, "failed", detail["state"])
	assert.Contains(t, detail["state_reason"], "forbidden")
	assert.NotNil(t, detail["detail"])
	execs := detail["executions"].([]any)
	require.Len(t, execs, 1)
	assert.Equal(t, "forbidden", execs[0].(map[string]any)["reason"])

	// a repeat is not a snapshot: it has no page of its own
	require.NoError(t, model.DB.Model(&model.Recommendation{}).Where("module_id = ? AND version = ?", a, 2).Select("id").Scan(&failedID).Error)
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodGet, "/snapshots/"+itoa(int(failedID)), "", nil).Code)
}

// In a mode that only recommends nothing applies the snapshot: it says so instead of waiting to be executed.
func TestARecommendOnlyModuleSnapshotIsAdvisoryNotPending(t *testing.T) {
	r, _ := forecastTestRouter(t)
	a := uint(createModule(t, r, map[string]any{"name": "a", "namespace": "p", "workload": "a"}))
	seedSnapshot(t, a, 1, time.Now().UTC().Add(-time.Minute), func(x *model.Recommendation) { x.ValidUntil = time.Now().UTC().Add(time.Hour) })
	rec := performJSON(t, r, http.MethodGet, "/snapshots", "", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	list := decodeData(t, rec.Body.Bytes())["list"].([]any)
	require.Len(t, list, 1)
	assert.Equal(t, "advisory", list[0].(map[string]any)["state"])
}
