package health

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"autonomy-platform/internal/metrics"
)

// CheckFunc is a health check function that returns nil if healthy.
type CheckFunc func() error

// Server provides a simple HTTP health check endpoint.
type Server struct {
	port    string
	service string
	checks  []CheckFunc
	start   time.Time
}

// New creates a health server on the given port.
func New(port, service string, checks ...CheckFunc) *Server {
	return &Server{
		port:    port,
		service: service,
		checks:  checks,
		start:   time.Now(),
	}
}

type healthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Uptime  string `json:"uptime"`
	Error   string `json:"error,omitempty"`
}

// Start runs the HTTP server in a goroutine. Returns immediately.
func (s *Server) Start() {
	metrics.MustRegister()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.Handle("/metrics", metrics.Handler())

	go func() {
		addr := ":" + s.port
		slog.Info("health endpoint starting", "port", s.port, "paths", []string{"/health", "/metrics"})
		if err := http.ListenAndServe(addr, mux); err != nil {
			slog.Error("health server failed", "error", err)
		}
	}()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := healthResponse{
		Status:  "ok",
		Service: s.service,
		Uptime:  fmt.Sprintf("%.0fs", time.Since(s.start).Seconds()),
	}

	for _, check := range s.checks {
		if err := check(); err != nil {
			resp.Status = "unhealthy"
			resp.Error = err.Error()
			w.WriteHeader(http.StatusServiceUnavailable)
			break
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
