package timing

import "time"

// The limits of the HTTP side (see requirements/references/ai-and-performance-p11.md, B0 and B6). They are system parameters like every other
// limit here: nothing about a time-out is fixed in the code.
var (
	// HTTPGatewayTimeout is how long the gateway or ingress in front of the platform waits for an answer (its read time-out). The platform cannot
	// see it, so the administrator says it. A request that is answered synchronously is kept inside it, so the platform answers with a reason
	// before the gateway cuts the connection with a bare 504.
	HTTPGatewayTimeout = Param{Key: "http.gateway_timeout_seconds", Default: 60, Min: 10, Max: 3600,
		Name:   "Gateway Read Timeout (seconds)",
		Remark: "How long the gateway or ingress in front of the platform waits for an answer (its read timeout; the common default is 60). The platform cannot see it, so say it here. Operations that are answered in one request (a manual run of a pipeline step, a change preview, a question to the AI assistant) are stopped 5 seconds before it, with a message that says what happened, instead of being cut by the gateway with a bare 504. If you raise the timeout of the gateway, raise this to the same number (10-3600)."}
	// HTTPSlowRequest is the time over which a request is logged as slow; 0 turns the log off.
	HTTPSlowRequest = Param{Key: "http.slow_request_seconds", Default: 3, Min: 0, Max: 600,
		Name:   "Slow Request Log Threshold (seconds)",
		Remark: "A request that takes longer than this is written to the log with its method, route, status, time and request id, and so is every request that the client or the gateway gave up on before it was answered. It names the page behind a 502 or 504 instead of guessing. 0 turns the log off (0-600)."}
	// UIRequestTimeout is the time the browser waits for one ordinary request.
	UIRequestTimeout = Param{Key: "ui.request_timeout_seconds", Default: 60, Min: 10, Max: 600,
		Name:   "Browser Request Timeout (seconds)",
		Remark: "How long the web console waits for one ordinary request before it stops waiting and says so. The requests known to be long (a pipeline step, a change preview, a question to the AI assistant) wait for the gateway timeout plus 10 seconds instead, so the answer of the platform comes first. Waiting longer is not a cure for a slow page (10-600)."}
	// UIRequestRetries is how many times the browser asks again for a read that failed because the platform was not reachable.
	UIRequestRetries = Param{Key: "ui.request_retries", Default: 1, Min: 0, Max: 3,
		Name:   "Browser Read Retries",
		Remark: "How many times the web console asks again for a read (GET) that failed with a network error, 502 or 503, which is what a restart or a rollout of the platform looks like for a moment. A timeout or a 504 is never asked again: that would only repeat the slow work. 0 turns it off (0-3)."}
)

// GatewayMargin is how much earlier than the gateway the platform stops a request it answers in one go.
const GatewayMargin = 5 * time.Second

// Under keeps the budget of a synchronous request inside the gateway timeout: the configured budget when it fits, else the gateway timeout less
// the margin (never below the margin itself, so a very small gateway timeout still leaves some time). A budget that is zero or less is the
// whole room that is left.
func Under(gateway, configured time.Duration) time.Duration {
	room := gateway - GatewayMargin
	if room < GatewayMargin {
		room = GatewayMargin
	}
	if configured <= 0 || configured > room {
		return room
	}
	return configured
}

// PruneBudget is the longest one run of the hourly clean-up spends deleting old forecasts (the biggest table of the platform).
var PruneBudget = Param{Key: "forecast.prune_budget_seconds", Default: 120, Min: 10, Max: 3600,
	Name:   "Forecast Clean-up Time Per Run (seconds)",
	Remark: "The hourly clean-up deletes the forecasts older than their retention a batch of ten thousand rows at a time, for at most this long; what is left is deleted by the next run. One delete of millions of rows is a long transaction that slows the pages while it runs. Raise it when the forecast table is far behind its retention, lower it when the database is busy (10-3600)."}
