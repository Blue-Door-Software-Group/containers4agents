package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "github.com/Blue-Door-Software-Group/containers4agents/internal/api/errors"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/middleware"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/observability/metrics"
)

func TestHealthzAndMetricsAvailable(t *testing.T) {
	handler := NewHandler(metrics.New(), nil, nil, nil, nil)
	testServer := httptest.NewServer(handler)
	defer testServer.Close()

	healthResp, err := http.Get(testServer.URL + "/healthz")
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	defer healthResp.Body.Close()

	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("expected health status 200, got %d", healthResp.StatusCode)
	}

	metricsResp, err := http.Get(testServer.URL + "/metrics")
	if err != nil {
		t.Fatalf("metrics request failed: %v", err)
	}
	defer metricsResp.Body.Close()

	if metricsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected metrics status 200, got %d", metricsResp.StatusCode)
	}
	body, err := io.ReadAll(metricsResp.Body)
	if err != nil {
		t.Fatalf("read metrics body: %v", err)
	}
	required := [][]byte{
		[]byte("c4a_http_requests_total"),
		[]byte("c4a_running_containers"),
		[]byte("c4a_stopped_containers"),
		[]byte("c4a_total_containers"),
		[]byte("c4a_exec_in_progress"),
		[]byte("c4a_reconcile_leader"),
	}
	for _, name := range required {
		if !bytes.Contains(body, name) {
			t.Fatalf("expected metrics output to contain %q", string(name))
		}
	}
}

func TestNotFoundReturnsStructuredErrorAndRequestID(t *testing.T) {
	handler := NewHandler(metrics.New(), nil, nil, nil, nil)
	testServer := httptest.NewServer(handler)
	defer testServer.Close()

	req, err := http.NewRequest(http.MethodGet, testServer.URL+"/does-not-exist", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set(middleware.RequestIDHeader, "req-integration-1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected status 404, got %d", resp.StatusCode)
	}

	if got := resp.Header.Get(middleware.RequestIDHeader); got != "req-integration-1" {
		t.Fatalf("expected response request id %q, got %q", "req-integration-1", got)
	}

	var payload apierrors.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload.Code != "not_found" {
		t.Fatalf("expected code not_found, got %s", payload.Code)
	}
	if !strings.Contains(payload.Message, "not found") {
		t.Fatalf("expected not found message, got %q", payload.Message)
	}
	if payload.RequestID != "req-integration-1" {
		t.Fatalf("expected request_id req-integration-1, got %s", payload.RequestID)
	}
}
