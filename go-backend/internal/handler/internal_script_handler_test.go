package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"trpggame/internal/middleware"
	"trpggame/internal/model"
	"trpggame/internal/service"
)

type fakeParseStatusService struct {
	err          error
	calls        int
	scriptID     uint
	status       model.ScriptStatus
	errorMessage string
	chunkCount   int
}

func (s *fakeParseStatusService) UpdateParseResult(
	scriptID uint,
	status model.ScriptStatus,
	errorMessage string,
	chunkCount int,
) error {
	s.calls++
	s.scriptID = scriptID
	s.status = status
	s.errorMessage = errorMessage
	s.chunkCount = chunkCount
	return s.err
}

func TestInternalScriptStatusCallback(t *testing.T) {
	fakeService := &fakeParseStatusService{}
	router := internalStatusTestRouter(fakeService, "test-secret")
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/internal/scripts/42/status",
		strings.NewReader(`{"status":"ready","chunk_count":7,"error_message":""}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Internal-Secret", "test-secret")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", recorder.Code, recorder.Body.String())
	}
	if fakeService.calls != 1 ||
		fakeService.scriptID != 42 ||
		fakeService.status != model.ScriptStatusReady ||
		fakeService.chunkCount != 7 {
		t.Fatalf("service call = %#v", fakeService)
	}
}

func TestInternalScriptStatusRejectsInvalidSecret(t *testing.T) {
	fakeService := &fakeParseStatusService{}
	router := internalStatusTestRouter(fakeService, "test-secret")
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/internal/scripts/42/status",
		strings.NewReader(`{"status":"ready","chunk_count":7}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Internal-Secret", "wrong-secret")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertJSONError(t, recorder, http.StatusUnauthorized, 1212)
	if fakeService.calls != 0 {
		t.Fatal("unauthorized request reached service")
	}
}

func TestInternalScriptStatusRejectsInvalidPayload(t *testing.T) {
	fakeService := &fakeParseStatusService{}
	router := internalStatusTestRouter(fakeService, "test-secret")
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/internal/scripts/42/status",
		strings.NewReader(`{"chunk_count":"invalid"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Internal-Secret", "test-secret")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertJSONError(t, recorder, http.StatusBadRequest, 1210)
	if fakeService.calls != 0 {
		t.Fatal("invalid payload reached service")
	}
}

func TestInternalScriptStatusMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		serviceErr error
		wantStatus int
		wantCode   int
	}{
		{"invalid result", service.ErrInvalidParseResult, http.StatusBadRequest, 1210},
		{"missing script", service.ErrScriptNotFound, http.StatusNotFound, 1206},
		{"status conflict", service.ErrScriptStatusConflict, http.StatusConflict, 1211},
		{"database failure", errors.New("database unavailable"), http.StatusInternalServerError, 1203},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeService := &fakeParseStatusService{err: tt.serviceErr}
			router := internalStatusTestRouter(fakeService, "test-secret")
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/internal/scripts/42/status",
				strings.NewReader(`{"status":"failed","chunk_count":0,"error_message":"解析失败"}`),
			)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Internal-Secret", "test-secret")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, tt.wantStatus, tt.wantCode)
		})
	}
}

func internalStatusTestRouter(
	statusService ScriptParseStatusService,
	sharedSecret string,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	internalHandler := NewInternalScriptHandler(statusService)
	router.POST(
		"/api/v1/internal/scripts/:id/status",
		middleware.InternalAuth(sharedSecret),
		internalHandler.UpdateStatus,
	)
	return router
}
