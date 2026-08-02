package api

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/config"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/provider"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/state"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/subscription"
	subscriptions "github.com/sleep/tencent-ddns-for-cf-ip/internal/subscriptions"
	syncsvc "github.com/sleep/tencent-ddns-for-cf-ip/internal/sync"
)

const maxCustomCSVUploadBytes int64 = 2 * 1024 * 1024

type Config struct {
	Token               string
	Subscriptions       []config.SubscriptionConfig
	SubscriptionManager *subscriptions.Manager
	CustomCSV           CustomCSVConfig
}

type CustomCSVConfig struct {
	Enabled bool
	Path    string
	TopN    int
}

type Server struct {
	cfg                 Config
	service             *syncsvc.Service
	config              config.Config
	subscriptionManager *subscriptions.Manager
	staticSubscriptions []config.SubscriptionConfig
}

type speedTestPreset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type temporarySpeedTestRequest struct {
	URL string `json:"url"`
}

type customCSVUpdateResponse struct {
	Saved      bool             `json:"saved"`
	Path       string           `json:"path"`
	Candidates int              `json:"candidates"`
	Sync       *syncsvc.Summary `json:"sync,omitempty"`
	Error      string           `json:"error,omitempty"`
}

var speedTestPresets = []speedTestPreset{
	{Name: "Cloudflare 1MB", URL: "https://speed.cloudflare.com/__down?bytes=1048576"},
	{Name: "Cloudflare 10MB", URL: "https://speed.cloudflare.com/__down?bytes=10485760"},
	{Name: "Cloudflare 50MB", URL: "https://speed.cloudflare.com/__down?bytes=52428800"},
	{Name: "Cloudflare 100MB", URL: "https://speed.cloudflare.com/__down?bytes=104857600"},
}

func NewServer(cfg Config, service *syncsvc.Service, redacted config.Config) http.Handler {
	s := &Server{
		cfg:                 cfg,
		service:             service,
		config:              redacted,
		subscriptionManager: cfg.SubscriptionManager,
		staticSubscriptions: append([]config.SubscriptionConfig(nil), cfg.Subscriptions...),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /api/v1/update", s.withAuth(s.update))
	mux.HandleFunc("GET /api/v1/records", s.withAuth(s.records))
	mux.HandleFunc("GET /api/v1/status", s.withAuth(s.status))
	mux.HandleFunc("GET /api/v1/config", s.withAuth(s.configHandler))
	mux.HandleFunc("GET /api/v1/admin/subscriptions", s.withAuth(s.adminListSubscriptions))
	mux.HandleFunc("POST /api/v1/admin/subscriptions", s.withAuth(s.adminCreateSubscription))
	mux.HandleFunc("GET /api/v1/admin/subscriptions/{id}", s.withAuth(s.adminGetSubscription))
	mux.HandleFunc("PUT /api/v1/admin/subscriptions/{id}", s.withAuth(s.adminUpdateSubscription))
	mux.HandleFunc("DELETE /api/v1/admin/subscriptions/{id}", s.withAuth(s.adminDeleteSubscription))
	mux.HandleFunc("POST /api/v1/admin/subscriptions/{id}/rotate-secret", s.withAuth(s.adminRotateSubscriptionSecret))
	mux.HandleFunc("POST /api/v1/admin/subscriptions/{id}/restore", s.withAuth(s.adminRestoreSubscription))
	mux.HandleFunc("POST /api/v1/admin/subscriptions/{id}/refresh-sources", s.withAuth(s.adminRefreshSubscriptionSources))
	mux.HandleFunc("POST /api/v1/admin/subscriptions/{id}/preview", s.withAuth(s.adminPreviewSubscription))
	mux.HandleFunc("POST /api/v1/admin/subscriptions/{id}/reveal-url", s.withAuth(s.adminRevealSubscriptionURL))
	mux.HandleFunc("PUT /api/v1/admin/custom-ips/csv", s.withAuth(s.adminUpdateCustomCSV))
	mux.HandleFunc("GET /api/v1/admin/speed-test-presets", s.withAuth(s.adminSpeedTestPresets))
	mux.HandleFunc("POST /api/v1/admin/speed-tests", s.withAuth(s.adminRunTemporarySpeedTest))
	mux.HandleFunc("POST /api/v1/admin/speed-tests/{id}/apply", s.withAuth(s.adminApplyTemporarySpeedTest))
	mux.HandleFunc("GET /sub/{token}", s.subscription)
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
	sub, ok := s.findSubscription(r.PathValue("token"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !subscriptionKeyMatches(r.URL.Query().Get("key"), sub.Key) {
		writeText(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	nodeIDs := sub.NodeIDs
	if !isMergeSubscription(sub) {
		var ok bool
		nodeIDs, ok = effectiveSubscriptionNodeIDs(sub.NodeIDs, r.URL.Query()["nodeids"])
		if !ok {
			writeText(w, http.StatusServiceUnavailable, subscription.ErrNoTargets.Error())
			return
		}
	}
	body, err := subscription.Generate(subscription.Config{
		Shares:  sub.Shares,
		Format:  sub.Format,
		NodeIDs: nodeIDs,
		Mode:    sub.Mode,
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

func (s *Server) adminListSubscriptions(w http.ResponseWriter, r *http.Request) {
	manager, ok := s.requireSubscriptionManager(w)
	if !ok {
		return
	}
	items := manager.List(requestBaseURL(r))
	var records []state.Record
	if s.service != nil {
		records = s.service.Records()
	}
	for i := range items {
		if sub, exists := manager.ConfigForID(items[i].ID); exists {
			items[i].OutputCount = generatedSubscriptionLineCount(sub, records)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subscriptions":      items,
		"override_conflicts": manager.OverrideConflicts(),
	})
}

func (s *Server) adminCreateSubscription(w http.ResponseWriter, r *http.Request) {
	manager, ok := s.requireSubscriptionManager(w)
	if !ok {
		return
	}
	var req subscriptions.UpsertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := manager.Create(req, requestBaseURL(r))
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) adminGetSubscription(w http.ResponseWriter, r *http.Request) {
	manager, ok := s.requireSubscriptionManager(w)
	if !ok {
		return
	}
	item, err := manager.Get(r.PathValue("id"), requestBaseURL(r))
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) adminUpdateSubscription(w http.ResponseWriter, r *http.Request) {
	manager, ok := s.requireSubscriptionManager(w)
	if !ok {
		return
	}
	var req subscriptions.UpsertRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	item, err := manager.Update(r.PathValue("id"), req, requestBaseURL(r))
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) adminDeleteSubscription(w http.ResponseWriter, r *http.Request) {
	manager, ok := s.requireSubscriptionManager(w)
	if !ok {
		return
	}
	if err := manager.Delete(r.PathValue("id")); err != nil {
		writeSubscriptionError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminRotateSubscriptionSecret(w http.ResponseWriter, r *http.Request) {
	manager, ok := s.requireSubscriptionManager(w)
	if !ok {
		return
	}
	var req subscriptions.RotateRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
	}
	result, err := manager.RotateSecret(r.PathValue("id"), req.Target, requestBaseURL(r))
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) adminRestoreSubscription(w http.ResponseWriter, r *http.Request) {
	manager, ok := s.requireSubscriptionManager(w)
	if !ok {
		return
	}
	item, err := manager.Restore(r.PathValue("id"), requestBaseURL(r))
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func (s *Server) adminRefreshSubscriptionSources(w http.ResponseWriter, r *http.Request) {
	manager, ok := s.requireSubscriptionManager(w)
	if !ok {
		return
	}
	draft, err := optionalSubscriptionUpsertRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	var item subscriptions.ListItem
	if draft == nil {
		item, err = manager.RefreshSources(r.Context(), r.PathValue("id"), requestBaseURL(r))
	} else {
		item, err = manager.RefreshDraftSources(r.Context(), r.PathValue("id"), draft, requestBaseURL(r))
	}
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

type subscriptionPreviewEntry struct {
	Protocol string `json:"protocol"`
	Outcome  string `json:"outcome"`
}

type subscriptionPreviewResponse struct {
	ResolvedCount    int                        `json:"resolved_count"`
	OutputCount      int                        `json:"output_count"`
	RewrittenCount   int                        `json:"rewritten_count"`
	PassthroughCount int                        `json:"passthrough_count"`
	Warnings         []string                   `json:"warnings,omitempty"`
	Entries          []subscriptionPreviewEntry `json:"entries"`
}

func (s *Server) adminPreviewSubscription(w http.ResponseWriter, r *http.Request) {
	manager, ok := s.requireSubscriptionManager(w)
	if !ok {
		return
	}
	draft, err := optionalSubscriptionUpsertRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	sub, err := manager.ResolveDraft(r.Context(), r.PathValue("id"), draft, draft != nil)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	nodeIDs := sub.NodeIDs
	if isMergeSubscription(sub) {
		nodeIDs = nil
	}
	var records []state.Record
	if s.service != nil {
		records = s.service.Records()
	}
	body, err := subscription.Generate(subscription.Config{
		Shares: sub.Shares, Format: sub.Format, NodeIDs: nodeIDs, Mode: sub.Mode,
	}, records)
	if err != nil {
		if errors.Is(err, subscription.ErrNoTargets) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
			return
		}
		if errors.Is(err, subscription.ErrNoValidShares) {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "decode generated preview: " + err.Error()})
		return
	}
	response := subscriptionPreviewResponse{ResolvedCount: len(sub.Shares)}
	response.OutputCount = len(nonEmptySubscriptionLines(string(decoded)))
	warningProtocols := map[string]int{}
	for _, share := range sub.Shares {
		protocol := subscriptionProtocol(share)
		outcome := "passthrough"
		if !isMergeSubscription(sub) && supportedSubscriptionProtocol(protocol) {
			outcome = "rewritten"
			response.RewrittenCount++
		} else {
			response.PassthroughCount++
			if !isMergeSubscription(sub) {
				warningProtocols[protocol]++
			}
		}
		if len(response.Entries) < 50 {
			response.Entries = append(response.Entries, subscriptionPreviewEntry{Protocol: protocol, Outcome: outcome})
		}
	}
	warningProtocolNames := make([]string, 0, len(warningProtocols))
	for protocol := range warningProtocols {
		warningProtocolNames = append(warningProtocolNames, protocol)
	}
	sort.Strings(warningProtocolNames)
	for _, protocol := range warningProtocolNames {
		count := warningProtocols[protocol]
		response.Warnings = append(response.Warnings, fmt.Sprintf("协议 %s 有 %d 条未识别内容，已原样保留", protocol, count))
	}
	writeJSON(w, http.StatusOK, response)
}

func optionalSubscriptionUpsertRequest(r *http.Request) (*subscriptions.UpsertRequest, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	var req subscriptions.UpsertRequest
	err := json.NewDecoder(r.Body).Decode(&req)
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &req, nil
}

func (s *Server) adminRevealSubscriptionURL(w http.ResponseWriter, r *http.Request) {
	manager, ok := s.requireSubscriptionManager(w)
	if !ok {
		return
	}
	fullURL, err := manager.SecretURL(r.PathValue("id"), requestBaseURL(r))
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, map[string]string{"url": fullURL})
}

func nonEmptySubscriptionLines(value string) []string {
	lines := make([]string, 0)
	for _, line := range strings.Split(value, "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func generatedSubscriptionLineCount(sub config.SubscriptionConfig, records []state.Record) int {
	nodeIDs := sub.NodeIDs
	if isMergeSubscription(sub) {
		nodeIDs = nil
	}
	body, err := subscription.Generate(subscription.Config{
		Shares: sub.Shares, Format: sub.Format, NodeIDs: nodeIDs, Mode: sub.Mode,
	}, records)
	if err != nil {
		return 0
	}
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return 0
	}
	return len(nonEmptySubscriptionLines(string(decoded)))
}

func subscriptionProtocol(share string) string {
	protocol, _, ok := strings.Cut(strings.TrimSpace(share), "://")
	if !ok || strings.TrimSpace(protocol) == "" {
		return "unknown"
	}
	return strings.ToLower(strings.TrimSpace(protocol))
}

func supportedSubscriptionProtocol(protocol string) bool {
	switch protocol {
	case "vmess", "vless", "trojan", "ss", "hysteria", "hysteria2":
		return true
	default:
		return false
	}
}

func (s *Server) adminUpdateCustomCSV(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.CustomCSV.Enabled {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "custom CSV provider is not enabled"})
		return
	}
	path := strings.TrimSpace(s.cfg.CustomCSV.Path)
	if path == "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "custom CSV path is not configured"})
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxCustomCSVUploadBytes)
	defer r.Body.Close()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "custom CSV body must be 2 MiB or less"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "read custom CSV body: " + err.Error()})
		return
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "custom CSV body must not be empty"})
		return
	}

	candidates, err := provider.ParseCustomCSV(bytes.NewReader(data), s.cfg.CustomCSV.TopN)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(candidates) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "custom CSV contains no valid IP rows"})
		return
	}
	if err := writeFileAtomic(path, data); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save custom CSV: " + err.Error()})
		return
	}

	summary, err := s.service.Run(r.Context())
	response := customCSVUpdateResponse{
		Saved:      true,
		Path:       path,
		Candidates: len(candidates),
		Sync:       &summary,
	}
	if err != nil {
		response.Error = err.Error()
		if errors.Is(err, syncsvc.ErrUpdateInProgress) {
			response.Sync = nil
			writeJSON(w, http.StatusConflict, response)
			return
		}
		writeJSON(w, http.StatusBadGateway, response)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) adminSpeedTestPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"presets": speedTestPresets})
}

func (s *Server) adminRunTemporarySpeedTest(w http.ResponseWriter, r *http.Request) {
	var req temporarySpeedTestRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	result, err := s.service.RunTemporarySpeedTest(r.Context(), req.URL)
	if err != nil {
		writeTemporarySpeedTestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) adminApplyTemporarySpeedTest(w http.ResponseWriter, r *http.Request) {
	result, err := s.service.ApplyTemporarySpeedTest(r.Context(), r.PathValue("id"))
	if err != nil {
		writeTemporarySpeedTestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) requireSubscriptionManager(w http.ResponseWriter) (*subscriptions.Manager, bool) {
	if s.subscriptionManager == nil {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "subscription manager is not configured"})
		return nil, false
	}
	return s.subscriptionManager, true
}

func (s *Server) findSubscription(token string) (config.SubscriptionConfig, bool) {
	if s.subscriptionManager != nil {
		return s.subscriptionManager.ConfigForToken(token)
	}
	return findSubscription(s.staticSubscriptions, token)
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

func isMergeSubscription(sub config.SubscriptionConfig) bool {
	return strings.EqualFold(strings.TrimSpace(sub.Mode), "merge")
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return false
	}
	return true
}

func writeSubscriptionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, subscriptions.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, subscriptions.ErrNotEditable):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, subscriptions.ErrInvalidInput):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

func writeTemporarySpeedTestError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, syncsvc.ErrUpdateInProgress):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case errors.Is(err, syncsvc.ErrTemporarySpeedTestGone):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, syncsvc.ErrTemporarySpeedTestURL), errors.Is(err, syncsvc.ErrTemporarySpeedTestEmpty):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}

func requestBaseURL(r *http.Request) string {
	proto := r.Header.Get("X-Forwarded-Proto")
	if proto == "" {
		if r.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	return proto + "://" + host
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

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(path)
		return os.Rename(tmp, path)
	}
	return nil
}
