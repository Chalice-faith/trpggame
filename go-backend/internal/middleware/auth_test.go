package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCORSAllowsPatchPreflight(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORS())
	router.PATCH("/groups/:groupId/members/:userId", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodOptions, "/groups/4/members/24", nil)
	request.Header.Set("Origin", "http://127.0.0.1:15173")
	request.Header.Set("Access-Control-Request-Method", http.MethodPatch)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if response.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("origin header = %q, want *", response.Header().Get("Access-Control-Allow-Origin"))
	}
	if !strings.Contains(response.Header().Get("Access-Control-Allow-Methods"), http.MethodPatch) {
		t.Fatalf("PATCH missing from allowed methods: %q", response.Header().Get("Access-Control-Allow-Methods"))
	}
}
