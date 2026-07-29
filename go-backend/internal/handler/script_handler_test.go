package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"trpggame/internal/model"
	"trpggame/internal/service"
)

type fakeScriptUploadService struct {
	err          error
	script       *model.Script
	request      *service.UploadScriptRequest
	content      []byte
	listResult   *service.ListScriptsResult
	listErr      error
	listUserID   uint
	listPage     int
	listSize     int
	detailResult *service.ScriptDetailResult
	detailErr    error
	detailUserID uint
	detailID     uint
	deleteErr    error
	deleteUserID uint
	deleteID     uint
}

func (s *fakeScriptUploadService) Upload(
	_ context.Context,
	req *service.UploadScriptRequest,
) (*model.Script, error) {
	s.request = req
	if req.File != nil {
		content, err := io.ReadAll(req.File)
		if err != nil {
			return nil, err
		}
		s.content = content
	}
	return s.script, s.err
}

func (s *fakeScriptUploadService) List(
	userID uint,
	page,
	pageSize int,
) (*service.ListScriptsResult, error) {
	s.listUserID = userID
	s.listPage = page
	s.listSize = pageSize
	return s.listResult, s.listErr
}

func (s *fakeScriptUploadService) GetDetail(
	userID,
	scriptID uint,
) (*service.ScriptDetailResult, error) {
	s.detailUserID = userID
	s.detailID = scriptID
	return s.detailResult, s.detailErr
}

func (s *fakeScriptUploadService) Delete(
	_ context.Context,
	userID,
	scriptID uint,
) error {
	s.deleteUserID = userID
	s.deleteID = scriptID
	return s.deleteErr
}

func TestScriptHandlerUploadScript(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fakeService := &fakeScriptUploadService{
		script: &model.Script{
			ID:          42,
			Title:       "Haunted House",
			Description: "A mystery.",
			FileSize:    15,
			Status:      model.ScriptStatusParsing,
		},
	}
	handler := NewScriptHandler(fakeService, 1024)
	router := uploadTestRouter(handler, true)
	body, contentType := multipartUploadBody(
		t,
		"haunted.pdf",
		[]byte("%PDF-1.7\nsample"),
		"Haunted House",
		"A mystery.",
	)
	request := httptest.NewRequest(http.MethodPost, "/upload", body)
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	var response struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			ID       uint               `json:"id"`
			Title    string             `json:"title"`
			Status   model.ScriptStatus `json:"status"`
			FilePath string             `json:"file_path"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != 0 || response.Message != "accepted" {
		t.Fatalf("response = %#v", response)
	}
	if response.Data.ID != 42 || response.Data.Title != "Haunted House" ||
		response.Data.Status != model.ScriptStatusParsing {
		t.Fatalf("response data = %#v", response.Data)
	}
	if response.Data.FilePath != "" {
		t.Fatalf("response leaked internal file path: %q", response.Data.FilePath)
	}
	if fakeService.request == nil || fakeService.request.UserID != 7 {
		t.Fatalf("service request = %#v", fakeService.request)
	}
	if fakeService.request.Title != "Haunted House" ||
		fakeService.request.Description != "A mystery." {
		t.Fatalf("service fields = %#v", fakeService.request)
	}
	if !bytes.Equal(fakeService.content, []byte("%PDF-1.7\nsample")) {
		t.Fatalf("uploaded content = %q", fakeService.content)
	}
}

func TestScriptHandlerUploadScriptRequiresAuthenticationContext(t *testing.T) {
	handler := NewScriptHandler(&fakeScriptUploadService{}, 1024)
	router := uploadTestRouter(handler, false)
	body, contentType := multipartUploadBody(t, "test.pdf", []byte("%PDF-1.7"), "", "")
	request := httptest.NewRequest(http.MethodPost, "/upload", body)
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertJSONError(t, recorder, http.StatusUnauthorized, 1002)
}

func TestScriptHandlerUploadScriptRequiresFile(t *testing.T) {
	handler := NewScriptHandler(&fakeScriptUploadService{}, 1024)
	router := uploadTestRouter(handler, true)
	request := httptest.NewRequest(
		http.MethodPost,
		"/upload",
		strings.NewReader("title=missing"),
	)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertJSONError(t, recorder, http.StatusBadRequest, 1200)
}

func TestScriptHandlerUploadScriptMapsValidationErrors(t *testing.T) {
	tests := []struct {
		name       string
		serviceErr error
		wantStatus int
		wantCode   int
	}{
		{
			name:       "file too large",
			serviceErr: service.ErrScriptFileTooLarge,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   1201,
		},
		{
			name:       "invalid PDF",
			serviceErr: fmt.Errorf("%w: sensitive parser detail", service.ErrScriptFileSignature),
			wantStatus: http.StatusBadRequest,
			wantCode:   1202,
		},
		{
			name:       "title too long",
			serviceErr: service.ErrScriptTitleTooLong,
			wantStatus: http.StatusBadRequest,
			wantCode:   1202,
		},
		{
			name:       "internal failure",
			serviceErr: fmt.Errorf("%w: database unavailable", service.ErrInternal),
			wantStatus: http.StatusInternalServerError,
			wantCode:   1203,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeService := &fakeScriptUploadService{err: tt.serviceErr}
			handler := NewScriptHandler(fakeService, 1024)
			router := uploadTestRouter(handler, true)
			body, contentType := multipartUploadBody(t, "test.pdf", []byte("%PDF-1.7"), "", "")
			request := httptest.NewRequest(http.MethodPost, "/upload", body)
			request.Header.Set("Content-Type", contentType)
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, request)

			assertJSONError(t, recorder, tt.wantStatus, tt.wantCode)
			if strings.Contains(recorder.Body.String(), "sensitive parser detail") {
				t.Fatalf("response leaked wrapped error detail: %s", recorder.Body.String())
			}
		})
	}
}

func TestScriptHandlerUploadScriptRejectsEmptyServiceResult(t *testing.T) {
	handler := NewScriptHandler(&fakeScriptUploadService{}, 1024)
	router := uploadTestRouter(handler, true)
	body, contentType := multipartUploadBody(t, "test.pdf", []byte("%PDF-1.7"), "", "")
	request := httptest.NewRequest(http.MethodPost, "/upload", body)
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertJSONError(t, recorder, http.StatusInternalServerError, 1203)
}

func TestScriptHandlerUploadScriptRejectsOversizedRequestBody(t *testing.T) {
	fakeService := &fakeScriptUploadService{}
	handler := NewScriptHandler(fakeService, 8)
	router := uploadTestRouter(handler, true)
	content := append([]byte("%PDF-"), bytes.Repeat([]byte("x"), int(multipartOverheadAllowance)+32)...)
	body, contentType := multipartUploadBody(t, "large.pdf", content, "", "")
	request := httptest.NewRequest(http.MethodPost, "/upload", body)
	request.Header.Set("Content-Type", contentType)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertJSONError(t, recorder, http.StatusRequestEntityTooLarge, 1201)
	if fakeService.request != nil {
		t.Fatal("oversized request reached service")
	}
}

func TestScriptHandlerListScripts(t *testing.T) {
	fakeService := &fakeScriptUploadService{
		listResult: &service.ListScriptsResult{
			Items: []model.Script{{
				ID:         42,
				UserID:     7,
				Title:      "Haunted House",
				FilePath:   "scripts/7/42/private.pdf",
				Status:     model.ScriptStatusFailed,
				ParseError: "no extractable text",
			}},
			Total:    6,
			Page:     2,
			PageSize: 5,
		},
	}
	handler := NewScriptHandler(fakeService, 1024)
	router := listTestRouter(handler, true)
	request := httptest.NewRequest(http.MethodGet, "/scripts?page=2&page_size=5", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if fakeService.listUserID != 7 || fakeService.listPage != 2 || fakeService.listSize != 5 {
		t.Fatalf(
			"service query = (user=%d, page=%d, size=%d)",
			fakeService.listUserID,
			fakeService.listPage,
			fakeService.listSize,
		)
	}
	var response struct {
		Data struct {
			Items []map[string]any `json:"items"`
			Total int64            `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data.Items) != 1 || response.Data.Total != 6 {
		t.Fatalf("response data = %#v", response.Data)
	}
	if _, exists := response.Data.Items[0]["file_path"]; exists {
		t.Fatalf("response leaked internal file path: %#v", response.Data.Items[0])
	}
	if response.Data.Items[0]["parse_error"] != "no extractable text" {
		t.Fatalf("parse error missing from response: %#v", response.Data.Items[0])
	}
}

func TestScriptHandlerListScriptsRejectsInvalidPagination(t *testing.T) {
	fakeService := &fakeScriptUploadService{}
	handler := NewScriptHandler(fakeService, 1024)
	router := listTestRouter(handler, true)
	request := httptest.NewRequest(http.MethodGet, "/scripts?page=invalid", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertJSONError(t, recorder, http.StatusBadRequest, 1204)
	if fakeService.listUserID != 0 {
		t.Fatal("invalid pagination reached service")
	}
}

func TestScriptHandlerListScriptsRequiresAuthenticationContext(t *testing.T) {
	handler := NewScriptHandler(&fakeScriptUploadService{}, 1024)
	router := listTestRouter(handler, false)
	request := httptest.NewRequest(http.MethodGet, "/scripts", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertJSONError(t, recorder, http.StatusUnauthorized, 1002)
}

func TestScriptHandlerListScriptsHandlesServiceFailure(t *testing.T) {
	fakeService := &fakeScriptUploadService{
		listErr: fmt.Errorf("%w: mysql unavailable", service.ErrInternal),
	}
	handler := NewScriptHandler(fakeService, 1024)
	router := listTestRouter(handler, true)
	request := httptest.NewRequest(http.MethodGet, "/scripts", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertJSONError(t, recorder, http.StatusInternalServerError, 1203)
}

func TestScriptHandlerGetScriptDetail(t *testing.T) {
	fakeService := &fakeScriptUploadService{
		detailResult: &service.ScriptDetailResult{
			Script: &model.Script{
				ID:       42,
				UserID:   7,
				Title:    "Haunted House",
				FilePath: "scripts/7/42/private.pdf",
				Status:   model.ScriptStatusReady,
			},
			Characters: []model.ScriptCharacter{{
				ID:          3,
				ScriptID:    42,
				Name:        "Investigator",
				Description: "A careful observer.",
				Attributes:  `{"hp":10,"san":60}`,
			}},
		},
	}
	handler := NewScriptHandler(fakeService, 1024)
	router := detailTestRouter(handler, true)
	request := httptest.NewRequest(http.MethodGet, "/scripts/42", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if fakeService.detailUserID != 7 || fakeService.detailID != 42 {
		t.Fatalf("detail query = (user=%d, script=%d)", fakeService.detailUserID, fakeService.detailID)
	}
	var response struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, exists := response.Data["file_path"]; exists {
		t.Fatalf("response leaked internal file path: %#v", response.Data)
	}
	characters, ok := response.Data["characters"].([]any)
	if !ok || len(characters) != 1 {
		t.Fatalf("characters = %#v", response.Data["characters"])
	}
	character := characters[0].(map[string]any)
	attributes, ok := character["attributes"].(map[string]any)
	if !ok || attributes["hp"] != float64(10) {
		t.Fatalf("attributes = %#v", character["attributes"])
	}
}

func TestScriptHandlerGetScriptDetailRejectsInvalidID(t *testing.T) {
	fakeService := &fakeScriptUploadService{}
	handler := NewScriptHandler(fakeService, 1024)
	router := detailTestRouter(handler, true)
	request := httptest.NewRequest(http.MethodGet, "/scripts/not-a-number", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertJSONError(t, recorder, http.StatusBadRequest, 1205)
	if fakeService.detailUserID != 0 {
		t.Fatal("invalid script ID reached service")
	}
}

func TestScriptHandlerGetScriptDetailMapsNotFound(t *testing.T) {
	fakeService := &fakeScriptUploadService{detailErr: service.ErrScriptNotFound}
	handler := NewScriptHandler(fakeService, 1024)
	router := detailTestRouter(handler, true)
	request := httptest.NewRequest(http.MethodGet, "/scripts/42", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertJSONError(t, recorder, http.StatusNotFound, 1206)
}

func TestScriptHandlerDeleteScript(t *testing.T) {
	fakeService := &fakeScriptUploadService{}
	handler := NewScriptHandler(fakeService, 1024)
	router := deleteTestRouter(handler, true)
	request := httptest.NewRequest(http.MethodDelete, "/scripts/42", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if fakeService.deleteUserID != 7 || fakeService.deleteID != 42 {
		t.Fatalf("delete call = (user=%d, script=%d)", fakeService.deleteUserID, fakeService.deleteID)
	}
}

func TestScriptHandlerDeleteScriptRejectsInvalidID(t *testing.T) {
	fakeService := &fakeScriptUploadService{}
	handler := NewScriptHandler(fakeService, 1024)
	router := deleteTestRouter(handler, true)
	request := httptest.NewRequest(http.MethodDelete, "/scripts/invalid", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertJSONError(t, recorder, http.StatusBadRequest, 1205)
	if fakeService.deleteUserID != 0 {
		t.Fatal("invalid script ID reached service")
	}
}

func TestScriptHandlerDeleteScriptMapsNotFound(t *testing.T) {
	fakeService := &fakeScriptUploadService{deleteErr: service.ErrScriptNotFound}
	handler := NewScriptHandler(fakeService, 1024)
	router := deleteTestRouter(handler, true)
	request := httptest.NewRequest(http.MethodDelete, "/scripts/42", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertJSONError(t, recorder, http.StatusNotFound, 1206)
}

func TestScriptHandlerDeleteScriptHandlesStorageFailure(t *testing.T) {
	fakeService := &fakeScriptUploadService{
		deleteErr: fmt.Errorf("%w: minio unavailable", service.ErrInternal),
	}
	handler := NewScriptHandler(fakeService, 1024)
	router := deleteTestRouter(handler, true)
	request := httptest.NewRequest(http.MethodDelete, "/scripts/42", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	assertJSONError(t, recorder, http.StatusInternalServerError, 1203)
}

func uploadTestRouter(handler *ScriptHandler, authenticated bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/upload", func(c *gin.Context) {
		if authenticated {
			c.Set("user_id", uint(7))
		}
		handler.UploadScript(c)
	})
	return router
}

func listTestRouter(handler *ScriptHandler, authenticated bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/scripts", func(c *gin.Context) {
		if authenticated {
			c.Set("user_id", uint(7))
		}
		handler.ListScripts(c)
	})
	return router
}

func detailTestRouter(handler *ScriptHandler, authenticated bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/scripts/:id", func(c *gin.Context) {
		if authenticated {
			c.Set("user_id", uint(7))
		}
		handler.GetScriptDetail(c)
	})
	return router
}

func deleteTestRouter(handler *ScriptHandler, authenticated bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.DELETE("/scripts/:id", func(c *gin.Context) {
		if authenticated {
			c.Set("user_id", uint(7))
		}
		handler.DeleteScript(c)
	})
	return router
}

func multipartUploadBody(
	t *testing.T,
	filename string,
	content []byte,
	title string,
	description string,
) (*bytes.Buffer, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	headers := make(textproto.MIMEHeader)
	headers.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, filename))
	headers.Set("Content-Type", "application/pdf")
	part, err := writer.CreatePart(headers)
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if title != "" {
		if err := writer.WriteField("title", title); err != nil {
			t.Fatalf("write title: %v", err)
		}
	}
	if description != "" {
		if err := writer.WriteField("description", description); err != nil {
			t.Fatalf("write description: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	return body, writer.FormDataContentType()
}

func assertJSONError(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus, wantCode int) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, wantStatus, recorder.Body.String())
	}
	var response struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if response.Code != wantCode {
		t.Fatalf("code = %d, want %d; body = %s", response.Code, wantCode, recorder.Body.String())
	}
}
