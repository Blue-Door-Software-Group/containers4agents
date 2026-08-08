package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
func (r *handlerTestRepo) CreateIdempotencyRecord(context.Context, repository.IdempotencyRecord) (*repository.IdempotencyRecord, error) {
	return nil, nil
}
func (r *handlerTestRepo) UpdateIdempotencyRecordResult(context.Context, string, string, string, string, int, string) (*repository.IdempotencyRecord, error) {
	return nil, nil
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
