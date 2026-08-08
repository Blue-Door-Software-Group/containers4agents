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
