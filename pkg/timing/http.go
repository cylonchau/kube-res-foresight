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

// BackgroundRead is how long a heavy read that did not finish within its request goes on working by itself (see resultcache.Within).
var BackgroundRead = Param{Key: "http.background_read_seconds", Default: 300, Min: 30, Max: 3600,
	Name:   "Heavy Read Background Time (seconds)",
	Remark: "A read that asks Prometheus for a long window (the resource analysis of a module, the table of the resource dashboard, the cluster figures) is answered within the read request time limit when it can be. When it cannot, the request is answered as 'being worked out' and the read goes on by itself for at most this long; the page asks again and gets the answer when it is ready, instead of a 504 that throws the work away. Asks that come meanwhile join the same work. It is bounded by the number of queries one data source takes at once (3600 at the most; 30-3600)."}

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

// ListQuery is the longest a read request (a list, a report, a chart) may work before the platform stops it and says so. It also stops the
// statements the request sent to the database: a request that the gateway has cut would otherwise leave its query running, and a few of those
// hold every connection of the pool, so the pages that were fast fail as well. It is kept under the gateway timeout.
var ListQuery = Param{Key: "http.list_query_seconds", Default: 30, Min: 3, Max: 600,
	Name:   "Read Request Time Limit (seconds)",
	Remark: "The longest a read request (a list, a report, a chart) may take. When it runs out the platform stops it, cancels its database statements and answers 504 with the reason, before the gateway cuts the connection. The pages that read the most (forecast results, recommendations and execution) are the ones to look at when this is reached: narrow the time range or add a filter, or raise it while http.gateway_timeout_seconds allows (3-600)."}

// HeavyReadWait is how long one request waits for a heavy read before it answers "being worked out". It is shorter than the read time limit on purpose: a gateway
// in front of the platform may wait less than the gateway time-out the platform was told, and the page asks again at once and shows the progress.
var HeavyReadWait = Param{Key: "http.heavy_read_wait_seconds", Default: 15, Min: 3, Max: 120,
	Name:   "Heavy Read Wait Per Request (seconds)",
	Remark: "A read that asks Prometheus for a long window (the resource analysis of a module, the resource dashboard, the cluster figures) holds one request for at most this long. When the work is not done by then the platform answers 'being worked out' with its progress, goes on by itself, and the page asks again until the answer is there. A short wait keeps the request under any gateway in front of the platform, so no 504 comes from waiting; it is also kept under the read request time limit and the gateway timeout (3-120)."}

// ListSearchDays is how far back a text search in a list reads when the request names no time range. A search for a word in the reason or the
// name of the rows of a table that grows (the scaling records, the admission records) cannot use an index: it reads every row it looks at. The
// time range is what an index can cut, so a search with no range reads the last days only, and the list says so.
var ListSearchDays = Param{Key: "http.list_search_days", Default: 7, Min: 1, Max: 365,
	Name:   "Text Search Window (days)",
	Remark: "A text search in the scaling records, the admission records and the degradation records, with no time range given, reads this many days back (the text cannot use an index, the time can). The list says so; give a time range to read further back (1-365)."}

// SummaryWindowHours is how far back the counts of a summary page read when the request names no time range. A count of every record of a
// table that grows, grouped, reads all of it; the last day is what the page is looked at for.
var SummaryWindowHours = Param{Key: "http.summary_window_hours", Default: 24, Min: 1, Max: 720,
	Name:   "Summary Window (hours)",
	Remark: "The counts of a summary page (the algorithm run status, for example) read this many hours back when the request gives no time range: the totals of a table that grows are the slowest read of a page. The page says so; give a time range to read further back (1-720)."}

// PrometheusSubqueryPoints is the most points one percentile query over a window is asked to evaluate (see forecastsvc subqueryStep).
var PrometheusSubqueryPoints = Param{Key: "forecast.prometheus_subquery_points", Default: 1500, Min: 100, Max: 11000,
	Name:   "Prometheus Points Per Percentile Query",
	Remark: "The most points the percentiles of a resource (P50, P90, P95, P99, Max) are worked out from for one metric over the analysis window. The step is the window divided by this, rounded up to a whole multiple of the step of the metric mapping, and never finer than that step: 7 days at 1500 points is 403 s, so 420 s with a mapping step of 60 s; a window short enough to need less than the mapping step (1 hour) keeps the mapping step. More points find shorter peaks and are slower for Prometheus; fewer are faster and smooth more (100-11000)."}

// PrometheusTotalConcurrency is how many queries to one data source may be in flight at once in the whole platform process.
var PrometheusTotalConcurrency = Param{Key: "forecast.prometheus_total_concurrency", Default: 8, Min: 1, Max: 64,
	Name:   "Prometheus Queries In Flight Per Data Source",
	Remark: "The most range queries the platform has in flight to one data source at once, for every request together (pages, collections, query tests). The max concurrency of the data source limits one fetch; this limits all of them, so many people opening the history page at once do not make Prometheus answer everyone slowly. A query waits for its turn and gives up with its request. Raise it when Prometheus is idle and queries wait, lower it when Prometheus is slow under the load (1-64)."}
