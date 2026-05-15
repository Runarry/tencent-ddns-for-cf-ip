package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/config"
	syncsvc "github.com/sleep/tencent-ddns-for-cf-ip/internal/sync"
)

type Config struct {
	Token string
}

type Server struct {
	cfg     Config
	service *syncsvc.Service
	config  config.Config
}

func NewServer(cfg Config, service *syncsvc.Service, redacted config.Config) http.Handler {
	s := &Server{cfg: cfg, service: service, config: redacted}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /api/v1/update", s.withAuth(s.update))
	mux.HandleFunc("GET /api/v1/records", s.withAuth(s.records))
	mux.HandleFunc("GET /api/v1/status", s.withAuth(s.status))
	mux.HandleFunc("GET /api/v1/config", s.withAuth(s.configHandler))
	return mux
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) update(w http.ResponseWriter, r *http.Request) {
	summary, err := s.service.Run(r.Context())
	if errors.Is(err, syncsvc.ErrUpdateInProgress) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, summary)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) records(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"records": s.service.Records()})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.service.Status())
}

func (s *Server) configHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.config)
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimSpace(r.Header.Get("Authorization"))
		want := "Bearer " + s.cfg.Token
		if got != want {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
