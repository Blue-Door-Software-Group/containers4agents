package idempotency

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql/sqlgraph"
	"github.com/lib/pq"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/metadata/repository"
)

const HeaderName = "Idempotency-Key"

const maxStoredResponseBytes = 8192
const truncatedMarker = "\n...[truncated]"

var (
	// ErrHashMismatch indicates the same idempotency key was reused with a different
	// canonical request hash (same key + different payload). Callers should map this
	// to an HTTP 409 conflict.
	ErrHashMismatch       = errors.New("idempotency key request hash mismatch")
	ErrInProgress         = errors.New("idempotency key already in progress")
	ErrInvalidOperation   = errors.New("idempotency operation is not allowed")
	ErrInvalidScope       = errors.New("idempotency scope is invalid for operation")
	ErrInvalidRequestHash = errors.New("idempotency request hash must be a sha256 hex string")
)

type BeginResult struct {
	Replay       bool
	StatusCode   int
	ResponseBody string
}

type Service struct {
	repo repository.ContainerRepository
	ttl  time.Duration
}

func NewService(repo repository.ContainerRepository, ttl time.Duration) *Service {
	return &Service{
		repo: repo,
		ttl:  ttl,
	}
}

// Begin claims or replays an idempotency key for the given operation and scope.
// It returns ErrHashMismatch if the key already exists with a different request
// hash and ErrInProgress if another request currently owns the key.
func (s *Service) Begin(
	ctx context.Context,
	operation string,
	scope string,
	key string,
	requestHash string,
) (*BeginResult, error) {
	if s == nil || key == "" {
		return &BeginResult{}, nil
	}
	if err := validateOperationScopeHash(operation, scope, requestHash); err != nil {
		return nil, err
	}

	record, err := s.repo.GetIdempotencyRecord(ctx, operation, scope, key)
	if err == nil {
		if !constantTimeEqual(record.RequestHash, requestHash) {
			return nil, ErrHashMismatch
		}
		if isReplayableStatus(record.StatusCode) && record.ResponseBody != nil {
			return &BeginResult{
				Replay:       true,
				StatusCode:   record.StatusCode,
				ResponseBody: *record.ResponseBody,
			}, nil
		}
		if record.StatusCode > 0 {
			return &BeginResult{}, nil
		}
		return nil, ErrInProgress
	}
	if !errors.Is(err, repository.ErrNotFound) {
		return nil, fmt.Errorf("load idempotency record: %w", err)
	}

	_, createErr := s.repo.CreateIdempotencyRecord(ctx, repository.IdempotencyRecord{
		Operation:   operation,
		Scope:       scope,
		Key:         key,
		RequestHash: requestHash,
		ExpiresAt:   time.Now().UTC().Add(s.ttl),
	})
	if createErr != nil {
		if !isUniqueViolationError(createErr) {
			return nil, fmt.Errorf("claim idempotency key infrastructure failure: %w", createErr)
		}
		// Race with another request claiming the same key. Re-read record.
		record, rereadErr := s.repo.GetIdempotencyRecord(ctx, operation, scope, key)
		if rereadErr != nil {
			if errors.Is(rereadErr, repository.ErrNotFound) {
				return nil, fmt.Errorf("claim idempotency key lost race without readable winner: %w", rereadErr)
			}
			return nil, fmt.Errorf("load idempotency record after claim conflict: %w", rereadErr)
		}
		if !constantTimeEqual(record.RequestHash, requestHash) {
			return nil, ErrHashMismatch
		}
		if isReplayableStatus(record.StatusCode) && record.ResponseBody != nil {
			return &BeginResult{
				Replay:       true,
				StatusCode:   record.StatusCode,
				ResponseBody: *record.ResponseBody,
			}, nil
		}
		if record.StatusCode > 0 {
			// Prior attempt completed with non-success. Allow a fresh attempt with same key+hash.
			return &BeginResult{}, nil
		}
		return nil, ErrInProgress
	}

	return &BeginResult{}, nil
}

func (s *Service) Complete(
	ctx context.Context,
	operation string,
	scope string,
	key string,
	requestHash string,
	statusCode int,
	responseBody string,
) error {
	if s == nil || key == "" {
		return nil
	}
	if err := validateOperationScopeHash(operation, scope, requestHash); err != nil {
		return err
	}
	_, err := s.repo.UpdateIdempotencyRecordResult(
		ctx,
		operation,
		scope,
		key,
		requestHash,
		statusCode,
		sanitizeStoredResponseBody(operation, statusCode, responseBody),
	)
	if err != nil {
		return fmt.Errorf("complete idempotency record: %w", err)
	}
	return nil
}

// Hash returns a stable, canonical SHA-256 digest for ordered request parts.
// Only this digest is persisted in storage (never raw payload data).
func Hash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func constantTimeEqual(a string, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func trimStoredResponseBody(responseBody string) string {
	if len(responseBody) <= maxStoredResponseBytes {
		return responseBody
	}
	if maxStoredResponseBytes <= len(truncatedMarker) {
		return truncatedMarker
	}
	keep := maxStoredResponseBytes - len(truncatedMarker)
	return responseBody[:keep] + truncatedMarker
}

func sanitizeStoredResponseBody(operation string, statusCode int, responseBody string) string {
	if !shouldStoreReplayBody(operation, statusCode) {
		return "{}"
	}
	return trimStoredResponseBody(responseBody)
}

func shouldStoreReplayBody(operation string, statusCode int) bool {
	if !isReplayableStatus(statusCode) {
		return false
	}
	switch operation {
	case "containers.create", "containers.stop", "containers.start", "containers.prune", "files.upload":
		return true
	default:
		return false
	}
}

func isReplayableStatus(statusCode int) bool {
	return statusCode >= 200 && statusCode < 400
}

func validateOperationScopeHash(operation string, scope string, requestHash string) error {
	if !isAllowedOperation(operation) {
		return ErrInvalidOperation
	}
	if !scopeMatchesOperation(operation, scope) {
		return ErrInvalidScope
	}
	if len(requestHash) != 64 {
		return ErrInvalidRequestHash
	}
	if _, err := hex.DecodeString(requestHash); err != nil {
		return ErrInvalidRequestHash
	}
	return nil
}

func isAllowedOperation(operation string) bool {
	switch operation {
	case "containers.create", "containers.stop", "containers.start", "containers.prune", "files.upload":
		return true
	default:
		return false
	}
}

func scopeMatchesOperation(operation string, scope string) bool {
	switch operation {
	case "containers.create":
		return scope == "containers:create"
	case "containers.stop":
		return len(scope) > len("containers:stop:") && scope[:len("containers:stop:")] == "containers:stop:"
	case "containers.start":
		return len(scope) > len("containers:start:") && scope[:len("containers:start:")] == "containers:start:"
	case "containers.prune":
		return len(scope) > len("containers:prune:") && scope[:len("containers:prune:")] == "containers:prune:"
	case "files.upload":
		return len(scope) > len("files:upload:") && scope[:len("files:upload:")] == "files:upload:"
	default:
		return false
	}
}

func isUniqueViolationError(err error) bool {
	if err == nil {
		return false
	}
	if sqlgraph.IsConstraintError(err) {
		return true
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return string(pqErr.Code) == "23505"
	}
	return false
}
