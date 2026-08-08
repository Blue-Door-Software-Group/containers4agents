package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	apierrors "github.com/Blue-Door-Software-Group/containers4agents/internal/api/errors"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/handlers"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/api/middleware"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/config"
	"github.com/Blue-Door-Software-Group/containers4agents/internal/observability/metrics"
)

type Server struct {
	httpServer *http.Server
	logger     *slog.Logger
}

func New(cfg config.Config, logger *slog.Logger, handler http.Handler) *Server {
	return &Server{
		httpServer: &http.Server{
			Addr:              cfg.HTTPListenAddr,
			Handler:           handler,
			ReadHeaderTimeout: cfg.ReadTimeout,
			WriteTimeout:      cfg.WriteTimeout,
			IdleTimeout:       cfg.IdleTimeout,
		},
		logger: logger,
	}
}

func NewHandler(
	m *metrics.Metrics,
	authMw func(http.Handler) http.Handler,
	containerHandler *handlers.ContainerHandler,
) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", m.Middleware("/healthz", http.HandlerFunc(handlers.Health)))
	mux.Handle("GET /metrics", m.Handler())
	if authMw != nil && containerHandler != nil {
		mux.Handle("POST /v1/containers", m.Middleware("/v1/containers:create", authMw(http.HandlerFunc(containerHandler.Create))))
		mux.Handle("GET /v1/containers/{id}", m.Middleware("/v1/containers:get", authMw(http.HandlerFunc(containerHandler.GetStatus))))
		mux.Handle("POST /v1/containers/{id}/stop", m.Middleware("/v1/containers:stop", authMw(http.HandlerFunc(containerHandler.Stop))))
		mux.Handle("POST /v1/containers/{id}/start", m.Middleware("/v1/containers:start", authMw(http.HandlerFunc(containerHandler.Start))))
		mux.Handle("POST /v1/containers/{id}/prune", m.Middleware("/v1/containers:prune", authMw(http.HandlerFunc(containerHandler.Prune))))
	}
	mux.Handle("/", m.Middleware("/*", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := middleware.GetRequestID(r.Context())
		apierrors.Write(w, requestID, apierrors.NotFound("resource not found"))
	})))

	return middleware.RequestID(mux)
}

func (s *Server) Start() error {
	s.logger.Info("http server starting", "addr", s.httpServer.Addr)
	err := s.httpServer.ListenAndServe()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("listen and serve: %w", err)
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}
