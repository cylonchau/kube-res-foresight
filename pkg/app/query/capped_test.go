package query

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func answer(t *testing.T, data interface{}) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	SuccessResponse(c, nil, data)
	var body struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body.Data
}

func TestAListWhoseTotalReachedTheBoundOfTheCountSaysItsTotalIsAFloor(t *testing.T) {
	got := answer(t, gin.H{"list": []int{1}, "total": int64(CappedTotal), "page": 1})
	assert.Equal(t, true, got["total_capped"])

	got = answer(t, map[string]interface{}{"list": []int{1}, "total": CappedTotal})
	assert.Equal(t, true, got["total_capped"], "a plain map and an int total are the same")

	got = answer(t, gin.H{"list": []int{1}, "total": int64(CappedTotal - 1)})
	assert.NotContains(t, got, "total_capped", "a count under the bound is exact")

	got = answer(t, gin.H{"list": []int{1}, "total": int64(CappedTotal), "total_capped": false})
	assert.Equal(t, false, got["total_capped"], "a list that says it itself is left as it says")

	got = answer(t, gin.H{"total": int64(CappedTotal)})
	assert.NotContains(t, got, "total_capped", "not a list")
	got = answer(t, gin.H{"list": []int{1}, "total": "100000"})
	assert.NotContains(t, got, "total_capped", "a total that is not a number")
}
