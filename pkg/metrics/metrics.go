// Package metrics exposes Prometheus metrics and a health endpoint for the
// standalone kube-res-foresight processes. Collectors register on the shared
// registry; a module serves it only when metrics.listen is configured.
package metrics

import (
	"context"
	"errors"
	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"net/http"
	"time"
)

// Registry is the process-wide registry (Go runtime + process collectors included).
var Registry = newRegistry()

func newRegistry() *prometheus.Registry {
	r := prometheus.NewRegistry()
	r.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
	return r
}

// Factory registers collectors on Registry.
var Factory = newFactory()

func newFactory() promAuto { return promAuto{} }

type promAuto struct{}

func (promAuto) Counter(name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "kube_res_foresight", Name: name, Help: help}, labels)
	Registry.MustRegister(c)
	return c
}

func (promAuto) Gauge(name, help string, labels ...string) *prometheus.GaugeVec {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: "kube_res_foresight", Name: name, Help: help}, labels)
	Registry.MustRegister(g)
	return g
}

func (promAuto) GaugeFunc(name, help string, fn func() float64) {
	Registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "kube_res_foresight", Name: name, Help: help}, fn))
}

// Handler serves /metrics, /healthz and /readyz.
func Handler(ready func() error) http.Handler {
	mux := http.NewServeMux()
	var gatherer prometheus.Gatherer = Registry
	if LegacyNames {
		gatherer = withLegacyNames(Registry)
	}
	mux.Handle("/metrics", promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))
	// /healthz (liveness) says the process answers: it does not ask the database. A database that is slow or out of connections must not make the
	// platform look dead: the kubelet restarts a Pod that fails its liveness probe, and a restart is a 502 for every request in flight, on top of the slowness
	// that was the problem. /readyz (readiness) asks, and a Pod that cannot reach its database is taken out of the Service until it can.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if ready != nil {
			if err := ready(); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

// Serve runs the endpoints on addr until ctx ends. An empty addr disables it.
func Serve(ctx context.Context, addr string, ready func() error) {
	if addr == "" {
		return
	}
	srv := &http.Server{Addr: addr, Handler: Handler(ready), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	go func() {
		logger.Info("metrics endpoint listening", logger.String("addr", addr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics endpoint stopped", logger.String("error", err.Error()))
		}
	}()
}
