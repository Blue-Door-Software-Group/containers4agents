package handlers

import (
	"bytes"
	"context"
	"errors"
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
