package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/config"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/dnspod"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/ping"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/provider"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/state"
	subscriptions "github.com/sleep/tencent-ddns-for-cf-ip/internal/subscriptions"
	syncsvc "github.com/sleep/tencent-ddns-for-cf-ip/internal/sync"
)

func TestAuth(t *testing.T) {
	service := syncsvc.NewService(syncsvc.Config{}, fakeProvider{}, fakePinger{}, nil, fakeDNS{}, fakeStore{}, state.Empty(), slog.Default())
	handler := NewServer(Config{Token: "secret"}, service, config.Config{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/records", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/records", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d", rr.Code)
	}
}

func TestConfigEndpointIncludesSpeedTestConfig(t *testing.T) {
	service := syncsvc.NewService(syncsvc.Config{}, fakeProvider{}, fakePinger{}, nil, fakeDNS{}, fakeStore{}, state.Empty(), slog.Default())
	handler := NewServer(Config{Token: "secret"}, service, config.Config{
		Sync: config.SyncConfig{
			SpeedTest: config.SpeedTestConfig{
				Enabled:           true,
				URL:               "https://download.example.com/probe.bin",
				DownloadBytes:     2048,
				Concurrency:       3,
				CandidatesPerNode: 4,
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/config", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d", rr.Code)
	}
	var got map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	syncConfig, ok := got["sync"].(map[string]any)
	if !ok {
		t.Fatalf("sync config missing from response: %#v", got)
	}
	speedTest, ok := syncConfig["speed_test"].(map[string]any)
	if !ok {
		t.Fatalf("speed test config missing from response: %#v", syncConfig)
	}
	if speedTest["enabled"] != true || speedTest["url"] != "https://download.example.com/probe.bin" || speedTest["download_bytes"] != float64(2048) {
		t.Fatalf("speed test config missing from response: %#v", speedTest)
	}
}

func TestAdminSpeedTestPresets(t *testing.T) {
	service := syncsvc.NewService(syncsvc.Config{}, fakeProvider{}, fakePinger{}, nil, fakeDNS{}, fakeStore{}, state.Empty(), slog.Default())
	handler := NewServer(Config{Token: "secret"}, service, config.Config{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/speed-test-presets", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got struct {
		Presets []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"presets"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Presets) != 4 || got.Presets[1].Name != "Cloudflare 10MB" || got.Presets[1].URL != "https://speed.cloudflare.com/__down?bytes=10485760" {
		t.Fatalf("unexpected presets: %#v", got.Presets)
	}
}

func TestPublicSubscriptionEndpoint(t *testing.T) {
	initial := state.State{
		Records: []state.Record{
			{Name: "cf-ctcc-01.cdn", FQDN: "cf-ctcc-01.cdn.example.com", NodeID: "ctcc", LatencyMS: 20},
			{Name: "cf-bgp-01.cdn", FQDN: "cf-bgp-01.cdn.example.com", NodeID: "bgp", LatencyMS: 10},
		},
	}
	service := syncsvc.NewService(syncsvc.Config{}, fakeProvider{}, fakePinger{}, nil, fakeDNS{}, fakeStore{}, initial, slog.Default())
	handler := NewServer(Config{
		Token: "secret",
		Subscriptions: []config.SubscriptionConfig{
			{
				Name:        "ctcc-main",
				Enabled:     true,
				PublicToken: "long-random-public-token",
				Key:         "subscription-key",
				Format:      "base64",
				NodeIDs:     []string{"ctcc"},
				Shares:      []string{"vless://uuid@old.example.com:443?security=tls&sni=sni.example.com#name"},
			},
			{
				Name:        "bgp-main",
				Enabled:     true,
				PublicToken: "another-random-public-token",
				Key:         "another-subscription-key",
				Format:      "base64",
				NodeIDs:     []string{"bgp"},
				Shares:      []string{"trojan://pass@old.example.com:443?security=tls&sni=sni.example.com#name"},
			},
		},
	}, service, config.Config{})

	req := httptest.NewRequest(http.MethodGet, "/sub/long-random-public-token?key=subscription-key", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("content-type = %q", got)
	}
	decoded, err := base64.StdEncoding.DecodeString(rr.Body.String())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(decoded), "@cf-ctcc-01.cdn.example.com:443") {
		t.Fatalf("subscription did not use preferred fqdn: %s", decoded)
	}
	if strings.Contains(string(decoded), "@cf-bgp-01.cdn.example.com:443") {
		t.Fatalf("subscription leaked another nodeid: %s", decoded)
	}
	if !strings.Contains(string(decoded), "sni=sni.example.com") {
		t.Fatalf("subscription changed sni: %s", decoded)
	}

	req = httptest.NewRequest(http.MethodGet, "/sub/another-random-public-token?key=another-subscription-key", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("second code = %d, body = %s", rr.Code, rr.Body.String())
	}
	decoded, err = base64.StdEncoding.DecodeString(rr.Body.String())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(decoded), "@cf-bgp-01.cdn.example.com:443") {
		t.Fatalf("second subscription did not use its nodeid: %s", decoded)
	}

	req = httptest.NewRequest(http.MethodGet, "/sub/long-random-public-token", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("missing key code = %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/sub/long-random-public-token?key=wrong", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key code = %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/sub/wrong-token", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("wrong-token code = %d", rr.Code)
	}
}

func TestPublicSubscriptionEndpointReportsNoTargets(t *testing.T) {
	service := syncsvc.NewService(syncsvc.Config{}, fakeProvider{}, fakePinger{}, nil, fakeDNS{}, fakeStore{}, state.Empty(), slog.Default())
	handler := NewServer(Config{
		Token: "secret",
		Subscriptions: []config.SubscriptionConfig{
			{
				Enabled:     true,
				PublicToken: "long-random-public-token",
				Key:         "subscription-key",
				Format:      "base64",
				Shares:      []string{"vless://uuid@old.example.com:443#name"},
			},
		},
	}, service, config.Config{})

	req := httptest.NewRequest(http.MethodGet, "/sub/long-random-public-token?key=subscription-key", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(rr.Body)
		t.Fatalf("code = %d, body = %s", rr.Code, body)
	}
}

func TestPublicSubscriptionEndpointQueryNodeIDsNarrowConfiguredNodeIDs(t *testing.T) {
	initial := state.State{
		Records: []state.Record{
			{Name: "cf-ctcc-01.cdn", FQDN: "cf-ctcc-01.cdn.example.com", NodeID: "ctcc", LatencyMS: 20},
			{Name: "cf-bgp-01.cdn", FQDN: "cf-bgp-01.cdn.example.com", NodeID: "bgp", LatencyMS: 10},
			{Name: "cf-cucc-01.cdn", FQDN: "cf-cucc-01.cdn.example.com", NodeID: "cucc", LatencyMS: 5},
		},
	}
	service := syncsvc.NewService(syncsvc.Config{}, fakeProvider{}, fakePinger{}, nil, fakeDNS{}, fakeStore{}, initial, slog.Default())
	handler := NewServer(Config{
		Token: "secret",
		Subscriptions: []config.SubscriptionConfig{
			{
				Enabled:     true,
				PublicToken: "long-random-public-token",
				Key:         "subscription-key",
				Format:      "base64",
				NodeIDs:     []string{"ctcc", "bgp"},
				Shares:      []string{"vless://uuid@old.example.com:443#name"},
			},
		},
	}, service, config.Config{})

	req := httptest.NewRequest(http.MethodGet, "/sub/long-random-public-token?key=subscription-key&nodeids=CTCC,cucc&nodeids=bgp", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rr.Code, rr.Body.String())
	}
	decoded, err := base64.StdEncoding.DecodeString(rr.Body.String())
	if err != nil {
		t.Fatal(err)
	}
	body := string(decoded)
	if !strings.Contains(body, "@cf-ctcc-01.cdn.example.com:443") || !strings.Contains(body, "@cf-bgp-01.cdn.example.com:443") {
		t.Fatalf("requested allowed targets missing: %s", body)
	}
	if strings.Contains(body, "@cf-cucc-01.cdn.example.com:443") {
		t.Fatalf("request expanded beyond configured nodeids: %s", body)
	}

	req = httptest.NewRequest(http.MethodGet, "/sub/long-random-public-token?key=subscription-key&nodeids=cucc", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("disallowed nodeids code = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestPublicSubscriptionEndpointQueryNodeIDsCanFilterUnrestrictedSubscription(t *testing.T) {
	initial := state.State{
		Records: []state.Record{
			{Name: "cf-ctcc-01.cdn", FQDN: "cf-ctcc-01.cdn.example.com", NodeID: "ctcc", LatencyMS: 20},
			{Name: "cf-bgp-01.cdn", FQDN: "cf-bgp-01.cdn.example.com", NodeID: "bgp", LatencyMS: 10},
		},
	}
	service := syncsvc.NewService(syncsvc.Config{}, fakeProvider{}, fakePinger{}, nil, fakeDNS{}, fakeStore{}, initial, slog.Default())
	handler := NewServer(Config{
		Token: "secret",
		Subscriptions: []config.SubscriptionConfig{
			{
				Enabled:     true,
				PublicToken: "long-random-public-token",
				Key:         "subscription-key",
				Format:      "base64",
				Shares:      []string{"vless://uuid@old.example.com:443#name"},
			},
		},
	}, service, config.Config{})

	req := httptest.NewRequest(http.MethodGet, "/sub/long-random-public-token?key=subscription-key&nodeids=bgp", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rr.Code, rr.Body.String())
	}
	decoded, err := base64.StdEncoding.DecodeString(rr.Body.String())
	if err != nil {
		t.Fatal(err)
	}
	body := string(decoded)
	if !strings.Contains(body, "@cf-bgp-01.cdn.example.com:443") {
		t.Fatalf("requested target missing: %s", body)
	}
	if strings.Contains(body, "@cf-ctcc-01.cdn.example.com:443") {
		t.Fatalf("unrequested target leaked: %s", body)
	}
}

func TestAdminSubscriptionsCRUDAndPublicEndpointUseWritableSubscriptions(t *testing.T) {
	manager, err := subscriptions.NewManager(nil, subscriptions.NewStore(filepath.Join(t.TempDir(), "subscriptions.json")))
	if err != nil {
		t.Fatal(err)
	}
	initial := state.State{
		Records: []state.Record{{Name: "cf-ctcc-01.cdn", FQDN: "cf-ctcc-01.cdn.example.com", NodeID: "ctcc", LatencyMS: 20}},
	}
	service := syncsvc.NewService(syncsvc.Config{}, fakeProvider{}, fakePinger{}, nil, fakeDNS{}, fakeStore{}, initial, slog.Default())
	handler := NewServer(Config{Token: "secret", SubscriptionManager: manager}, service, config.Config{})

	body := strings.NewReader(`{"name":"main","enabled":true,"format":"base64","nodeids":["ctcc"],"shares":["vless://uuid@old.example.com:443#name"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/subscriptions", body)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "admin.example.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create code = %d, body = %s", rr.Code, rr.Body.String())
	}
	var created struct {
		Item subscriptions.ListItem `json:"item"`
		Key  string                 `json:"key"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Key == "" || created.Item.PublicToken == "" || created.Item.URLTemplate != "https://admin.example.com/sub/"+created.Item.PublicToken+"?key=<key>" {
		t.Fatalf("unexpected create response: %#v", created)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/subscriptions", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list code = %d, body = %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), created.Key) {
		t.Fatalf("list leaked subscription key: %s", rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/sub/"+created.Item.PublicToken+"?key="+created.Key, nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("subscription code = %d, body = %s", rr.Code, rr.Body.String())
	}
	decoded, err := base64.StdEncoding.DecodeString(rr.Body.String())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(decoded), "@cf-ctcc-01.cdn.example.com:443") {
		t.Fatalf("writable subscription did not use preferred fqdn: %s", decoded)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/v1/admin/subscriptions/"+created.Item.ID, nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete code = %d, body = %s", rr.Code, rr.Body.String())
	}
}

func TestAdminRotateSecretReturnsNewKeyOnce(t *testing.T) {
	manager, err := subscriptions.NewManager(nil, subscriptions.NewStore(filepath.Join(t.TempDir(), "subscriptions.json")))
	if err != nil {
		t.Fatal(err)
	}
	created, err := manager.Create(subscriptions.UpsertRequest{
		Name:    "main",
		Enabled: true,
		Format:  "base64",
		Shares:  []string{"vless://uuid@old.example.com:443#name"},
	}, "http://example.com")
	if err != nil {
		t.Fatal(err)
	}
	service := syncsvc.NewService(syncsvc.Config{}, fakeProvider{}, fakePinger{}, nil, fakeDNS{}, fakeStore{}, state.Empty(), slog.Default())
	handler := NewServer(Config{Token: "secret", SubscriptionManager: manager}, service, config.Config{})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/subscriptions/"+created.Item.ID+"/rotate-secret", strings.NewReader(`{"target":"key"}`))
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("rotate code = %d, body = %s", rr.Code, rr.Body.String())
	}
	var rotated struct {
		Item subscriptions.ListItem `json:"item"`
		Key  string                 `json:"key"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.Key == "" || rotated.Key == created.Key {
		t.Fatalf("unexpected rotate response: %#v", rotated)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/subscriptions", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list code = %d, body = %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), rotated.Key) {
		t.Fatalf("list leaked rotated key: %s", rr.Body.String())
	}
}

type fakeProvider struct{}

func (fakeProvider) Fetch(context.Context, []string) (map[string][]provider.Candidate, error) {
	return map[string][]provider.Candidate{}, nil
}

type fakePinger struct{}

func (fakePinger) Check(context.Context, []provider.Candidate) []ping.Result {
	return nil
}

type fakeDNS struct{}

func (fakeDNS) ListRecords(context.Context) ([]dnspod.Record, error) { return nil, nil }
func (fakeDNS) CreateRecord(context.Context, dnspod.Record) (uint64, error) {
	return 0, nil
}
func (fakeDNS) ModifyRecord(context.Context, dnspod.Record) error { return nil }
func (fakeDNS) DeleteRecord(context.Context, uint64) error        { return nil }

type fakeStore struct{}

func (fakeStore) Load() (state.State, error) { return state.Empty(), nil }
func (fakeStore) Save(state.State) error     { return nil }
