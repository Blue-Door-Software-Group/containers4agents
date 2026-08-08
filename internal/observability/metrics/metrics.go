package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Metrics struct {
	Registry *prometheus.Registry

	Requests *prometheus.CounterVec
	Duration *prometheus.HistogramVec

	LifecycleTransitions *prometheus.CounterVec
	LifecycleDuration    *prometheus.HistogramVec
	ExecutionTotal       *prometheus.CounterVec
	ExecutionDuration    *prometheus.HistogramVec
	CapacityEvents       *prometheus.CounterVec
	AuthFailures         *prometheus.CounterVec
	ReconcileRuns        *prometheus.CounterVec
	ReconcileActions     *prometheus.CounterVec
	ProviderFailures     *prometheus.CounterVec
	FileTransferBytes    *prometheus.CounterVec
	FileTransferDuration *prometheus.HistogramVec
	WorkspaceBytes       prometheus.Histogram
	WorkspaceFiles       prometheus.Histogram

	RunningContainers   prometheus.Gauge
	StoppedContainers   prometheus.Gauge
	TotalContainers     prometheus.Gauge
	ExecInProgress      prometheus.Gauge
	ReconcileLeadership prometheus.Gauge
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
		LifecycleTransitions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "c4a_lifecycle_transitions_total",
				Help: "Total lifecycle operation outcomes by operation and outcome.",
			},
			[]string{"operation", "outcome"},
		),
		LifecycleDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "c4a_lifecycle_duration_seconds",
				Help:    "Lifecycle operation duration in seconds by operation and outcome.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"operation", "outcome"},
		),
		ExecutionTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "c4a_exec_total",
				Help: "Total execution requests by outcome.",
			},
			[]string{"outcome"},
		),
		ExecutionDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "c4a_exec_duration_seconds",
				Help:    "Execution duration in seconds by outcome.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"outcome"},
		),
		CapacityEvents: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "c4a_capacity_events_total",
				Help: "Capacity and reclamation events by event and outcome.",
			},
			[]string{"event", "outcome"},
		),
		AuthFailures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "c4a_auth_failures_total",
				Help: "Authentication failures by bounded reason.",
			},
			[]string{"reason"},
		),
		ReconcileRuns: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "c4a_reconcile_runs_total",
				Help: "Reconcile loop runs by outcome.",
			},
			[]string{"outcome"},
		),
		ReconcileActions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "c4a_reconcile_actions_total",
				Help: "Reconcile actions by action and outcome.",
			},
			[]string{"action", "outcome"},
		),
		ProviderFailures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "c4a_provider_failures_total",
				Help: "Provider failures by operation and category.",
			},
			[]string{"operation", "category"},
		),
		FileTransferBytes: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "c4a_file_transfer_bytes_total",
				Help: "File transfer bytes by direction and outcome.",
			},
			[]string{"direction", "outcome"},
		),
		FileTransferDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "c4a_file_transfer_duration_seconds",
				Help:    "File transfer duration in seconds by direction and outcome.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"direction", "outcome"},
		),
		WorkspaceBytes: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "c4a_workspace_bytes",
				Help:    "Observed workspace bytes during manifest/read-usage operations.",
				Buckets: []float64{1024, 10 * 1024, 100 * 1024, 1024 * 1024, 10 * 1024 * 1024, 100 * 1024 * 1024, 1024 * 1024 * 1024},
			},
		),
		WorkspaceFiles: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "c4a_workspace_file_count",
				Help:    "Observed workspace file count during manifest/read-usage operations.",
				Buckets: []float64{1, 10, 100, 500, 1000, 5000, 10000},
			},
		),
		RunningContainers: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "c4a_running_containers",
				Help: "Current number of running containers observed by this control-plane instance.",
			},
		),
		StoppedContainers: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "c4a_stopped_containers",
				Help: "Current number of stopped containers observed by this control-plane instance.",
			},
		),
		TotalContainers: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "c4a_total_containers",
				Help: "Current total number of containers observed by this control-plane instance.",
			},
		),
		ExecInProgress: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "c4a_exec_in_progress",
				Help: "Current number of executions in progress observed by this control-plane instance.",
			},
		),
		ReconcileLeadership: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "c4a_reconcile_leader",
				Help: "Whether this instance currently holds reconcile leadership (1=true, 0=false).",
			},
		),
	}
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.Requests,
		m.Duration,
		m.LifecycleTransitions,
		m.LifecycleDuration,
		m.ExecutionTotal,
		m.ExecutionDuration,
		m.CapacityEvents,
		m.AuthFailures,
		m.ReconcileRuns,
		m.ReconcileActions,
		m.ProviderFailures,
		m.FileTransferBytes,
		m.FileTransferDuration,
		m.WorkspaceBytes,
		m.WorkspaceFiles,
		m.RunningContainers,
		m.StoppedContainers,
		m.TotalContainers,
		m.ExecInProgress,
		m.ReconcileLeadership,
	)
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

func (m *Metrics) ObserveLifecycle(operation string, outcome string, d time.Duration) {
	if m == nil {
		return
	}
	m.LifecycleTransitions.WithLabelValues(operation, outcome).Inc()
	m.LifecycleDuration.WithLabelValues(operation, outcome).Observe(d.Seconds())
}

func (m *Metrics) ObserveExecution(outcome string, d time.Duration) {
	if m == nil {
		return
	}
	m.ExecutionTotal.WithLabelValues(outcome).Inc()
	m.ExecutionDuration.WithLabelValues(outcome).Observe(d.Seconds())
}

func (m *Metrics) RecordCapacityEvent(event string, outcome string) {
	if m == nil {
		return
	}
	m.CapacityEvents.WithLabelValues(event, outcome).Inc()
}

func (m *Metrics) RecordAuthFailure(reason string) {
	if m == nil {
		return
	}
	m.AuthFailures.WithLabelValues(reason).Inc()
}

func (m *Metrics) RecordReconcileRun(outcome string) {
	if m == nil {
		return
	}
	m.ReconcileRuns.WithLabelValues(outcome).Inc()
}

func (m *Metrics) RecordReconcileAction(action string, outcome string) {
	if m == nil {
		return
	}
	m.ReconcileActions.WithLabelValues(action, outcome).Inc()
}

func (m *Metrics) RecordProviderFailure(operation string, category string) {
	if m == nil {
		return
	}
	m.ProviderFailures.WithLabelValues(operation, category).Inc()
}

func (m *Metrics) ObserveFileTransfer(direction string, outcome string, bytes int64, d time.Duration) {
	if m == nil {
		return
	}
	m.FileTransferBytes.WithLabelValues(direction, outcome).Add(float64(bytes))
	m.FileTransferDuration.WithLabelValues(direction, outcome).Observe(d.Seconds())
}

func (m *Metrics) ObserveWorkspaceUsage(bytes int64, files int64) {
	if m == nil {
		return
	}
	m.WorkspaceBytes.Observe(float64(bytes))
	m.WorkspaceFiles.Observe(float64(files))
}

func (m *Metrics) SetContainerCounts(running int, stopped int, total int) {
	if m == nil {
		return
	}
	m.RunningContainers.Set(float64(running))
	m.StoppedContainers.Set(float64(stopped))
	m.TotalContainers.Set(float64(total))
}

func (m *Metrics) SetExecInProgress(count int) {
	if m == nil {
		return
	}
	m.ExecInProgress.Set(float64(count))
}

func (m *Metrics) SetReconcileLeader(leader bool) {
	if m == nil {
		return
	}
	if leader {
		m.ReconcileLeadership.Set(1)
		return
	}
	m.ReconcileLeadership.Set(0)
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}
