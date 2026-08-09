package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Blue-Door-Software-Group/containers4agents/internal/files"
	obslog "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/logging"
)

type loggingFileService struct{}

func (s loggingFileService) Upload(context.Context, string, string, io.Reader) error { return nil }
func (s loggingFileService) Delete(context.Context, string, string) error            { return nil }
func (s loggingFileService) Manifest(context.Context, string) (*files.Manifest, error) {
	return &files.Manifest{}, nil
}
func (s loggingFileService) Download(_ context.Context, _ string, _ string, dst io.Writer) error {
	_, _ = dst.Write([]byte("partial"))
	return errors.New("docker exec '/workspace/secret.txt' rm -rf / && payload=super-secret")
}

type loggingFileErrorService struct{}

func (s loggingFileErrorService) Upload(context.Context, string, string, io.Reader) error {
	return files.ErrProviderFailure
}
func (s loggingFileErrorService) Delete(context.Context, string, string) error {
	return files.ErrProviderFailure
}
func (s loggingFileErrorService) Manifest(context.Context, string) (*files.Manifest, error) {
	return nil, fmt.Errorf("%w: exec secret-token command=/workspace/private.txt", files.ErrProviderFailure)
}
func (s loggingFileErrorService) Download(context.Context, string, string, io.Writer) error {
	return files.ErrProviderFailure
}

func TestDownloadStreamLogRedactsSensitiveErrorDetails(t *testing.T) {
	var logs bytes.Buffer
	oldDefault := slog.Default()
	slog.SetDefault(obslog.NewWithWriter("info", &logs))
	defer slog.SetDefault(oldDefault)

	h := NewFileHandler(loggingFileService{}, 1024)
	req := httptest.NewRequest(http.MethodGet, "/v1/containers/c1/files/secret.txt", nil)
	req.SetPathValue("id", "c1")
	req.SetPathValue("path", "secret.txt")
	rec := httptest.NewRecorder()

	h.Download(rec, req)

	out := logs.String()
	if strings.Contains(out, "secret.txt") {
		t.Fatalf("expected logs not to include file path, got %q", out)
	}
	if strings.Contains(out, "payload=super-secret") {
		t.Fatalf("expected logs not to include raw provider payload, got %q", out)
	}
	if !strings.Contains(out, "\"error_category\":\"internal_error\"") {
		t.Fatalf("expected sanitized error category field, got %q", out)
	}
}

func TestManifestProviderFailureIsLogged(t *testing.T) {
	var logs bytes.Buffer
	oldDefault := slog.Default()
	slog.SetDefault(obslog.NewWithWriter("info", &logs))
	defer slog.SetDefault(oldDefault)

	h := NewFileHandler(loggingFileErrorService{}, 1024)
	req := httptest.NewRequest(http.MethodGet, "/v1/containers/c1/files/manifest", nil)
	req.SetPathValue("id", "c1")
	rec := httptest.NewRecorder()

	h.Manifest(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", rec.Code)
	}
	out := logs.String()
	if !strings.Contains(out, "file request failed") {
		t.Fatalf("expected file failure log message, got %q", out)
	}
	if !strings.Contains(out, "\"component\":\"api_files\"") {
		t.Fatalf("expected component field in logs, got %q", out)
	}
	if strings.Contains(out, "secret-token") || strings.Contains(out, "private.txt") {
		t.Fatalf("expected sensitive details to be redacted from logs, got %q", out)
	}
	if !strings.Contains(out, "\"error_summary\":\"redacted\"") {
		t.Fatalf("expected redacted error summary, got %q", out)
	}
}
