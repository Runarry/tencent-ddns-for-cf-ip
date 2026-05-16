package api

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/config"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/subscription"
	syncsvc "github.com/sleep/tencent-ddns-for-cf-ip/internal/sync"
)

type Config struct {
	Token         string
	Subscriptions []config.SubscriptionConfig
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
	if hasEnabledSubscriptions(cfg.Subscriptions) {
		mux.HandleFunc("GET /sub/{token}", s.subscription)
	}
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

func (s *Server) subscription(w http.ResponseWriter, r *http.Request) {
	sub, ok := findSubscription(s.cfg.Subscriptions, r.PathValue("token"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !subscriptionKeyMatches(r.URL.Query().Get("key"), sub.Key) {
		writeText(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	nodeIDs, ok := effectiveSubscriptionNodeIDs(sub.NodeIDs, r.URL.Query()["nodeids"])
	if !ok {
		writeText(w, http.StatusServiceUnavailable, subscription.ErrNoTargets.Error())
		return
	}
	body, err := subscription.Generate(subscription.Config{
		Shares:  sub.Shares,
		Format:  sub.Format,
		NodeIDs: nodeIDs,
	}, s.service.Records())
	if errors.Is(err, subscription.ErrNoTargets) {
		writeText(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if errors.Is(err, subscription.ErrNoValidShares) {
		writeText(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err != nil {
		writeText(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeText(w, http.StatusOK, body)
}

func hasEnabledSubscriptions(subscriptions []config.SubscriptionConfig) bool {
	for _, sub := range subscriptions {
		if sub.Enabled {
			return true
		}
	}
	return false
}

func findSubscription(subscriptions []config.SubscriptionConfig, token string) (config.SubscriptionConfig, bool) {
	for _, sub := range subscriptions {
		if sub.Enabled && sub.PublicToken == token {
			return sub, true
		}
	}
	return config.SubscriptionConfig{}, false
}

func subscriptionKeyMatches(got string, want string) bool {
	got = strings.TrimSpace(got)
	want = strings.TrimSpace(want)
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func effectiveSubscriptionNodeIDs(configured []string, requestedValues []string) ([]string, bool) {
	requested := normalizeRequestedNodeIDs(requestedValues)
	if len(requested) == 0 {
		return configured, true
	}
	if len(configured) == 0 {
		return requested, true
	}

	allowed := map[string]struct{}{}
	for _, nodeID := range configured {
		nodeID = strings.ToLower(strings.TrimSpace(nodeID))
		if nodeID != "" {
			allowed[nodeID] = struct{}{}
		}
	}

	filtered := make([]string, 0, len(requested))
	for _, nodeID := range requested {
		if _, ok := allowed[nodeID]; ok {
			filtered = append(filtered, nodeID)
		}
	}
	return filtered, len(filtered) > 0
}

func normalizeRequestedNodeIDs(values []string) []string {
	seen := map[string]struct{}{}
	nodeIDs := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			nodeID := strings.ToLower(strings.TrimSpace(part))
			if nodeID == "" {
				continue
			}
			if _, ok := seen[nodeID]; ok {
				continue
			}
			seen[nodeID] = struct{}{}
			nodeIDs = append(nodeIDs, nodeID)
		}
	}
	return nodeIDs
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

func writeText(w http.ResponseWriter, status int, value string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(value))
}
