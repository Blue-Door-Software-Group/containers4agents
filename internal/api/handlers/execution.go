package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	apierrors "github.com/Blue-Door-Software-Group/containers4agents/internal/api/errors"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/middleware"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/execution"
)

type ExecutionService interface {
	Execute(ctx context.Context, externalID string, command []string, env map[string]string) (*execution.Result, error)
}

type ExecutionHandler struct {
	service ExecutionService
}

func NewExecutionHandler(service ExecutionService) *ExecutionHandler {
	return &ExecutionHandler{service: service}
}

type execRequest struct {
	Command []string `json:"command"`
	// Env is extra environment for this command only (optional; never logged).
	Env map[string]string `json:"env,omitempty"`
}

// Exec env limits.
const (
	maxExecEnvEntries = 64
	maxExecEnvBytes   = 64 << 10
)

var execEnvNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)

// validateExecEnv checks an exec's env: names, count and total size.
func validateExecEnv(env map[string]string) string {
	if len(env) > maxExecEnvEntries {
		return "env has too many entries"
	}
	total := 0
	for k, v := range env {
		if !execEnvNamePattern.MatchString(k) {
			return "env names must match ^[A-Z_][A-Z0-9_]{0,63}$"
		}
		if strings.ContainsRune(v, 0) {
			return "env values must not contain NUL"
		}
		total += len(k) + len(v)
	}
	if total > maxExecEnvBytes {
		return "env is too large"
	}
	return ""
}

type execResponse struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	Stdout           string `json:"stdout"`
	Stderr           string `json:"stderr"`
	DurationMs       int64  `json:"duration_ms"`
	AppliedTimeoutMs int64  `json:"applied_timeout_ms"`
	TruncatedOut     bool   `json:"truncated_stdout"`
	TruncatedErr     bool   `json:"truncated_stderr"`
}

func (h *ExecutionHandler) Exec(w http.ResponseWriter, r *http.Request) {
	externalID := r.PathValue("id")
	if externalID == "" {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.BadRequest("missing container id"))
		return
	}

	var body execRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.BadRequest("invalid execution request body"))
		return
	}
	if len(body.Command) == 0 {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.BadRequest("command must not be empty"))
		return
	}

	if msg := validateExecEnv(body.Env); msg != "" {
		apierrors.Write(w, middleware.GetRequestID(r.Context()), apierrors.BadRequest(msg))
		return
	}

	result, err := h.service.Execute(r.Context(), externalID, body.Command, body.Env)
	if err != nil {
		writeExecutionError(w, r, err)
		return
	}

	resp := execResponse{
		ID:               result.ContainerID,
		Status:           result.Status,
		ExitCode:         result.ExitCode,
		Stdout:           result.Stdout,
		Stderr:           result.Stderr,
		DurationMs:       result.Duration.Milliseconds(),
		AppliedTimeoutMs: result.AppliedTimeout.Milliseconds(),
		TruncatedOut:     result.TruncatedOut,
		TruncatedErr:     result.TruncatedErr,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func writeExecutionError(w http.ResponseWriter, r *http.Request, err error) {
	requestID := middleware.GetRequestID(r.Context())

	switch {
	case errors.Is(err, execution.ErrInvalidCommand):
		apierrors.Write(w, requestID, apierrors.BadRequest("invalid execution request"))
	case errors.Is(err, execution.ErrContainerNotFound):
		apierrors.Write(w, requestID, apierrors.NotFound("container not found"))
	case errors.Is(err, execution.ErrStateConflict):
		apierrors.Write(w, requestID, apierrors.Conflict(err.Error()))
	case errors.Is(err, execution.ErrProviderFailure):
		apierrors.Write(w, requestID, apierrors.ServiceUnavailable("container provider unavailable"))
	case errors.Is(err, context.DeadlineExceeded):
		apierrors.Write(w, requestID, apierrors.ServiceUnavailable("execution timed out in control plane"))
	default:
		apierrors.Write(w, requestID, apierrors.Internal("internal execution error"))
	}
}
