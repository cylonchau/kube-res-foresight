package middleware

import (
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWebsocketBearerReadsTokenFromSubprotocol(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/market/stream", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Protocol", WebSocketAuthSubprotocol+", abc.def.ghi")
	assert.Equal(t, "Bearer abc.def.ghi", websocketBearer(req))

	// A plain request must never authenticate through the subprotocol header.
	plain := httptest.NewRequest(http.MethodGet, "/api/v1/market/stream", nil)
	plain.Header.Set("Sec-WebSocket-Protocol", WebSocketAuthSubprotocol+", abc.def.ghi")
	assert.Empty(t, websocketBearer(plain))

	// The marker without a token yields nothing.
	req.Header.Set("Sec-WebSocket-Protocol", WebSocketAuthSubprotocol)
	assert.Empty(t, websocketBearer(req))
}
