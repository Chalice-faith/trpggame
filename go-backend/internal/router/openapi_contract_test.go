package router

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"

	"trpggame/internal/config"
	"trpggame/internal/handler"
	"trpggame/internal/openapi"
)

var openAPIOperationMethods = map[string]struct{}{
	"delete":  {},
	"get":     {},
	"head":    {},
	"options": {},
	"patch":   {},
	"post":    {},
	"put":     {},
	"trace":   {},
}

type openAPIContractDocument struct {
	Paths map[string]map[string]yaml.Node `yaml:"paths"`
}

func TestPublicRESTRoutesMatchOpenAPIContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := Setup(
		&config.Config{
			JWT:      config.JWTConfig{Secret: "contract-test-secret", AccessTokenTTL: 15},
			Internal: config.InternalConfig{SharedSecret: "internal-test-secret"},
		},
		nil,
		nil,
		nil,
		nil,
		handler.NewGameHandler(&routerGameStartService{}),
	)

	actual := make(map[string]struct{})
	for _, route := range engine.Routes() {
		if !strings.HasPrefix(route.Path, "/api/v1/") ||
			strings.HasPrefix(route.Path, "/api/v1/internal/") {
			continue
		}
		actual[route.Method+" "+normalizeGinPath(route.Path)] = struct{}{}
	}

	var document openAPIContractDocument
	if err := yaml.Unmarshal(openapi.Specification(), &document); err != nil {
		t.Fatalf("parse embedded OpenAPI specification: %v", err)
	}

	expected := make(map[string]struct{})
	for path, pathItem := range document.Paths {
		for method := range pathItem {
			method = strings.ToLower(method)
			if _, ok := openAPIOperationMethods[method]; !ok {
				continue
			}
			expected[strings.ToUpper(method)+" "+path] = struct{}{}
		}
	}

	missingFromContract := difference(actual, expected)
	missingFromRouter := difference(expected, actual)
	if len(missingFromContract) != 0 || len(missingFromRouter) != 0 {
		t.Fatalf(
			"public REST routes and OpenAPI contract differ\nmissing from contract: %s\nmissing from router: %s",
			formatRouteDifference(missingFromContract),
			formatRouteDifference(missingFromRouter),
		)
	}
}

func normalizeGinPath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, ":") {
			segments[i] = "{" + strings.TrimPrefix(segment, ":") + "}"
		}
	}
	return strings.Join(segments, "/")
}

func difference(left, right map[string]struct{}) []string {
	difference := make([]string, 0)
	for route := range left {
		if _, ok := right[route]; !ok {
			difference = append(difference, route)
		}
	}
	sort.Strings(difference)
	return difference
}

func formatRouteDifference(routes []string) string {
	if len(routes) == 0 {
		return "none"
	}
	return fmt.Sprintf("[%s]", strings.Join(routes, ", "))
}
