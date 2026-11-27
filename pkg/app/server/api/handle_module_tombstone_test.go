package api

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The parameters of one module go onto several in one call; each target is told apart: one that is switched on is refused with its reason and the others are done.
func TestTheParametersOfAModuleCanBeCopiedOntoSeveralAndEachTargetIsAnswered(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	ref := uint(moduleWithCPU(t, r, cpu, map[string]any{"name": "ref", "workload": "ref", "min_replicas": 2, "max_replicas": 9, "scale_up_max_step": 2}))
	a, b := registeredModule(t, "auto-a"), registeredModule(t, "auto-b")
	// a module made by hand is switched on when it is made
	on := uint(moduleWithCPU(t, r, cpu, map[string]any{"name": "already-on", "workload": "already-on", "min_replicas": 1, "max_replicas": 3}))

	rec := performJSON(t, r, http.MethodPost, "/modules/batch-copy-config", "", map[string]any{"from_id": ref, "target_ids": []uint{a, b, on, a, 99999, ref}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	out := decodeData(t, rec.Body.Bytes())
	rows := out["results"].([]any)
	byID := map[uint]map[string]any{}
	for _, row := range rows {
		m := row.(map[string]any)
		byID[uint(m["id"].(float64))] = m
	}
	assert.Len(t, rows, 5, "the same module named twice is done once")
	assert.Equal(t, true, byID[a]["ok"])
	assert.Equal(t, true, byID[b]["ok"])
	assert.Equal(t, false, byID[on]["ok"], "a module that is switched on is refused")
	assert.NotEmpty(t, byID[on]["error"])
	assert.Equal(t, false, byID[99999]["ok"])
	assert.Equal(t, "the module does not exist", byID[99999]["error"])
	assert.Equal(t, false, byID[ref]["ok"], "the source itself is refused")
	assert.EqualValues(t, 2, out["copied"])
	assert.EqualValues(t, 3, out["failed"])
	for _, id := range []uint{a, b} {
		got := decodeData(t, performJSON(t, r, http.MethodGet, fmt.Sprintf("/modules/%d", id), "", nil).Body.Bytes())
		assert.EqualValues(t, 9, got["max_replicas"])
		assert.Equal(t, "shop", got["namespace"], "who the module is stays")
		assert.Equal(t, false, got["enabled"], "copying does not switch it on")
	}

	// the request is checked before anything is done
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/modules/batch-copy-config", "", map[string]any{"from_id": ref, "target_ids": []uint{}}).Code)
	assert.Equal(t, http.StatusBadRequest, performJSON(t, r, http.MethodPost, "/modules/batch-copy-config", "", map[string]any{"from_id": ref, "target_ids": []uint{a}, "groups": []string{"nope"}}).Code)
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodPost, "/modules/batch-copy-config", "", map[string]any{"from_id": 99999, "target_ids": []uint{a}}).Code)
}

// Deleting a module that registered itself leaves a tombstone that a person can list and remove; deleting one made by hand leaves none.
func TestADeletedSelfRegisteredModuleLeavesATombstoneThatCanBeListedAndRemoved(t *testing.T) {
	r, cpu := forecastTestRouter(t)
	auto := registeredModule(t, "auto-del")
	hand := moduleWithCPU(t, r, cpu, map[string]any{"name": "by-hand", "workload": "by-hand", "min_replicas": 1, "max_replicas": 3, "enabled": false})
	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodDelete, fmt.Sprintf("/modules/%d", hand), "", nil).Code)
	assert.Empty(t, decodeData2(t, performJSON(t, r, http.MethodGet, "/modules/tombstones", "", nil).Body.Bytes()))

	require.Equal(t, http.StatusOK, performJSON(t, r, http.MethodDelete, fmt.Sprintf("/modules/%d", auto), "", nil).Code)
	list := decodeData2(t, performJSON(t, r, http.MethodGet, "/modules/tombstones", "", nil).Body.Bytes())
	require.Len(t, list, 1)
	row := list[0].(map[string]any)
	assert.Equal(t, "auto-del", row["module_name"])
	assert.Equal(t, "auto-del", row["workload"])

	id := int(row["id"].(float64))
	assert.Equal(t, http.StatusOK, performJSON(t, r, http.MethodDelete, fmt.Sprintf("/modules/tombstones/%d", id), "", nil).Code)
	assert.Empty(t, decodeData2(t, performJSON(t, r, http.MethodGet, "/modules/tombstones", "", nil).Body.Bytes()))
	assert.Equal(t, http.StatusNotFound, performJSON(t, r, http.MethodDelete, fmt.Sprintf("/modules/tombstones/%d", id), "", nil).Code)
}
