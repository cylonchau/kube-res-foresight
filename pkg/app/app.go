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
	"github.com/cylonchau/kube-res-foresight/pkg/logger"
	"github.com/gin-gonic/gin"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed all:router/dist
var staticFS embed.FS

// NewHTTPServer starts the management API and gracefully shuts it down when the command context is cancelled.
func NewHTTPServer(ctx context.Context) error {
	cfg := config.Get()
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	logger.Info("kube-res-foresight HTTP server listening", logger.String("addr", addr))

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
		} else if strings.HasSuffix(filePath, ".ico") {
			contentType = "image/x-icon"
		}

		c.Data(http.StatusOK, contentType, data)
	})

	// Register Routers
	router.RegisteredRouter(engine)

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
		logger.Info("kube-res-foresight HTTP server shutting down")
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
			logger.Warn("kube-res-foresight HTTP listener did not report shutdown in time")
		}
		return nil
	}
}
