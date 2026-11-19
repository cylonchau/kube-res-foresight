package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/reqstat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func measured(t *testing.T) (*gin.Engine, *reqstat.Recorder) {
	gin.SetMode(gin.TestMode)
	rec := reqstat.NewRecorder()
	r := gin.New()
	// the order of the server: the measure is before the deadline that is put on the reads
	r.Use(SlowRequests(nil), Measure(rec), ReadDeadline(func() time.Duration { return 5 * time.Second }))
	return r, rec
}

func hoursOf(t *testing.T, rec *reqstat.Recorder) map[string]reqstat.Hour {
	out := map[string]reqstat.Hour{}
	for _, h := range rec.TakeChanged() {
		out[h.Method+" "+h.Route] = h
	}
	return out
}

func TestARequestIsMeasuredByItsRouteNotItsPathWithTheTimeOfTheLayersAndTheDeadlineIsNotTakenForTheOtherSideLeaving(t *testing.T) {
	r, rec := measured(t)
	r.GET("/modules/:id", func(c *gin.Context) {
		reqstat.AddDB(c.Request.Context(), 7*time.Millisecond)
		reqstat.AddProm(c.Request.Context(), 11*time.Millisecond)
		c.String(200, "ok")
	})
	for _, id := range []string{"1", "2", "3"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/modules/"+id, nil))
		require.Equal(t, 200, w.Code)
	}
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/nothing", nil))
	hours := hoursOf(t, rec)
	require.Len(t, hours, 1, "three paths are one route, and a path that is no route is not a route")
	h := hours["GET /modules/:id"]
	assert.EqualValues(t, 3, h.Count)
	assert.EqualValues(t, 21, h.DBMs)
	assert.EqualValues(t, 3, h.DBCalls)
	assert.EqualValues(t, 33, h.PromMs)
	assert.Zero(t, h.Abandoned, "the deadline of a read is cancelled when the handler returns: that is not the browser going away")
}

func TestARequestTheClientLeftIsCountedAbandonedAndTheStopAtThePlatformsLimitIsToldFromAGatewayStatus(t *testing.T) {
	r, rec := measured(t)
	r.GET("/wait", func(c *gin.Context) { time.Sleep(20 * time.Millisecond); c.String(200, "ok") })
	r.GET("/stop", func(c *gin.Context) {
		query.TimedOut(c, context.DeadlineExceeded)
	})
	r.GET("/gateway", func(c *gin.Context) { c.Status(http.StatusGatewayTimeout) })

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/wait", nil).WithContext(ctx)
	go func() { time.Sleep(5 * time.Millisecond); cancel() }()
	r.ServeHTTP(httptest.NewRecorder(), req)
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/stop", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/gateway", nil))

	hours := hoursOf(t, rec)
	assert.EqualValues(t, 1, hours["GET /wait"].Abandoned)
	assert.EqualValues(t, 1, hours["GET /stop"].Timeouts, "504 with X-Timeout-Source: platform is the platform's own stop")
	assert.EqualValues(t, 1, hours["GET /stop"].Status5xx)
	assert.Zero(t, hours["GET /gateway"].Timeouts, "a 504 that is not marked is not claimed as the platform's")
	assert.EqualValues(t, 1, hours["GET /gateway"].Status5xx)
}

func TestTheStreamsAreNotMeasured(t *testing.T) {
	r, rec := measured(t)
	r.GET("/x/stream", func(c *gin.Context) { c.String(200, "ok") })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x/stream", nil))
	assert.Empty(t, hoursOf(t, rec))
}
