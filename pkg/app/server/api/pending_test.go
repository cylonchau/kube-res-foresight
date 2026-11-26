package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cylonchau/kube-res-foresight/pkg/forecastsvc"
	"github.com/cylonchau/kube-res-foresight/pkg/resultcache"
)

// A heavy read that is still being worked out is answered 202 with the sign the page reads, and any other error is left to the handler.
func TestAHeavyReadThatIsStillWorkingIsAnswered202WithTheSignToAskAgain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	assert.False(t, answerPending(c, errors.New("boom")), "an error that is not 'being worked out' is not answered here")
	assert.False(t, answerPending(c, nil))
	require.True(t, answerPending(c, forecastsvc.ErrPending))
	assert.Equal(t, http.StatusAccepted, w.Code)
	assert.Equal(t, "3", w.Header().Get("Retry-After"))
	var body struct {
		Data struct {
			Pending bool `json:"pending"`
			Retry   int  `json:"retry_after_seconds"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.True(t, body.Data.Pending)
	assert.Equal(t, PendingRetrySeconds, body.Data.Retry)
	assert.True(t, errors.Is(forecastsvc.ErrPending, resultcache.ErrPending), "the service and the cache say the same thing")
}
