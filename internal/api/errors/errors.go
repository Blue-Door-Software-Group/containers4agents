package errors

import (
	"encoding/json"
	"net/http"
)

type ErrorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
	Retryable bool   `json:"retryable"`
}

type APIError struct {
	Code      string
	Message   string
	Status    int
	Retryable bool
}

func (e APIError) Error() string {
	return e.Message
}

func Write(w http.ResponseWriter, requestID string, e APIError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Code:      e.Code,
		Message:   e.Message,
		RequestID: requestID,
		Retryable: e.Retryable,
	})
}

func Internal(message string) APIError {
	return APIError{
		Code:      "internal_error",
		Message:   message,
		Status:    http.StatusInternalServerError,
		Retryable: true,
	}
}

func NotFound(message string) APIError {
	return APIError{
		Code:      "not_found",
		Message:   message,
		Status:    http.StatusNotFound,
		Retryable: false,
	}
}

func Unauthorized(message string) APIError {
	return APIError{
		Code:      "unauthorized",
		Message:   message,
		Status:    http.StatusUnauthorized,
		Retryable: false,
	}
}

func Conflict(message string) APIError {
	return APIError{
		Code:      "state_conflict",
		Message:   message,
		Status:    http.StatusConflict,
		Retryable: false,
	}
}

func IdempotencyConflict(message string) APIError {
	return APIError{
		Code:      "idempotency_conflict",
		Message:   message,
		Status:    http.StatusConflict,
		Retryable: false,
	}
}

func BadRequest(message string) APIError {
	return APIError{
		Code:      "bad_request",
		Message:   message,
		Status:    http.StatusBadRequest,
		Retryable: false,
	}
}

func UnprocessableEntity(message string) APIError {
	return APIError{
		Code:      "validation_error",
		Message:   message,
		Status:    http.StatusUnprocessableEntity,
		Retryable: false,
	}
}

func ServiceUnavailable(message string) APIError {
	return APIError{
		Code:      "provider_unavailable",
		Message:   message,
		Status:    http.StatusServiceUnavailable,
		Retryable: true,
	}
}

func PayloadTooLarge(message string) APIError {
	return APIError{
		Code:      "payload_too_large",
		Message:   message,
		Status:    http.StatusRequestEntityTooLarge,
		Retryable: false,
	}
}

func CapacityExceeded(message string) APIError {
	return APIError{
		Code:      "capacity_exceeded",
		Message:   message,
		Status:    http.StatusTooManyRequests,
		Retryable: true,
	}
}
