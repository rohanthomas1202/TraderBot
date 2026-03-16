package dashboard

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"autonomy-platform/gen/watchdogpb"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed static/*
var staticFS embed.FS

// Server is the dashboard HTTP server.
type Server struct {
	db       *pgxpool.Pool
	hub      *Hub
	watchdog watchdogpb.WatchdogClient
	apiKey   string
	logger   *slog.Logger
}

// NewServer creates a dashboard server.
func NewServer(db *pgxpool.Pool, hub *Hub, watchdog watchdogpb.WatchdogClient, apiKey string, logger *slog.Logger) *Server {
	return &Server{
		db:       db,
		hub:      hub,
		watchdog: watchdog,
		apiKey:   apiKey,
		logger:   logger,
	}
}

// Handler returns the HTTP handler for the dashboard.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Static files
	staticContent, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /", http.FileServer(http.FS(staticContent)))

	// Auth
	mux.HandleFunc("POST /api/login", s.handleLogin)

	// Data endpoints (require auth)
	mux.HandleFunc("GET /api/orders", s.requireAuth(s.handleOrders))
	mux.HandleFunc("GET /api/positions", s.requireAuth(s.handlePositions))
	mux.HandleFunc("GET /api/audit", s.requireAuth(s.handleAudit))
	mux.HandleFunc("GET /api/halts", s.requireAuth(s.handleHalts))
	mux.HandleFunc("GET /api/risk", s.requireAuth(s.handleRiskStats))

	// Control endpoints (require auth)
	mux.HandleFunc("POST /api/kill", s.requireAuth(s.handleKill))
	mux.HandleFunc("POST /api/ack", s.requireAuth(s.handleAck))
	mux.HandleFunc("POST /api/resume", s.requireAuth(s.handleResume))

	// WebSocket
	mux.HandleFunc("GET /ws", s.requireAuth(s.handleWS))

	return mux
}

// --- Auth ---

func (s *Server) sessionToken() string {
	mac := hmac.New(sha256.New, []byte(s.apiKey))
	mac.Write([]byte("dashboard-session"))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.APIKey != s.apiKey {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "session",
		Value:    s.sessionToken(),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400, // 24 hours
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("session")
		if err != nil || cookie.Value != s.sessionToken() {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// --- Data Handlers ---

func (s *Server) handleOrders(w http.ResponseWriter, r *http.Request) {
	orders, err := QueryRecentOrders(r.Context(), s.db, 50)
	if err != nil {
		s.logger.Error("query orders failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, orders)
}

func (s *Server) handlePositions(w http.ResponseWriter, r *http.Request) {
	positions, err := QueryPositions(r.Context(), s.db)
	if err != nil {
		s.logger.Error("query positions failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, positions)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	entries, err := QueryRecentAudit(r.Context(), s.db, 30)
	if err != nil {
		s.logger.Error("query audit failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, entries)
}

func (s *Server) handleHalts(w http.ResponseWriter, r *http.Request) {
	halts, err := QueryActiveHalts(r.Context(), s.db)
	if err != nil {
		s.logger.Error("query halts failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, halts)
}

func (s *Server) handleRiskStats(w http.ResponseWriter, r *http.Request) {
	stats, err := QueryRiskStats(r.Context(), s.db)
	if err != nil {
		s.logger.Error("query risk stats failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, stats)
}

// --- Control Handlers ---

func (s *Server) handleKill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Level string `json:"level"`
		Scope string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Level == "" {
		req.Level = "cancel_only"
	}
	if req.Scope == "" {
		req.Scope = "global"
	}

	levelMap := map[string]watchdogpb.KillSwitchLevel{
		"soft_pause":  watchdogpb.KillSwitchLevel_KILL_LEVEL_SOFT_PAUSE,
		"cancel_only": watchdogpb.KillSwitchLevel_KILL_LEVEL_CANCEL_ONLY,
		"full_stop":   watchdogpb.KillSwitchLevel_KILL_LEVEL_FULL_STOP,
	}
	level, ok := levelMap[req.Level]
	if !ok {
		http.Error(w, "invalid level", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	_, err := s.watchdog.TriggerKillSwitch(ctx, &watchdogpb.KillSwitchRequest{
		Level:       level,
		Scope:       req.Scope,
		Reason:      "dashboard kill switch",
		TriggeredBy: "dashboard",
	})
	if err != nil {
		s.logger.Error("kill switch failed", "error", err)
		http.Error(w, fmt.Sprintf("kill switch failed: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "triggered"})
}

func (s *Server) handleAck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scope     string `json:"scope"`
		RootCause string `json:"root_cause"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Scope == "" {
		req.Scope = "global"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	_, err := s.watchdog.AcknowledgeHalt(ctx, &watchdogpb.AcknowledgeHaltRequest{
		Scope:          req.Scope,
		AcknowledgedBy: "dashboard",
		RootCause:      req.RootCause,
	})
	if err != nil {
		s.logger.Error("ack failed", "error", err)
		http.Error(w, fmt.Sprintf("ack failed: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "acknowledged"})
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scope string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.Scope == "" {
		req.Scope = "global"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	_, err := s.watchdog.ResumeTrading(ctx, &watchdogpb.ResumeTradingRequest{
		Scope:     req.Scope,
		ResumedBy: "dashboard",
	})
	if err != nil {
		s.logger.Error("resume failed", "error", err)
		http.Error(w, fmt.Sprintf("resume failed: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "resumed"})
}

// --- WebSocket ---

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		s.logger.Error("websocket accept failed", "error", err)
		return
	}
	defer conn.CloseNow()

	s.hub.ServeClient(r.Context(), conn)
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
