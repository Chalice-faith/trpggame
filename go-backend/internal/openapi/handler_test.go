package openapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRegisterRoutesServesEmbeddedSpecification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	RegisterRoutes(engine)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, SpecPath, nil)
	engine.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/yaml") {
		t.Fatalf("Content-Type = %q, want application/yaml", contentType)
	}
	body := response.Body.String()
	if !strings.Contains(body, "openapi: 3.0.3") ||
		!strings.Contains(body, "/api/v1/games/{roomId}/action:") {
		t.Fatal("embedded specification is incomplete")
	}
	if strings.Contains(body, "/api/v1/internal/") {
		t.Fatal("public specification must not expose internal routes")
	}
}

func TestRegisterRoutesServesSwaggerUI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	RegisterRoutes(engine)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/docs/index.html", nil)
	engine.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", contentType)
	}
	body := response.Body.String()
	escapedSpecPath := strings.ReplaceAll(SpecPath, "/", `\/`)
	if !strings.Contains(body, "SwaggerUIBundle") || !strings.Contains(body, escapedSpecPath) {
		t.Fatal("Swagger UI does not reference the embedded OpenAPI specification")
	}

	assetResponse := httptest.NewRecorder()
	assetRequest := httptest.NewRequest(
		http.MethodGet,
		"/api/docs/swagger-ui-bundle.js",
		nil,
	)
	engine.ServeHTTP(assetResponse, assetRequest)
	if assetResponse.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want %d", assetResponse.Code, http.StatusOK)
	}
	if contentType := assetResponse.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/javascript") {
		t.Fatalf("asset Content-Type = %q, want application/javascript", contentType)
	}
}
