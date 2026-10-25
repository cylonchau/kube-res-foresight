package api

import (
	"bufio"
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/cylonchau/kube-resource-fcst/pkg/app/middleware"
	"github.com/gin-gonic/gin"
	"net"
	"net/http"
	"time"
)

const (
	statsTransportPollInterval = time.Second
	statsTransportPingInterval = 30 * time.Second
	statsTransportBatchLimit   = 500
	statsTransportWriteTimeout = 5 * time.Second
)

// streamMessage carries a statistics invalidation.
type streamMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// statsTransportAcceptOptions skips the browser Origin check on purpose. The
// stream is authenticated by a bearer token carried in the subprotocol, which a
// third-party page cannot obtain or attach, so there is no cross-site hijacking
// to defend against; enforcing Origin only broke legitimate dev servers and
// reverse proxies (which rewrite Host) with an opaque 403.
func statsTransportAcceptOptions() *websocket.AcceptOptions {
	return &websocket.AcceptOptions{
		Subprotocols:       []string{middleware.WebSocketAuthSubprotocol},
		InsecureSkipVerify: true,
	}
}

// websocketWriter adapts gin's writer for the WebSocket handshake. gin >= 1.10
// refuses to Hijack a writer whose header was flushed, which is what the
// library does when it detects gin, so the 101 status goes to the net/http
// writer directly while Hijack still goes through gin, which then knows the
// response was taken over and does not write to the hijacked connection.
type websocketWriter struct {
	gin gin.ResponseWriter
	raw http.ResponseWriter
}

func (w websocketWriter) Header() http.Header         { return w.raw.Header() }
func (w websocketWriter) Write(b []byte) (int, error) { return w.raw.Write(b) }
func (w websocketWriter) WriteHeader(code int)        { w.raw.WriteHeader(code) }
func (w websocketWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.gin.Hijack()
}

func newWebsocketWriter(w gin.ResponseWriter) http.ResponseWriter {
	var raw http.ResponseWriter = w
	for {
		unwrapper, ok := raw.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return websocketWriter{gin: w, raw: raw}
		}
		raw = unwrapper.Unwrap()
	}
}

func writeStream(ctx context.Context, conn *websocket.Conn, message streamMessage) bool {
	payload, err := json.Marshal(message)
	if err != nil {
		return true
	}
	writeCtx, cancel := context.WithTimeout(ctx, statsTransportWriteTimeout)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, payload) == nil
}
