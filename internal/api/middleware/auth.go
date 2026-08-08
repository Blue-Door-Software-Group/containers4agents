package middleware

import (
	"net/http"

	apierrors "github.com/Blue-Door-Software-Group/containers4agents/internal/api/errors"
	obsmetrics "github.com/Blue-Door-Software-Group/containers4agents/internal/observability/metrics"
)

type Authorizer interface {
	// IsAuthorized evaluates the raw Authorization header value.
	// Implementations are expected to enforce Bearer token semantics.
	IsAuthorized(authHeader string) bool
}

func Auth(authorizer Authorizer, metrics ...*obsmetrics.Metrics) func(http.Handler) http.Handler {
	var m *obsmetrics.Metrics
	if len(metrics) > 0 {
		m = metrics[0]
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if !authorizer.IsAuthorized(authHeader) {
				reason := "invalid_credentials"
				if authHeader == "" {
					reason = "missing_authorization_header"
				}
				if m != nil {
					m.RecordAuthFailure(reason)
				}
				requestID := GetRequestID(r.Context())
				apierrors.Write(w, requestID, apierrors.Unauthorized("unauthorized"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
