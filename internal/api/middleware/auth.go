package middleware

import (
	"net/http"

	apierrors "github.com/Blue-Door-Software-Group/containers4agents/internal/api/errors"
)

type Authorizer interface {
	IsAuthorized(authHeader string) bool
}

func Auth(authorizer Authorizer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !authorizer.IsAuthorized(r.Header.Get("Authorization")) {
				requestID := GetRequestID(r.Context())
				apierrors.Write(w, requestID, apierrors.Unauthorized("unauthorized"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
