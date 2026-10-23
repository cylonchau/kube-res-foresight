package app

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/app/middleware"
	"github.com/cylonchau/kubeforecast/pkg/app/query"
	"github.com/cylonchau/kubeforecast/pkg/app/router"
	"github.com/cylonchau/kubeforecast/pkg/config"
	"github.com/cylonchau/kubeforecast/pkg/logger"
	"github.com/cylonchau/kubeforecast/pkg/workflow"
	"github.com/gin-gonic/gin"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed all:router/dist
var staticFS embed.FS

// NewHTTPServer starts the management API and gracefully shuts it down when
// the command context is cancelled. workflowEngine is the shared
// *workflow.Engine constructed by the caller (pkg/cmd/server/server.go) —
// see router.RegisteredRouter's doc comment for why it's threaded through
// rather than built here.
func NewHTTPServer(ctx context.Context, workflowEngine *workflow.Engine) error {
	cfg := config.Get()
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	logger.Info("KubeForecast HTTP server listening", logger.String("addr", addr))

	// Initialize Gin
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(gin.Logger(), gin.Recovery(), middleware.CORS(cfg.Server.CORSAllowedOrigins), middleware.SlowRequests(3*time.Second))

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
		}

		c.Data(http.StatusOK, contentType, data)
	})

	// Register Routers
	router.RegisteredRouter(engine, workflowEngine)

	server := &http.Server{Addr: addr, Handler: engine}
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
		logger.Info("KubeForecast HTTP server shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
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
			logger.Warn("KubeForecast HTTP listener did not report shutdown in time")
		}
		return nil
	}
}
