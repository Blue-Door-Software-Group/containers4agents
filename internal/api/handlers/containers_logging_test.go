package handlers

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/lifecycle"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
)

type loggingContainerService struct{}

func (s loggingContainerService) CreateContainer(context.Context) (*lifecycle.ContainerView, error) {
	return nil, fmt.Errorf("%w: provider failure with token=super-secret command=python -c print('x')", lifecycle.ErrProviderFailure)
}
func (s loggingContainerService) GetContainer(context.Context, string) (*lifecycle.ContainerView, error) {
	return nil, lifecycle.ErrProviderFailure
}
func (s loggingContainerService) StopContainer(context.Context, string) (*lifecycle.ContainerView, error) {
	return nil, lifecycle.ErrProviderFailure
}
func (s loggingContainerService) StartContainer(context.Context, string) (*lifecycle.ContainerView, error) {
	return nil, lifecycle.ErrProviderFailure
}
func (s loggingContainerService) PruneContainer(context.Context, string) (*lifecycle.ContainerView, error) {
	return nil, lifecycle.ErrProviderFailure
}

func TestCreateProviderFailureIsLogged(t *testing.T) {
	var logs bytes.Buffer
	oldDefault := slog.Default()
	slog.SetDefault(obslog.NewWithWriter("info", &logs))
	defer slog.SetDefault(oldDefault)

	h := NewContainerHandler(loggingContainerService{})
	req := httptest.NewRequest(http.MethodPost, "/v1/containers", nil)
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", rec.Code)
	}
	out := logs.String()
	if !strings.Contains(out, "container lifecycle request failed") {
		t.Fatalf("expected lifecycle failure log message, got %q", out)
	}
	if !strings.Contains(out, "\"component\":\"api_containers\"") {
		t.Fatalf("expected component field in logs, got %q", out)
	}
	if strings.Contains(out, "super-secret") || strings.Contains(out, "python -c") {
		t.Fatalf("expected sensitive details to be redacted from logs, got %q", out)
	}
	if !strings.Contains(out, "\"error_summary\":\"redacted\"") {
		t.Fatalf("expected redacted error summary, got %q", out)
	}
}
