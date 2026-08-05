package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"gorm.io/gorm"

	"trpggame/internal/ai_client"
	"trpggame/internal/config"
	"trpggame/internal/model"
)

type fakeScriptRepository struct {
	createErr      error
	updateFileErr  error
	statusErr      error
	created        *model.Script
	filePath       string
	fileSize       int64
	statuses       []model.ScriptStatus
	statusErrors   []string
	retryUpdated   bool
	retryErr       error
	retryID        uint
	retryUserID    uint
	parseUpdated   bool
	parseStatus    model.ScriptStatus
	parseError     string
	parseChunks    int
	parseUpdateErr error
	scriptByID     *model.Script
	scriptByIDErr  error
	findItems      []model.Script
	findTotal      int64
	findErr        error
	findUserID     uint
	findOffset     int
	findLimit      int
	detailScript   *model.Script
	detailErr      error
	detailID       uint
	detailUserID   uint
	characters     []model.ScriptCharacter
	charactersErr  error
	deleteErr      error
	deletedID      uint
}

func (r *fakeScriptRepository) Create(script *model.Script) error {
	if r.createErr != nil {
		return r.createErr
	}
	script.ID = 42
	copied := *script
	r.created = &copied
	return nil
}

func (r *fakeScriptRepository) FindByID(_ uint) (*model.Script, error) {
	return r.scriptByID, r.scriptByIDErr
}

func (r *fakeScriptRepository) FindByUserID(
	userID uint,
	offset,
	limit int,
) ([]model.Script, int64, error) {
	r.findUserID = userID
	r.findOffset = offset
	r.findLimit = limit
	return r.findItems, r.findTotal, r.findErr
}

func (r *fakeScriptRepository) FindByIDAndUserID(id, userID uint) (*model.Script, error) {
	r.detailID = id
	r.detailUserID = userID
	return r.detailScript, r.detailErr
}

func (r *fakeScriptRepository) FindCharactersByScriptID(_ uint) ([]model.ScriptCharacter, error) {
	return r.characters, r.charactersErr
}

func (r *fakeScriptRepository) UpdateFile(_ uint, filePath string, fileSize int64) error {
	if r.updateFileErr != nil {
		return r.updateFileErr
	}
	r.filePath = filePath
	r.fileSize = fileSize
	return nil
}

func (r *fakeScriptRepository) UpdateStatus(_ uint, status model.ScriptStatus, errMsg string) error {
	r.statuses = append(r.statuses, status)
	r.statusErrors = append(r.statusErrors, errMsg)
	return r.statusErr
}

func (r *fakeScriptRepository) BeginRetry(id, userID uint) (bool, error) {
	r.retryID = id
	r.retryUserID = userID
	return r.retryUpdated, r.retryErr
}

func (r *fakeScriptRepository) UpdateParseResult(
	_ uint,
	status model.ScriptStatus,
	errMsg string,
	chunkCount int,
) (bool, error) {
	r.parseStatus = status
	r.parseError = errMsg
	r.parseChunks = chunkCount
	return r.parseUpdated, r.parseUpdateErr
}

func (r *fakeScriptRepository) Delete(id uint) error {
	r.deletedID = id
	return r.deleteErr
}

func TestScriptServiceUpdateParseResult(t *testing.T) {
	repository := &fakeScriptRepository{parseUpdated: true}
	svc := newScriptServiceForTest(repository, &fakeScriptStorage{}, &fakeScriptParser{})

	err := svc.UpdateParseResult(42, model.ScriptStatusReady, "", 6)

	if err != nil {
		t.Fatalf("UpdateParseResult() error = %v", err)
	}
	if repository.parseStatus != model.ScriptStatusReady ||
		repository.parseError != "" ||
		repository.parseChunks != 6 {
		t.Fatalf(
			"parse update = (%q, %q, %d)",
			repository.parseStatus,
			repository.parseError,
			repository.parseChunks,
		)
	}
}

func TestScriptServiceUpdateParseResultValidatesTerminalResult(t *testing.T) {
	tests := []struct {
		name       string
		status     model.ScriptStatus
		errorText  string
		chunkCount int
		wantErr    error
	}{
		{"unsupported status", model.ScriptStatusParsing, "", 0, ErrInvalidScriptStatus},
		{"ready without chunks", model.ScriptStatusReady, "", 0, ErrInvalidParseResult},
		{"ready with error", model.ScriptStatusReady, "unexpected", 2, ErrInvalidParseResult},
		{"failed without error", model.ScriptStatusFailed, "", 0, ErrInvalidParseResult},
		{"failed with chunks", model.ScriptStatusFailed, "failed", 1, ErrInvalidParseResult},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repository := &fakeScriptRepository{}
			svc := newScriptServiceForTest(
				repository,
				&fakeScriptStorage{},
				&fakeScriptParser{},
			)

			err := svc.UpdateParseResult(42, tt.status, tt.errorText, tt.chunkCount)

			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if repository.parseStatus != "" {
				t.Fatalf("invalid result reached repository: %q", repository.parseStatus)
			}
		})
	}
}

func TestScriptServiceUpdateParseResultIsIdempotent(t *testing.T) {
	repository := &fakeScriptRepository{
		scriptByID: &model.Script{
			ID:         42,
			Status:     model.ScriptStatusFailed,
			ParseError: "PDF 解析失败",
			ChunkCount: 0,
		},
	}
	svc := newScriptServiceForTest(repository, &fakeScriptStorage{}, &fakeScriptParser{})

	err := svc.UpdateParseResult(42, model.ScriptStatusFailed, " PDF 解析失败 ", 0)

	if err != nil {
		t.Fatalf("repeated callback error = %v", err)
	}
}

func TestScriptServiceUpdateParseResultRejectsConflictingCallback(t *testing.T) {
	repository := &fakeScriptRepository{
		scriptByID: &model.Script{
			ID:         42,
			Status:     model.ScriptStatusReady,
			ChunkCount: 5,
		},
	}
	svc := newScriptServiceForTest(repository, &fakeScriptStorage{}, &fakeScriptParser{})

	err := svc.UpdateParseResult(42, model.ScriptStatusFailed, "late failure", 0)

	if !errors.Is(err, ErrScriptStatusConflict) {
		t.Fatalf("error = %v, want ErrScriptStatusConflict", err)
	}
}

func TestScriptServiceUpdateParseResultMapsMissingScript(t *testing.T) {
	repository := &fakeScriptRepository{scriptByIDErr: gorm.ErrRecordNotFound}
	svc := newScriptServiceForTest(repository, &fakeScriptStorage{}, &fakeScriptParser{})

	err := svc.UpdateParseResult(42, model.ScriptStatusReady, "", 3)

	if !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("error = %v, want ErrScriptNotFound", err)
	}
}

type fakeScriptStorage struct {
	putErr        error
	removeErr     error
	objectName    string
	content       []byte
	contentType   string
	removedObject string
}

func (s *fakeScriptStorage) PutObject(
	_ context.Context,
	objectName string,
	reader io.Reader,
	_ int64,
	contentType string,
) error {
	if s.putErr != nil {
		return s.putErr
	}
	content, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.objectName = objectName
	s.content = content
	s.contentType = contentType
	return nil
}

func (s *fakeScriptStorage) RemoveObject(_ context.Context, objectName string) error {
	s.removedObject = objectName
	return s.removeErr
}

type fakeScriptParser struct {
	err              error
	response         *ai_client.ParseScriptResponse
	request          *ai_client.ParseScriptRequest
	deleteVectorsErr error
	deletedVectorID  uint
}

func (p *fakeScriptParser) ParseScript(
	_ context.Context,
	req *ai_client.ParseScriptRequest,
) (*ai_client.ParseScriptResponse, error) {
	p.request = req
	return p.response, p.err
}

func (p *fakeScriptParser) DeleteScriptVectors(_ context.Context, scriptID uint) error {
	p.deletedVectorID = scriptID
	return p.deleteVectorsErr
}

func newScriptServiceForTest(
	repository *fakeScriptRepository,
	objectStorage *fakeScriptStorage,
	parser *fakeScriptParser,
) *ScriptService {
	return NewScriptService(repository, objectStorage, parser, &config.Config{
		MinIO: config.MinIOConfig{MaxUploadSize: 1024},
	})
}

func validUploadRequest() *UploadScriptRequest {
	content := []byte("%PDF-1.7\nsample")
	file, header := newPDFUpload("haunted-house.pdf", "application/pdf", content)
	return &UploadScriptRequest{
		UserID:      7,
		Description: "  A mystery adventure.  ",
		File:        file,
		FileHeader:  header,
	}
}

func TestScriptServiceUpload(t *testing.T) {
	repository := &fakeScriptRepository{}
	objectStorage := &fakeScriptStorage{}
	parser := &fakeScriptParser{
		response: &ai_client.ParseScriptResponse{Success: true, Message: "accepted"},
	}
	svc := newScriptServiceForTest(repository, objectStorage, parser)

	script, err := svc.Upload(context.Background(), validUploadRequest())
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	if script.ID != 42 {
		t.Fatalf("script ID = %d, want 42", script.ID)
	}
	if script.Title != "haunted-house" {
		t.Fatalf("script title = %q, want %q", script.Title, "haunted-house")
	}
	if script.Description != "A mystery adventure." {
		t.Fatalf("script description = %q", script.Description)
	}
	if script.Status != model.ScriptStatusParsing {
		t.Fatalf("script status = %q, want %q", script.Status, model.ScriptStatusParsing)
	}
	if !strings.HasPrefix(objectStorage.objectName, "scripts/7/42/") ||
		!strings.HasSuffix(objectStorage.objectName, ".pdf") {
		t.Fatalf("object name = %q, want scripts/7/42/<uuid>.pdf", objectStorage.objectName)
	}
	if !bytes.Equal(objectStorage.content, []byte("%PDF-1.7\nsample")) {
		t.Fatalf("stored content = %q", objectStorage.content)
	}
	if objectStorage.contentType != pdfContentType {
		t.Fatalf("content type = %q, want %q", objectStorage.contentType, pdfContentType)
	}
	if repository.filePath != objectStorage.objectName {
		t.Fatalf("repository file path = %q, want %q", repository.filePath, objectStorage.objectName)
	}
	if parser.request == nil || parser.request.ScriptID != 42 ||
		parser.request.FilePath != objectStorage.objectName {
		t.Fatalf("parser request = %#v", parser.request)
	}
	if len(repository.statuses) != 1 || repository.statuses[0] != model.ScriptStatusParsing {
		t.Fatalf("statuses = %v, want [parsing]", repository.statuses)
	}
}

func TestScriptServiceUploadRejectsInvalidPDFBeforeSideEffects(t *testing.T) {
	repository := &fakeScriptRepository{}
	objectStorage := &fakeScriptStorage{}
	parser := &fakeScriptParser{}
	svc := newScriptServiceForTest(repository, objectStorage, parser)
	req := validUploadRequest()
	req.File, req.FileHeader = newPDFUpload("fake.pdf", "application/pdf", []byte("not pdf"))

	_, err := svc.Upload(context.Background(), req)
	if !errors.Is(err, ErrScriptFileSignature) {
		t.Fatalf("Upload() error = %v, want %v", err, ErrScriptFileSignature)
	}
	if repository.created != nil || objectStorage.objectName != "" || parser.request != nil {
		t.Fatal("invalid upload caused external side effects")
	}
}

func TestScriptServiceUploadMarksStorageFailure(t *testing.T) {
	repository := &fakeScriptRepository{}
	objectStorage := &fakeScriptStorage{putErr: errors.New("storage unavailable")}
	parser := &fakeScriptParser{}
	svc := newScriptServiceForTest(repository, objectStorage, parser)

	_, err := svc.Upload(context.Background(), validUploadRequest())
	if !errors.Is(err, ErrInternal) {
		t.Fatalf("Upload() error = %v, want wrapped %v", err, ErrInternal)
	}
	assertLastScriptStatus(t, repository, model.ScriptStatusFailed, "failed to store script file")
	if parser.request != nil {
		t.Fatal("storage failure should not trigger parser")
	}
}

func TestScriptServiceUploadCleansObjectWhenMetadataUpdateFails(t *testing.T) {
	repository := &fakeScriptRepository{updateFileErr: errors.New("database unavailable")}
	objectStorage := &fakeScriptStorage{}
	parser := &fakeScriptParser{}
	svc := newScriptServiceForTest(repository, objectStorage, parser)

	_, err := svc.Upload(context.Background(), validUploadRequest())
	if !errors.Is(err, ErrInternal) {
		t.Fatalf("Upload() error = %v, want wrapped %v", err, ErrInternal)
	}
	if objectStorage.removedObject == "" || objectStorage.removedObject != objectStorage.objectName {
		t.Fatalf("removed object = %q, stored object = %q", objectStorage.removedObject, objectStorage.objectName)
	}
	assertLastScriptStatus(t, repository, model.ScriptStatusFailed, "failed to save script file metadata")
}

func TestScriptServiceUploadMarksParserFailure(t *testing.T) {
	repository := &fakeScriptRepository{}
	objectStorage := &fakeScriptStorage{}
	parser := &fakeScriptParser{err: errors.New("parser unavailable")}
	svc := newScriptServiceForTest(repository, objectStorage, parser)

	_, err := svc.Upload(context.Background(), validUploadRequest())
	if !errors.Is(err, ErrInternal) {
		t.Fatalf("Upload() error = %v, want wrapped %v", err, ErrInternal)
	}
	assertLastScriptStatus(t, repository, model.ScriptStatusFailed, "failed to start script parsing")
}

func TestScriptServiceUploadRejectsLongTitle(t *testing.T) {
	repository := &fakeScriptRepository{}
	objectStorage := &fakeScriptStorage{}
	parser := &fakeScriptParser{}
	svc := newScriptServiceForTest(repository, objectStorage, parser)
	req := validUploadRequest()
	req.Title = strings.Repeat("剧", maxScriptTitleLength+1)

	_, err := svc.Upload(context.Background(), req)
	if !errors.Is(err, ErrScriptTitleTooLong) {
		t.Fatalf("Upload() error = %v, want %v", err, ErrScriptTitleTooLong)
	}
	if repository.created != nil {
		t.Fatal("long title should be rejected before database write")
	}
}

func TestScriptServiceList(t *testing.T) {
	repository := &fakeScriptRepository{
		findItems: []model.Script{{ID: 1, UserID: 7, Title: "Adventure"}},
		findTotal: 6,
	}
	svc := newScriptServiceForTest(repository, &fakeScriptStorage{}, &fakeScriptParser{})

	result, err := svc.List(7, 2, 5)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if repository.findUserID != 7 || repository.findOffset != 5 || repository.findLimit != 5 {
		t.Fatalf(
			"repository query = (user=%d, offset=%d, limit=%d)",
			repository.findUserID,
			repository.findOffset,
			repository.findLimit,
		)
	}
	if result.Total != 6 || result.Page != 2 || result.PageSize != 5 || len(result.Items) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestScriptServiceListAppliesPaginationDefaultsAndLimit(t *testing.T) {
	repository := &fakeScriptRepository{}
	svc := newScriptServiceForTest(repository, &fakeScriptStorage{}, &fakeScriptParser{})

	result, err := svc.List(7, 0, maxScriptPageSize+50)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if result.Page != 1 || result.PageSize != maxScriptPageSize {
		t.Fatalf("pagination = (%d, %d)", result.Page, result.PageSize)
	}
	if result.Items == nil {
		t.Fatal("empty items must be serialized as an empty array")
	}
}

func TestScriptServiceListWrapsRepositoryFailure(t *testing.T) {
	repository := &fakeScriptRepository{findErr: errors.New("mysql unavailable")}
	svc := newScriptServiceForTest(repository, &fakeScriptStorage{}, &fakeScriptParser{})

	_, err := svc.List(7, 1, 20)
	if !errors.Is(err, ErrInternal) {
		t.Fatalf("List() error = %v, want wrapped %v", err, ErrInternal)
	}
}

func TestScriptServiceGetDetail(t *testing.T) {
	repository := &fakeScriptRepository{
		detailScript: &model.Script{ID: 42, UserID: 7, Title: "Adventure"},
		characters: []model.ScriptCharacter{{
			ID:         3,
			ScriptID:   42,
			Name:       "Investigator",
			Attributes: `{"hp":10}`,
		}},
	}
	svc := newScriptServiceForTest(repository, &fakeScriptStorage{}, &fakeScriptParser{})

	result, err := svc.GetDetail(7, 42)
	if err != nil {
		t.Fatalf("GetDetail() error = %v", err)
	}
	if repository.detailID != 42 || repository.detailUserID != 7 {
		t.Fatalf("detail query = (script=%d, user=%d)", repository.detailID, repository.detailUserID)
	}
	if result.Script.ID != 42 || len(result.Characters) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestScriptServiceGetDetailHidesUnauthorizedScriptAsNotFound(t *testing.T) {
	repository := &fakeScriptRepository{detailErr: gorm.ErrRecordNotFound}
	svc := newScriptServiceForTest(repository, &fakeScriptStorage{}, &fakeScriptParser{})

	_, err := svc.GetDetail(99, 42)
	if !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("GetDetail() error = %v, want %v", err, ErrScriptNotFound)
	}
}

func TestScriptServiceGetDetailReturnsEmptyCharacterArray(t *testing.T) {
	repository := &fakeScriptRepository{
		detailScript: &model.Script{ID: 42, UserID: 7},
	}
	svc := newScriptServiceForTest(repository, &fakeScriptStorage{}, &fakeScriptParser{})

	result, err := svc.GetDetail(7, 42)
	if err != nil {
		t.Fatalf("GetDetail() error = %v", err)
	}
	if result.Characters == nil {
		t.Fatal("characters must be an empty slice")
	}
}

func TestScriptServiceDelete(t *testing.T) {
	repository := &fakeScriptRepository{
		detailScript: &model.Script{
			ID:       42,
			UserID:   7,
			FilePath: "scripts/7/42/file.pdf",
			Status:   model.ScriptStatusReady,
		},
	}
	objectStorage := &fakeScriptStorage{}
	parser := &fakeScriptParser{}
	svc := newScriptServiceForTest(repository, objectStorage, parser)

	err := svc.Delete(context.Background(), 7, 42)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if repository.detailUserID != 7 || repository.detailID != 42 {
		t.Fatalf("detail query = (user=%d, script=%d)", repository.detailUserID, repository.detailID)
	}
	if objectStorage.removedObject != "scripts/7/42/file.pdf" {
		t.Fatalf("removed object = %q", objectStorage.removedObject)
	}
	if parser.deletedVectorID != 42 {
		t.Fatalf("deleted vector script ID = %d, want 42", parser.deletedVectorID)
	}
	if repository.deletedID != 42 {
		t.Fatalf("deleted ID = %d, want 42", repository.deletedID)
	}
}

func TestScriptServiceDeleteWithoutStoredObject(t *testing.T) {
	repository := &fakeScriptRepository{
		detailScript: &model.Script{
			ID:     42,
			UserID: 7,
			Status: model.ScriptStatusFailed,
		},
	}
	objectStorage := &fakeScriptStorage{}
	svc := newScriptServiceForTest(repository, objectStorage, &fakeScriptParser{})

	err := svc.Delete(context.Background(), 7, 42)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if objectStorage.removedObject != "" {
		t.Fatalf("unexpected object removal: %q", objectStorage.removedObject)
	}
	if repository.deletedID != 42 {
		t.Fatalf("deleted ID = %d, want 42", repository.deletedID)
	}
}

func TestScriptServiceDeleteStopsWhenObjectRemovalFails(t *testing.T) {
	repository := &fakeScriptRepository{
		detailScript: &model.Script{
			ID:       42,
			UserID:   7,
			FilePath: "scripts/7/42/file.pdf",
			Status:   model.ScriptStatusReady,
		},
	}
	objectStorage := &fakeScriptStorage{removeErr: errors.New("minio unavailable")}
	svc := newScriptServiceForTest(repository, objectStorage, &fakeScriptParser{})

	err := svc.Delete(context.Background(), 7, 42)
	if !errors.Is(err, ErrInternal) {
		t.Fatalf("Delete() error = %v, want wrapped %v", err, ErrInternal)
	}
	if repository.deletedID != 0 {
		t.Fatalf("database record deleted after object failure: %d", repository.deletedID)
	}
}

func TestScriptServiceDeleteStopsWhenVectorCleanupFails(t *testing.T) {
	repository := &fakeScriptRepository{
		detailScript: &model.Script{
			ID:       42,
			UserID:   7,
			FilePath: "scripts/7/42/file.pdf",
			Status:   model.ScriptStatusReady,
		},
	}
	objectStorage := &fakeScriptStorage{}
	parser := &fakeScriptParser{deleteVectorsErr: errors.New("milvus unavailable")}
	svc := newScriptServiceForTest(repository, objectStorage, parser)

	err := svc.Delete(context.Background(), 7, 42)

	if !errors.Is(err, ErrInternal) {
		t.Fatalf("Delete() error = %v, want wrapped %v", err, ErrInternal)
	}
	if objectStorage.removedObject != "" || repository.deletedID != 0 {
		t.Fatal("deletion continued after vector cleanup failure")
	}
}

func TestScriptServiceDeleteRejectsActiveParsing(t *testing.T) {
	repository := &fakeScriptRepository{
		detailScript: &model.Script{
			ID:     42,
			UserID: 7,
			Status: model.ScriptStatusParsing,
		},
	}
	parser := &fakeScriptParser{}
	svc := newScriptServiceForTest(repository, &fakeScriptStorage{}, parser)

	err := svc.Delete(context.Background(), 7, 42)

	if !errors.Is(err, ErrScriptStatusConflict) {
		t.Fatalf("Delete() error = %v, want ErrScriptStatusConflict", err)
	}
	if parser.deletedVectorID != 0 || repository.deletedID != 0 {
		t.Fatal("active script reached deletion dependencies")
	}
}

func TestScriptServiceDeleteHidesUnauthorizedScriptAsNotFound(t *testing.T) {
	repository := &fakeScriptRepository{detailErr: gorm.ErrRecordNotFound}
	svc := newScriptServiceForTest(repository, &fakeScriptStorage{}, &fakeScriptParser{})

	err := svc.Delete(context.Background(), 99, 42)
	if !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("Delete() error = %v, want %v", err, ErrScriptNotFound)
	}
}

func TestScriptServiceRetry(t *testing.T) {
	repository := &fakeScriptRepository{
		detailScript: &model.Script{
			ID:         42,
			UserID:     7,
			FilePath:   "scripts/7/42/source.pdf",
			Status:     model.ScriptStatusFailed,
			ParseError: "empty PDF",
			ChunkCount: 3,
		},
		retryUpdated: true,
	}
	parser := &fakeScriptParser{
		response: &ai_client.ParseScriptResponse{Success: true, Message: "accepted"},
	}
	svc := newScriptServiceForTest(repository, &fakeScriptStorage{}, parser)

	script, err := svc.Retry(context.Background(), 7, 42)

	if err != nil {
		t.Fatalf("Retry() error = %v", err)
	}
	if repository.retryID != 42 || repository.retryUserID != 7 {
		t.Fatalf(
			"retry transition = (script=%d, user=%d)",
			repository.retryID,
			repository.retryUserID,
		)
	}
	if parser.request == nil ||
		parser.request.ScriptID != 42 ||
		parser.request.FilePath != "scripts/7/42/source.pdf" {
		t.Fatalf("parser request = %#v", parser.request)
	}
	if script.Status != model.ScriptStatusParsing ||
		script.ParseError != "" ||
		script.ChunkCount != 0 {
		t.Fatalf("retried script = %#v", script)
	}
}

func TestScriptServiceRetryRejectsNonFailedScript(t *testing.T) {
	repository := &fakeScriptRepository{
		detailScript: &model.Script{
			ID:       42,
			UserID:   7,
			FilePath: "scripts/7/42/source.pdf",
			Status:   model.ScriptStatusReady,
		},
	}
	parser := &fakeScriptParser{}
	svc := newScriptServiceForTest(repository, &fakeScriptStorage{}, parser)

	_, err := svc.Retry(context.Background(), 7, 42)

	if !errors.Is(err, ErrScriptStatusConflict) {
		t.Fatalf("Retry() error = %v, want ErrScriptStatusConflict", err)
	}
	if repository.retryID != 0 || parser.request != nil {
		t.Fatal("non-failed script reached retry dependencies")
	}
}

func TestScriptServiceRetryRestoresFailedStatusWhenParserCallFails(t *testing.T) {
	repository := &fakeScriptRepository{
		detailScript: &model.Script{
			ID:       42,
			UserID:   7,
			FilePath: "scripts/7/42/source.pdf",
			Status:   model.ScriptStatusFailed,
		},
		retryUpdated: true,
	}
	parser := &fakeScriptParser{err: errors.New("python unavailable")}
	svc := newScriptServiceForTest(repository, &fakeScriptStorage{}, parser)

	_, err := svc.Retry(context.Background(), 7, 42)

	if !errors.Is(err, ErrInternal) {
		t.Fatalf("Retry() error = %v, want wrapped ErrInternal", err)
	}
	assertLastScriptStatus(
		t,
		repository,
		model.ScriptStatusFailed,
		"failed to restart script parsing",
	)
}

func TestScriptServiceRetryHidesUnauthorizedScriptAsNotFound(t *testing.T) {
	repository := &fakeScriptRepository{detailErr: gorm.ErrRecordNotFound}
	svc := newScriptServiceForTest(repository, &fakeScriptStorage{}, &fakeScriptParser{})

	_, err := svc.Retry(context.Background(), 99, 42)

	if !errors.Is(err, ErrScriptNotFound) {
		t.Fatalf("Retry() error = %v, want ErrScriptNotFound", err)
	}
}

func assertLastScriptStatus(
	t *testing.T,
	repository *fakeScriptRepository,
	wantStatus model.ScriptStatus,
	wantMessage string,
) {
	t.Helper()
	if len(repository.statuses) == 0 {
		t.Fatal("no status update recorded")
	}
	last := len(repository.statuses) - 1
	if repository.statuses[last] != wantStatus || repository.statusErrors[last] != wantMessage {
		t.Fatalf(
			"last status = (%q, %q), want (%q, %q)",
			repository.statuses[last],
			repository.statusErrors[last],
			wantStatus,
			wantMessage,
		)
	}
}
