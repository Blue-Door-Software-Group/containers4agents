package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	apierrors "github.com/Blue-Door-Software-Group/containers4agents/internal/api/errors"
	obsmetrics "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type staticAuthorizer struct {
	allow bool
}

func (a staticAuthorizer) IsAuthorized(_ string) bool {
	return a.allow
}

func TestAuthMiddlewareRejectsUnauthorized(t *testing.T) {
	protected := Auth(staticAuthorizer{allow: false})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	handler := RequestID(protected)

	req := httptest.NewRequest(http.MethodGet, "/v1/containers", nil)
	req.Header.Set(RequestIDHeader, "req-auth-1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rec.Code)
	}

	var payload apierrors.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if payload.Code != "unauthorized" {
		t.Fatalf("expected unauthorized code, got %s", payload.Code)
	}
	if payload.RequestID != "req-auth-1" {
		t.Fatalf("expected request ID req-auth-1, got %s", payload.RequestID)
	}
}

func TestAuthMiddlewareAllowsAuthorized(t *testing.T) {
	protected := Auth(staticAuthorizer{allow: true})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	handler := RequestID(protected)

	req := httptest.NewRequest(http.MethodGet, "/v1/containers", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d", rec.Code)
	}
}

func TestAuthMiddlewareRecordsFailureMetric(t *testing.T) {
	m := obsmetrics.New()
	protected := Auth(staticAuthorizer{allow: false}, m)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	handler := RequestID(protected)

	req := httptest.NewRequest(http.MethodGet, "/v1/containers", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", rec.Code)
	}
	if got := testutil.ToFloat64(m.AuthFailures.WithLabelValues("missing_authorization_header")); got != 1 {
		t.Fatalf("expected missing auth header metric to be 1, got %v", got)
	}
}
