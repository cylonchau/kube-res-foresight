package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
)

// A question that took more than one request may is answered with the reason and the platform's mark, not as a bad gateway of the model.
func TestAQuestionThatRanOutOfTimeIsAnsweredWithTheReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/assistant/chat", nil)
	(&AssistantRouter{}).reply(c, nil, fmt.Errorf("the model did not answer: %w", context.DeadlineExceeded))
	assert.Equal(t, http.StatusGatewayTimeout, w.Code)
	assert.Equal(t, "platform", w.Header().Get(query.TimeoutSourceHeader))
}
