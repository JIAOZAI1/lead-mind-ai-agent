package http

import (
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestNewRouterRegistersBusinessRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	routes := NewRouter(nil, logger).Routes()

	assertRouteRegistered(t, routes, http.MethodGet, "/healthz")
	assertRouteRegistered(t, routes, http.MethodPost, "/ai-agent/chat")
}

func assertRouteRegistered(t *testing.T, routes gin.RoutesInfo, method, path string) {
	t.Helper()

	for _, route := range routes {
		if route.Method == method && route.Path == path {
			return
		}
	}

	t.Errorf("未注册路由 %s %s", method, path)
}
