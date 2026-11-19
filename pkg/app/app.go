package app

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"github.com/cylonchau/kube-res-foresight/pkg/app/middleware"
	"github.com/cylonchau/kube-res-foresight/pkg/app/query"
	"github.com/cylonchau/kube-res-foresight/pkg/app/router"
	"github.com/cylonchau/kube-res-foresight/pkg/config"
	"github.com/cylonchau/kube-res-foresight/pkg/dao/rdb"
	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/cylonchau/kube-res-foresight/pkg/model"
	"github.com/cylonchau/kube-res-foresight/pkg/reqstat"
	"github.com/cylonchau/kube-res-foresight/pkg/timing"
	"github.com/cylonchau/kube-res-foresight/pkg/version"
	"github.com/gin-gonic/gin"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed all:router/dist
var staticFS embed.FS

// NewHTTPServer starts the management API and gracefully shuts it down when the command context is cancelled.
func NewHTTPServer(ctx context.Context) error {
	cfg := config.Get()
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	logger.Info("kube-res-foresight HTTP server listening", logger.String("addr", addr))

	// the record of the runs of this process and the statistics of its requests (P11 B0): what the diagnostics say about a 502 or a 504 is read from here
	stats := startRequestStats(ctx)
	defer stats.Stop()

	// Initialize Gin
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(gin.Logger(), gin.Recovery(), middleware.CORS(cfg.Server.CORSAllowedOrigins), middleware.SlowRequests(slowRequestLimit()), middleware.Measure(reqstat.DefaultRecorder()), middleware.ReadDeadline(readBudget()), middleware.PageDepth(20000))

	// Custom 404/Static Handler
	engine.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path
		if strings.HasPrefix(path, "/api/") {
			c.JSON(404, gin.H{"code": 404, "msg": "API Not Found"})
			return
		}

		if path == "/" || path == "" {
			path = "/index.html"
		}

		subFS, _ := fs.Sub(staticFS, "router/dist")
		filePath := strings.TrimPrefix(path, "/")

		data, err := fs.ReadFile(subFS, filePath)
		if err != nil {
			// Fallback to index.html for Vue SPA History Mode
			dataIndex, errIndex := fs.ReadFile(subFS, "index.html")
			if errIndex != nil {
				query.ErrorResponse(c, http.StatusNotFound, query.ErrRouteNotFound)
				return
			}
			c.Data(http.StatusOK, "text/html; charset=utf-8", dataIndex)
			return
		}

		contentType := "application/octet-stream"
		if strings.HasSuffix(filePath, ".html") {
			contentType = "text/html; charset=utf-8"
		} else if strings.HasSuffix(filePath, ".js") {
			contentType = "application/javascript"
		} else if strings.HasSuffix(filePath, ".css") {
			contentType = "text/css"
		} else if strings.HasSuffix(filePath, ".svg") {
			contentType = "image/svg+xml"
		} else if strings.HasSuffix(filePath, ".png") {
			contentType = "image/png"
		} else if strings.HasSuffix(filePath, ".ico") {
			contentType = "image/x-icon"
		}

		c.Data(http.StatusOK, contentType, data)
	})

	// Register Routers
	router.RegisteredRouter(engine)

	// the limits of a connection that is not working: a client that sends its headers slowly does not hold a connection for ever, and an idle one is closed after longer
	// than the idle time of the gateways in front (a proxy that keeps connections open for a minute would otherwise find the platform has closed one it is about to use,
	// which is a 502). There is no read or write time-out of the whole request: the time of a request is bounded by its own deadline (see middleware.ReadDeadline).
	server := &http.Server{Addr: addr, Handler: engine, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 3 * time.Minute}
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("kube-res-foresight HTTP server shutting down")
		// a request that is running gets the time a read request may take (30 seconds by default) to finish before the process goes: a rolling update does not
		// cut the page that was being answered
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownDrain)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("gracefully shut down HTTP server: %w", err)
		}
		// Shutdown has already closed listeners and drained active requests.
		// Do not let process termination hang forever if ListenAndServe is
		// slow to publish its terminal result (notably possible when a
		// listener is cancelled during startup).
		select {
		case err := <-serveErrors:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
		case <-time.After(time.Second):
			logger.Warn("kube-res-foresight HTTP listener did not report shutdown in time")
		}
		return nil
	}
}

// slowRequestLimit reads the slow request threshold from the system parameter on every request (the value is cached for a few seconds by the
// config DAO), so a change is seen without a restart. Before the database is open it is the default.
func slowRequestLimit() func() time.Duration {
	var once sync.Once
	var dao *rdb.SysConfigDAO
	fallback := time.Duration(timing.HTTPSlowRequest.Default) * time.Second
	return func() time.Duration {
		if model.DB == nil {
			return fallback
		}
		once.Do(func() { dao = rdb.NewSysConfigDAO(model.DB) })
		raw, err := dao.GetValueOrDefault(context.Background(), timing.HTTPSlowRequest.Key, "")
		if err != nil {
			return fallback
		}
		return time.Duration(timing.HTTPSlowRequest.Parse(raw)) * time.Second
	}
}

// readBudget is the time a read request may take: the system parameter, kept under the gateway timeout (also a system parameter), both read on
// every request. Before the database is open it is the default.
func readBudget() func() time.Duration {
	var once sync.Once
	var dao *rdb.SysConfigDAO
	value := func(p timing.Param) time.Duration {
		if model.DB == nil {
			return time.Duration(p.Default) * time.Second
		}
		once.Do(func() { dao = rdb.NewSysConfigDAO(model.DB) })
		raw, err := dao.GetValueOrDefault(context.Background(), p.Key, "")
		if err != nil {
			return time.Duration(p.Default) * time.Second
		}
		return time.Duration(p.Parse(raw)) * time.Second
	}
	return func() time.Duration {
		return timing.Under(value(timing.HTTPGatewayTimeout), value(timing.ListQuery))
	}
}

// shutdownDrain is how long the server waits for the requests that are running when it is told to stop. The pod is given more time than this
// (terminationGracePeriodSeconds of the chart) so the process ends by itself and is not killed.
const shutdownDrain = 40 * time.Second

// startRequestStats writes this run of the process and starts storing the request statistics. Without a database there is nothing to store them in, and the
// diagnostics report that.
func startRequestStats(ctx context.Context) *reqstat.Service {
	if model.DB == nil {
		return nil
	}
	svc := reqstat.New(rdb.NewRequestStatDAO(model.DB), rdb.NewProcessBootDAO(model.DB), reqstat.DefaultRecorder(), version.Version)
	reqstat.SetDefault(svc)
	svc.Start(ctx)
	return svc
}
