package reqstat

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cylonchau/kube-res-foresight/pkg/model"
)

// RouteSummary is one route over the hours asked for: the baseline of its time, and where the time goes.
type RouteSummary struct {
	Method    string `json:"method"`
	Route     string `json:"route"`
	Count     int64  `json:"count"`
	Status4xx int64  `json:"status_4xx"`
	Status5xx int64  `json:"status_5xx"`
	// Timeouts are the requests the platform stopped at its own time limit (they are also in Status5xx); Abandoned the ones the other side gave up on.
	Timeouts  int64 `json:"timeouts"`
	Abandoned int64 `json:"abandoned"`
	AvgMs     int64 `json:"avg_ms"`
	// P50, P95 and P99 are the upper edges of the buckets of the histogram they fall in: "at most".
	P50   int64 `json:"p50_ms"`
	P95   int64 `json:"p95_ms"`
	P99   int64 `json:"p99_ms"`
	MaxMs int64 `json:"max_ms"`
	// DBShare and PromShare are the fractions of the time of the requests spent in the database and in Prometheus (at most 1: statements that ran in parallel
	// add up to more than the request took); the rest is the platform's own work (computing, encoding the answer, waiting on a lock).
	DBShare    float64   `json:"db_share"`
	PromShare  float64   `json:"prom_share"`
	DBAvgMs    int64     `json:"db_avg_ms"`
	PromAvgMs  int64     `json:"prom_avg_ms"`
	DBCalls    float64   `json:"db_calls_avg"`
	PromCalls  float64   `json:"prom_calls_avg"`
	Layer      string    `json:"layer"`
	Examples   []Slowest `json:"examples"`
	Buckets    []int64   `json:"buckets"`
	hourMerged *Hour
}

// Layers a route's time can be in.
const (
	LayerDatabase   = "database"
	LayerPrometheus = "prometheus"
	LayerHandler    = "handler"
)

// Finding is one thing the numbers say, as a code with its figures: the page writes the sentence in the user's language. Level is critical, warning or info.
type Finding struct {
	Level  string         `json:"level"`
	Code   string         `json:"code"`
	Route  string         `json:"route,omitempty"`
	Layer  string         `json:"layer,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

// The codes of the findings.
const (
	FindingNoData         = "no_data"
	FindingSlowRoute      = "slow_route"
	FindingHalfGateway    = "over_half_gateway"
	FindingPlatformStop   = "platform_timeouts"
	FindingAbandoned      = "abandoned"
	FindingAbandoned60    = "abandoned_at_limit"
	FindingServerErrors   = "server_errors"
	FindingKilled         = "restart_killed"
	FindingMemoryNear     = "memory_near_limit"
	FindingGraceful       = "restart_graceful"
	FindingPoolWaiting    = "pool_waiting"
	FindingDBDominant     = "database_dominant"
	FindingPromDominant   = "prometheus_dominant"
	FindingStatementCount = "many_statements"
)

// Limits are the figures the findings are measured against.
type Limits struct {
	// Gateway is the time the gateway in front of the platform waits (http.gateway_timeout_seconds); the browser's own limit is read from the examples.
	Gateway time.Duration
	// ListBudget is the time a read request may take before the platform stops it (http.list_query_seconds).
	ListBudget time.Duration
	// Pool is the wait of the connection pool of this process.
	PoolWaitCount int64
	PoolWaitMs    int64
	PoolMaxOpen   int
}

// minRequests is how many requests of a route are needed before a percentile says anything about it.
const minRequests = 5

func fromRow(r model.RequestStat) *Hour {
	h := &Hour{Hour: r.Hour, Method: r.Method, Route: r.Route, Count: r.Count, Status4xx: r.Status4xx, Status5xx: r.Status5xx, Timeouts: r.Timeouts, Abandoned: r.Abandoned,
		SumMs: r.SumMs, MaxMs: r.MaxMs, DBMs: r.DBMs, DBCalls: r.DBCalls, PromMs: r.PromMs, PromCalls: r.PromCalls, Buckets: make([]int64, BucketCount())}
	for i, part := range strings.Split(r.Buckets, ",") {
		if i < len(h.Buckets) {
			h.Buckets[i], _ = strconv.ParseInt(part, 10, 64)
		}
	}
	_ = json.Unmarshal([]byte(r.Examples), &h.Examples)
	return h
}

func toRow(bootID string, h Hour) model.RequestStat {
	parts := make([]string, len(h.Buckets))
	for i, n := range h.Buckets {
		parts[i] = strconv.FormatInt(n, 10)
	}
	examples, _ := json.Marshal(h.Examples)
	return model.RequestStat{Hour: h.Hour, BootID: bootID, Method: h.Method, Route: h.Route, Count: h.Count, Status4xx: h.Status4xx, Status5xx: h.Status5xx, Timeouts: h.Timeouts,
		Abandoned: h.Abandoned, SumMs: h.SumMs, MaxMs: h.MaxMs, DBMs: h.DBMs, DBCalls: h.DBCalls, PromMs: h.PromMs, PromCalls: h.PromCalls, Buckets: strings.Join(parts, ","), Examples: string(examples)}
}

// Summarize merges the stored hours by route and writes the baseline of each route.
func Summarize(rows []model.RequestStat) []RouteSummary {
	merged := map[routeKey]*Hour{}
	for _, r := range rows {
		h := fromRow(r)
		key := routeKey{r.Method, r.Route}
		if m := merged[key]; m != nil {
			m.Merge(h)
		} else {
			h.Hour = time.Time{}
			merged[key] = h
		}
	}
	out := make([]RouteSummary, 0, len(merged))
	for _, h := range merged {
		s := RouteSummary{Method: h.Method, Route: h.Route, Count: h.Count, Status4xx: h.Status4xx, Status5xx: h.Status5xx, Timeouts: h.Timeouts, Abandoned: h.Abandoned,
			P50: h.Percentile(0.50), P95: h.Percentile(0.95), P99: h.Percentile(0.99), MaxMs: h.MaxMs, Examples: h.Examples, Buckets: h.Buckets}
		if h.Count > 0 {
			s.AvgMs = h.SumMs / h.Count
			s.DBAvgMs = h.DBMs / h.Count
			s.PromAvgMs = h.PromMs / h.Count
			s.DBCalls = float64(h.DBCalls) / float64(h.Count)
			s.PromCalls = float64(h.PromCalls) / float64(h.Count)
		}
		if h.SumMs > 0 {
			s.DBShare = clamp01(float64(h.DBMs) / float64(h.SumMs))
			s.PromShare = clamp01(float64(h.PromMs) / float64(h.SumMs))
		}
		s.Layer = layerOf(s.DBShare, s.PromShare)
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].P95 != out[j].P95 {
			return out[i].P95 > out[j].P95
		}
		return out[i].Count > out[j].Count
	})
	return out
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	}
	return v
}

// layerOf says where the time of a route is: the layer that holds the largest share, the platform's own work when neither the database nor Prometheus holds
// as much as the rest.
func layerOf(db, prom float64) string {
	rest := 1 - db - prom
	switch {
	case db >= prom && db >= rest:
		return LayerDatabase
	case prom >= db && prom >= rest:
		return LayerPrometheus
	}
	return LayerHandler
}

const memoryNear = 0.85

// Diagnose writes down what the numbers say: which route is slow, which layer its time is in, and why a request ends in a 502 or a 504. Every finding
// has the figures it rests on. The most serious come first.
func Diagnose(routes []RouteSummary, boots []model.ProcessBoot, lim Limits) []Finding {
	var out []Finding
	var total int64
	for _, r := range routes {
		total += r.Count
	}
	if total < 20 {
		out = append(out, Finding{Level: "info", Code: FindingNoData, Params: map[string]any{"requests": total}})
	}
	gateway := lim.Gateway.Milliseconds()
	for _, r := range routes {
		if r.Timeouts > 0 {
			out = append(out, Finding{Level: "critical", Code: FindingPlatformStop, Route: r.Method + " " + r.Route, Layer: r.Layer, Params: map[string]any{
				"count": r.Timeouts, "requests": r.Count, "budget_s": int(lim.ListBudget / time.Second), "db_share": pct(r.DBShare), "prom_share": pct(r.PromShare), "p95_ms": r.P95}})
		}
		if r.Count >= minRequests && gateway > 0 {
			switch {
			case r.P95 >= gateway/2:
				out = append(out, Finding{Level: "critical", Code: FindingHalfGateway, Route: r.Method + " " + r.Route, Layer: r.Layer, Params: map[string]any{
					"p95_ms": r.P95, "max_ms": r.MaxMs, "gateway_s": int(lim.Gateway / time.Second), "db_share": pct(r.DBShare), "prom_share": pct(r.PromShare),
					"db_calls": round1(r.DBCalls), "prom_calls": round1(r.PromCalls), "count": r.Count}})
			case r.MaxMs >= gateway/2:
				out = append(out, Finding{Level: "warning", Code: FindingSlowRoute, Route: r.Method + " " + r.Route, Layer: r.Layer, Params: map[string]any{
					"p95_ms": r.P95, "max_ms": r.MaxMs, "gateway_s": int(lim.Gateway / time.Second), "db_share": pct(r.DBShare), "prom_share": pct(r.PromShare), "count": r.Count}})
			}
		}
		if r.Abandoned > 0 {
			code := FindingAbandoned
			params := map[string]any{"count": r.Abandoned, "requests": r.Count}
			for _, e := range r.Examples {
				if e.Abandoned {
					params["took_ms"] = e.Millis
					if e.Millis >= 55000 && e.Millis <= 66000 {
						code = FindingAbandoned60
					}
					break
				}
			}
			out = append(out, Finding{Level: "warning", Code: code, Route: r.Method + " " + r.Route, Layer: r.Layer, Params: params})
		}
		if other := r.Status5xx - r.Timeouts; other > 0 {
			out = append(out, Finding{Level: "warning", Code: FindingServerErrors, Route: r.Method + " " + r.Route, Params: map[string]any{"count": other, "requests": r.Count}})
		}
		if r.Count >= minRequests && r.DBCalls >= 50 {
			out = append(out, Finding{Level: "info", Code: FindingStatementCount, Route: r.Method + " " + r.Route, Layer: LayerDatabase, Params: map[string]any{"db_calls": round1(r.DBCalls)}})
		}
	}
	var killed, graceful int
	var killedAt time.Time
	for _, b := range boots {
		switch {
		case b.Unexpected:
			killed++
			if b.LastSeen.After(killedAt) {
				killedAt = b.LastSeen
			}
			if b.MemLimit > 0 && float64(b.SysBytes) >= memoryNear*float64(b.MemLimit) {
				out = append(out, Finding{Level: "critical", Code: FindingMemoryNear, Params: map[string]any{"instance": b.Instance, "sys_bytes": b.SysBytes, "limit_bytes": b.MemLimit, "at": b.LastSeen}})
			}
		case b.Graceful:
			graceful++
		}
	}
	if killed > 0 {
		out = append(out, Finding{Level: "critical", Code: FindingKilled, Params: map[string]any{"count": killed, "last": killedAt}})
	}
	if graceful > 0 {
		out = append(out, Finding{Level: "info", Code: FindingGraceful, Params: map[string]any{"count": graceful}})
	}
	if lim.PoolWaitCount >= 10 && lim.PoolWaitMs >= 500 {
		out = append(out, Finding{Level: "warning", Code: FindingPoolWaiting, Layer: LayerDatabase, Params: map[string]any{"waits": lim.PoolWaitCount, "wait_ms": lim.PoolWaitMs, "max_open": lim.PoolMaxOpen}})
	}
	rank := map[string]int{"critical": 0, "warning": 1, "info": 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Level] < rank[out[j].Level] })
	return out
}

func pct(v float64) int { return int(v*100 + 0.5) }

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
