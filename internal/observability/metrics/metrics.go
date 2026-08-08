package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	Registry *prometheus.Registry
	Requests *prometheus.CounterVec
	Duration *prometheus.HistogramVec
}

func New() *Metrics {
	registry := prometheus.NewRegistry()
	m := &Metrics{
		Registry: registry,
		Requests: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "c4a_http_requests_total",
				Help: "Total number of HTTP requests by method, route, and status.",
			},
			[]string{"method", "route", "status"},
		),
		Duration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "c4a_http_request_duration_seconds",
				Help:    "HTTP request duration in seconds by method and route.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"method", "route"},
		),
	}
	registry.MustRegister(m.Requests, m.Duration)
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}

func (m *Metrics) Middleware(route string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(recorder, r)

		status := strconv.Itoa(recorder.statusCode)
		m.Requests.WithLabelValues(r.Method, route, status).Inc()
		m.Duration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}
