package errors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteSerializesStructuredError(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, "req-123", APIError{
		Code:      "bad_request",
		Message:   "invalid payload",
		Status:    http.StatusBadRequest,
		Retryable: false,
	})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var payload ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if payload.Code != "bad_request" {
		t.Fatalf("expected code bad_request, got %s", payload.Code)
	}
	if payload.Message != "invalid payload" {
		t.Fatalf("expected message %q, got %q", "invalid payload", payload.Message)
	}
	if payload.RequestID != "req-123" {
		t.Fatalf("expected request id req-123, got %s", payload.RequestID)
	}
	if payload.Retryable {
		t.Fatal("expected retryable to be false")
	}
}
