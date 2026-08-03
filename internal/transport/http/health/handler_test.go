package health

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCheck(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	router := gin.New()
	router.GET("/healthz", Check)

	router.ServeHTTP(recorder, request)

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Errorf("状态码 = %d，期望 %d", got, want)
	}
	if got, want := recorder.Body.String(), "{\"code\":0,\"message\":\"success\",\"data\":{\"status\":\"ok\"}}"; got != want {
		t.Errorf("响应体 = %q，期望 %q", got, want)
	}
}
