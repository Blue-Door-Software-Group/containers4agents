package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apierrors "github.com/Blue-Door-Software-Group/containers4agents/internal/api/errors"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/files"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/idempotency"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
)

type handlerTestRepo struct {
	records map[string]*repository.IdempotencyRecord
}

func (r *handlerTestRepo) key(op, scope, key string) string {
	return op + "|" + scope + "|" + key
}

func (r *handlerTestRepo) Create(context.Context, repository.CreateParams) (*repository.Container, error) {
	return nil, nil
}
func (r *handlerTestRepo) GetByExternalID(context.Context, string) (*repository.Container, error) {
	return nil, repository.ErrNotFound
}
func (r *handlerTestRepo) UpdateState(context.Context, repository.UpdateStateParams) (*repository.Container, error) {
	return nil, nil
}
func (r *handlerTestRepo) UpdateStateIfCurrent(context.Context, repository.UpdateStateParams, string) (*repository.Container, error) {
	return nil, nil
}
func (r *handlerTestRepo) BeginExecution(context.Context, string, string) error { return nil }
func (r *handlerTestRepo) EndExecution(context.Context, string) error           { return nil }
func (r *handlerTestRepo) CountByState(context.Context, string) (int, error)    { return 0, nil }
func (r *handlerTestRepo) CountTotal(context.Context) (int, error)              { return 0, nil }
func (r *handlerTestRepo) CountExecutionInProgress(context.Context) (int, error) {
	return 0, nil
}
func (r *handlerTestRepo) FindLongestIdleStopped(context.Context, int) ([]*repository.Container, error) {
	return nil, nil
}
func (r *handlerTestRepo) FindExpiredRunningIdle(context.Context, time.Time, int) ([]*repository.Container, error) {
	return nil, nil
}
func (r *handlerTestRepo) FindExpiredStoppedRetention(context.Context, time.Time, int) ([]*repository.Container, error) {
	return nil, nil
}
func (r *handlerTestRepo) FindLongestIdleRunningNoExec(context.Context, int) ([]*repository.Container, error) {
	return nil, nil
}
func (r *handlerTestRepo) FindPruneRetryDue(context.Context, time.Time, int) ([]*repository.Container, error) {
	return nil, nil
}
func (r *handlerTestRepo) ListNonTerminal(context.Context, int) ([]*repository.Container, error) {
	return nil, nil
}
func (r *handlerTestRepo) BackfillLifecycleDeadlines(context.Context, time.Time, time.Duration, time.Duration, int) (int, error) {
	return 0, nil
}
func (r *handlerTestRepo) GetIdempotencyRecord(_ context.Context, operation, scope, key string) (*repository.IdempotencyRecord, error) {
	record, ok := r.records[r.key(operation, scope, key)]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return record, nil
}
func (r *handlerTestRepo) CreateIdempotencyRecord(_ context.Context, record repository.IdempotencyRecord) (*repository.IdempotencyRecord, error) {
	if r.records == nil {
		r.records = map[string]*repository.IdempotencyRecord{}
	}
	stored := record
	r.records[r.key(record.Operation, record.Scope, record.Key)] = &stored
	return &stored, nil
}
func (r *handlerTestRepo) UpdateIdempotencyRecordResult(_ context.Context, operation, scope, key, requestHash string, statusCode int, responseBody string) (*repository.IdempotencyRecord, error) {
	k := r.key(operation, scope, key)
	record, ok := r.records[k]
	if !ok {
		return nil, repository.ErrNotFound
	}
	if record.RequestHash != requestHash {
		return nil, repository.ErrStateConflict
	}
	record.StatusCode = statusCode
	record.ResponseBody = &responseBody
	r.records[k] = record
	return record, nil
}
func (r *handlerTestRepo) DeleteExpiredIdempotencyRecords(context.Context, time.Time) (int, error) {
	return 0, nil
}

type noOpContainerService struct {
	createCalls int
}

func (s *noOpContainerService) CreateContainer(context.Context) (*lifecycle.ContainerView, error) {
	s.createCalls++
	now := time.Now().UTC()
	return &lifecycle.ContainerView{
		ExternalID:   "c1",
		State:        lifecycle.StateRunning,
		Profile:      "default",
		ImageVersion: "latest",
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}
func (s *noOpContainerService) GetContainer(context.Context, string) (*lifecycle.ContainerView, error) {
	return nil, lifecycle.ErrContainerNotFound
}
func (s *noOpContainerService) StopContainer(context.Context, string) (*lifecycle.ContainerView, error) {
	return nil, lifecycle.ErrContainerNotFound
}
func (s *noOpContainerService) StartContainer(context.Context, string) (*lifecycle.ContainerView, error) {
	return nil, lifecycle.ErrContainerNotFound
}
func (s *noOpContainerService) PruneContainer(context.Context, string) (*lifecycle.ContainerView, error) {
	return nil, lifecycle.ErrContainerNotFound
}

type replayAwareContainerService struct {
	createCalls int
	createID    string
	getByID     map[string]error
}

func (s *replayAwareContainerService) CreateContainer(context.Context) (*lifecycle.ContainerView, error) {
	s.createCalls++
	now := time.Now().UTC()
	return &lifecycle.ContainerView{
		ExternalID:   s.createID,
		State:        lifecycle.StateRunning,
		Profile:      "default",
		ImageVersion: "latest",
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

func (s *replayAwareContainerService) GetContainer(_ context.Context, externalID string) (*lifecycle.ContainerView, error) {
	if err, ok := s.getByID[externalID]; ok && err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &lifecycle.ContainerView{
		ExternalID:   externalID,
		State:        lifecycle.StateRunning,
		Profile:      "default",
		ImageVersion: "latest",
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

func (s *replayAwareContainerService) StopContainer(context.Context, string) (*lifecycle.ContainerView, error) {
	return nil, lifecycle.ErrContainerNotFound
}

func (s *replayAwareContainerService) StartContainer(context.Context, string) (*lifecycle.ContainerView, error) {
	return nil, lifecycle.ErrContainerNotFound
}

func (s *replayAwareContainerService) PruneContainer(context.Context, string) (*lifecycle.ContainerView, error) {
	return nil, lifecycle.ErrContainerNotFound
}

type noOpFileService struct {
	uploadCalls int
}

func (s *noOpFileService) Upload(context.Context, string, string, io.Reader) error {
	s.uploadCalls++
	return nil
}
func (s *noOpFileService) Download(context.Context, string, string, io.Writer) error { return nil }
func (s *noOpFileService) Delete(context.Context, string, string) error              { return nil }
func (s *noOpFileService) Manifest(context.Context, string) (*files.Manifest, error) {
	return &files.Manifest{}, nil
}

func TestContainerCreateHashMismatchMapsToConflict(t *testing.T) {
	idemKey := "idem-key-1"
	method := http.MethodPost
	path := "/v1/containers"
	requestHash := idempotency.Hash(method, path)

	repo := &handlerTestRepo{
		records: map[string]*repository.IdempotencyRecord{
			"containers.create|containers:create|" + idemKey: {
				Operation:   "containers.create",
				Scope:       "containers:create",
				Key:         idemKey,
				RequestHash: strings.Repeat("a", 64), // intentionally different from requestHash
			},
		},
	}
	if repo.records["containers.create|containers:create|"+idemKey].RequestHash == requestHash {
		t.Fatal("test setup error: expected mismatched request hash")
	}

	containerSvc := &noOpContainerService{}
	idemSvc := idempotency.NewService(repo, time.Hour)
	handler := NewContainerHandler(containerSvc, idemSvc)

	req := httptest.NewRequest(method, path, nil)
	req.Header.Set(idempotency.HeaderName, idemKey)
	rr := httptest.NewRecorder()

	handler.Create(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d body=%s", rr.Code, rr.Body.String())
	}
	if containerSvc.createCalls != 0 {
		t.Fatalf("expected container create not to be called, got %d", containerSvc.createCalls)
	}

	var payload apierrors.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != "idempotency_conflict" {
		t.Fatalf("expected idempotency_conflict code, got %q", payload.Code)
	}
}

func TestFileUploadHashMismatchMapsToConflict(t *testing.T) {
	idemKey := "idem-key-2"
	containerID := "c1"
	requestedPath := "foo.txt"
	method := http.MethodPut
	path := "/v1/containers/c1/files/foo.txt"
	contentType := "text/plain"
	body := "hello from upload"

	sum := sha256.Sum256([]byte(body))
	bodyHash := hex.EncodeToString(sum[:])
	requestHash := idempotency.Hash(method, path, bodyHash, contentType)

	scope := "files:upload:" + containerID + ":" + requestedPath
	repo := &handlerTestRepo{
		records: map[string]*repository.IdempotencyRecord{
			"files.upload|" + scope + "|" + idemKey: {
				Operation:   "files.upload",
				Scope:       scope,
				Key:         idemKey,
				RequestHash: strings.Repeat("b", 64), // intentionally different from requestHash
			},
		},
	}
	if repo.records["files.upload|"+scope+"|"+idemKey].RequestHash == requestHash {
		t.Fatal("test setup error: expected mismatched request hash")
	}

	fileSvc := &noOpFileService{}
	idemSvc := idempotency.NewService(repo, time.Hour)
	handler := NewFileHandler(fileSvc, 1024*1024, idemSvc)

	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set(idempotency.HeaderName, idemKey)
	req.SetPathValue("id", containerID)
	req.SetPathValue("path", requestedPath)
	rr := httptest.NewRecorder()

	handler.Upload(rr, req)

	if rr.Code != http.StatusConflict {
		t.Fatalf("expected status 409, got %d body=%s", rr.Code, rr.Body.String())
	}
	if fileSvc.uploadCalls != 0 {
		t.Fatalf("expected upload not to be called, got %d", fileSvc.uploadCalls)
	}

	var payload apierrors.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Code != "idempotency_conflict" {
		t.Fatalf("expected idempotency_conflict code, got %q", payload.Code)
	}
}

func TestContainerCreateReplayLiveContainerReturnsStoredResponse(t *testing.T) {
	idemKey := "idem-live"
	method := http.MethodPost
	path := "/v1/containers"
	requestHash := idempotency.Hash(method, path)
	replayBody := `{"id":"c-live","state":"running","profile":"default","image_version":"latest","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`

	repo := &handlerTestRepo{
		records: map[string]*repository.IdempotencyRecord{
			"containers.create|containers:create|" + idemKey: {
				Operation:    "containers.create",
				Scope:        "containers:create",
				Key:          idemKey,
				RequestHash:  requestHash,
				StatusCode:   http.StatusCreated,
				ResponseBody: &replayBody,
			},
		},
	}
	containerSvc := &replayAwareContainerService{
		createID: "c-new",
		getByID:  map[string]error{"c-live": nil},
	}
	idemSvc := idempotency.NewService(repo, time.Hour)
	handler := NewContainerHandler(containerSvc, idemSvc)

	req := httptest.NewRequest(method, path, nil)
	req.Header.Set(idempotency.HeaderName, idemKey)
	rr := httptest.NewRecorder()
	handler.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rr.Code, rr.Body.String())
	}
	if containerSvc.createCalls != 0 {
		t.Fatalf("expected no create call, got %d", containerSvc.createCalls)
	}
	if strings.TrimSpace(rr.Body.String()) != replayBody {
		t.Fatalf("expected replay body %s, got %s", replayBody, strings.TrimSpace(rr.Body.String()))
	}
}

func TestContainerCreateReplayMissingContainerCreatesFreshAndUpdatesRecord(t *testing.T) {
	idemKey := "idem-stale"
	method := http.MethodPost
	path := "/v1/containers"
	requestHash := idempotency.Hash(method, path)
	replayBody := `{"id":"c-old","state":"running","profile":"default","image_version":"latest","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`

	repo := &handlerTestRepo{
		records: map[string]*repository.IdempotencyRecord{
			"containers.create|containers:create|" + idemKey: {
				Operation:    "containers.create",
				Scope:        "containers:create",
				Key:          idemKey,
				RequestHash:  requestHash,
				StatusCode:   http.StatusCreated,
				ResponseBody: &replayBody,
			},
		},
	}
	containerSvc := &replayAwareContainerService{
		createID: "c-new",
		getByID:  map[string]error{"c-old": lifecycle.ErrContainerNotFound},
	}
	idemSvc := idempotency.NewService(repo, time.Hour)
	handler := NewContainerHandler(containerSvc, idemSvc)

	req := httptest.NewRequest(method, path, nil)
	req.Header.Set(idempotency.HeaderName, idemKey)
	rr := httptest.NewRecorder()
	handler.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rr.Code, rr.Body.String())
	}
	if containerSvc.createCalls != 1 {
		t.Fatalf("expected one fresh create call, got %d", containerSvc.createCalls)
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode fresh create response: %v", err)
	}
	if payload["id"] != "c-new" {
		t.Fatalf("expected new id c-new, got %v", payload["id"])
	}
	updated := repo.records["containers.create|containers:create|"+idemKey]
	if updated == nil || updated.ResponseBody == nil || !strings.Contains(*updated.ResponseBody, `"id":"c-new"`) {
		t.Fatalf("expected idempotency record to be updated with fresh create response")
	}
}

func TestContainerCreateReplayMalformedBodyFallsBackToFreshCreate(t *testing.T) {
	idemKey := "idem-bad-replay"
	method := http.MethodPost
	path := "/v1/containers"
	requestHash := idempotency.Hash(method, path)
	replayBody := `{"id":`

	repo := &handlerTestRepo{
		records: map[string]*repository.IdempotencyRecord{
			"containers.create|containers:create|" + idemKey: {
				Operation:    "containers.create",
				Scope:        "containers:create",
				Key:          idemKey,
				RequestHash:  requestHash,
				StatusCode:   http.StatusCreated,
				ResponseBody: &replayBody,
			},
		},
	}
	containerSvc := &replayAwareContainerService{
		createID: "c-fallback",
		getByID:  map[string]error{},
	}
	idemSvc := idempotency.NewService(repo, time.Hour)
	handler := NewContainerHandler(containerSvc, idemSvc)

	req := httptest.NewRequest(method, path, nil)
	req.Header.Set(idempotency.HeaderName, idemKey)
	rr := httptest.NewRecorder()
	handler.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d body=%s", rr.Code, rr.Body.String())
	}
	if containerSvc.createCalls != 1 {
		t.Fatalf("expected fallback create call, got %d", containerSvc.createCalls)
	}
}

func TestContainerCreateReplayLivenessErrorMapsToServiceUnavailable(t *testing.T) {
	idemKey := "idem-liveness-error"
	method := http.MethodPost
	path := "/v1/containers"
	requestHash := idempotency.Hash(method, path)
	replayBody := `{"id":"c-live","state":"running","profile":"default","image_version":"latest","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}`

	repo := &handlerTestRepo{
		records: map[string]*repository.IdempotencyRecord{
			"containers.create|containers:create|" + idemKey: {
				Operation:    "containers.create",
				Scope:        "containers:create",
				Key:          idemKey,
				RequestHash:  requestHash,
				StatusCode:   http.StatusCreated,
				ResponseBody: &replayBody,
			},
		},
	}
	containerSvc := &replayAwareContainerService{
		createID: "c-should-not-create",
		getByID:  map[string]error{"c-live": fmt.Errorf("%w: provider unavailable", lifecycle.ErrProviderFailure)},
	}
	idemSvc := idempotency.NewService(repo, time.Hour)
	handler := NewContainerHandler(containerSvc, idemSvc)

	req := httptest.NewRequest(method, path, nil)
	req.Header.Set(idempotency.HeaderName, idemKey)
	rr := httptest.NewRecorder()
	handler.Create(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d body=%s", rr.Code, rr.Body.String())
	}
	if containerSvc.createCalls != 0 {
		t.Fatalf("expected no fresh create on liveness error, got %d", containerSvc.createCalls)
	}
}

type failOnceContainerService struct {
	noOpContainerService
	failErr error
}

func (s *failOnceContainerService) CreateContainer(ctx context.Context) (*lifecycle.ContainerView, error) {
	if s.failErr != nil {
		s.createCalls++
		err := s.failErr
		s.failErr = nil
		return nil, err
	}
	return s.noOpContainerService.CreateContainer(ctx)
}

func TestContainerCreateFailureReleasesIdempotencyKeyForRetry(t *testing.T) {
	idemKey := "idem-retry-after-failure"
	repo := &handlerTestRepo{}
	containerSvc := &failOnceContainerService{
		failErr: fmt.Errorf("%w: total container limit reached", lifecycle.ErrCapacityExceeded),
	}
	handler := NewContainerHandler(containerSvc, idempotency.NewService(repo, time.Hour))

	doCreate := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/containers", nil)
		req.Header.Set(idempotency.HeaderName, idemKey)
		rr := httptest.NewRecorder()
		handler.Create(rr, req)
		return rr
	}

	if rr := doCreate(); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected first create status 429, got %d body=%s", rr.Code, rr.Body.String())
	}
	record := repo.records["containers.create|containers:create|"+idemKey]
	if record == nil || record.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected idempotency record to capture failed status, got %+v", record)
	}

	rr := doCreate()
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected retry with same key to create (201), got %d body=%s", rr.Code, rr.Body.String())
	}
	if containerSvc.createCalls != 2 {
		t.Fatalf("expected two create attempts, got %d", containerSvc.createCalls)
	}
}

func TestFileUploadFailureReleasesIdempotencyKeyForRetry(t *testing.T) {
	idemKey := "idem-upload-retry-after-failure"
	repo := &handlerTestRepo{}
	fileSvc := &failOnceFileService{failErr: files.ErrProviderFailure}
	handler := NewFileHandler(fileSvc, 1024, idempotency.NewService(repo, time.Hour))

	doUpload := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/v1/containers/c1/files/a.txt", strings.NewReader("hello"))
		req.SetPathValue("id", "c1")
		req.SetPathValue("path", "a.txt")
		req.Header.Set(idempotency.HeaderName, idemKey)
		rr := httptest.NewRecorder()
		handler.Upload(rr, req)
		return rr
	}

	if rr := doUpload(); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected first upload status 503, got %d body=%s", rr.Code, rr.Body.String())
	}
	if rr := doUpload(); rr.Code != http.StatusCreated {
		t.Fatalf("expected retry with same key to upload (201), got %d body=%s", rr.Code, rr.Body.String())
	}
	if fileSvc.uploadCalls != 2 {
		t.Fatalf("expected two upload attempts, got %d", fileSvc.uploadCalls)
	}
}

type failOnceFileService struct {
	noOpFileService
	failErr error
}

func (s *failOnceFileService) Upload(ctx context.Context, externalID string, path string, r io.Reader) error {
	if s.failErr != nil {
		s.uploadCalls++
		err := s.failErr
		s.failErr = nil
		return err
	}
	return s.noOpFileService.Upload(ctx, externalID, path, r)
}
