package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestAdminCustomCSVRequiresAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.csv")
	service := syncsvc.NewService(syncsvc.Config{}, fakeProvider{}, fakePinger{}, nil, fakeDNS{}, fakeStore{}, state.Empty(), slog.Default())
	handler := NewServer(Config{
		Token: "secret",
		CustomCSV: CustomCSVConfig{
			Enabled: true,
			Path:    path,
			TopN:    5,
		},
	}, service, config.Config{})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/custom-ips/csv", strings.NewReader(validCustomCSV()))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, body = %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("custom CSV was written without auth, stat err = %v", err)
	}
}

func TestAdminCustomCSVRejectsDisabledProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.csv")
	providerClient := &countingProvider{}
	service := syncsvc.NewService(syncsvc.Config{}, providerClient, fakePinger{}, nil, fakeDNS{}, fakeStore{}, state.Empty(), slog.Default())
	handler := NewServer(Config{Token: "secret"}, service, config.Config{})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/custom-ips/csv", strings.NewReader(validCustomCSV()))
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("code = %d, body = %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("custom CSV was written while disabled, stat err = %v", err)
	}
	if providerClient.calls != 0 {
		t.Fatalf("sync was triggered while disabled, calls = %d", providerClient.calls)
	}
}

func TestAdminCustomCSVRejectsInvalidCSVWithoutSideEffects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.csv")
	providerClient := &countingProvider{}
	service := syncsvc.NewService(syncsvc.Config{}, providerClient, fakePinger{}, nil, fakeDNS{}, fakeStore{}, state.Empty(), slog.Default())
	handler := NewServer(Config{
		Token: "secret",
		CustomCSV: CustomCSVConfig{
			Enabled: true,
			Path:    path,
			TopN:    5,
		},
	}, service, config.Config{})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/custom-ips/csv", strings.NewReader("IP,速度\n1.1.1.1,10\n"))
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, body = %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid custom CSV was written, stat err = %v", err)
	}
	if providerClient.calls != 0 {
		t.Fatalf("sync was triggered for invalid CSV, calls = %d", providerClient.calls)
	}
}

func TestAdminCustomCSVRejectsCSVWithoutValidRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.csv")
	service := syncsvc.NewService(syncsvc.Config{}, fakeProvider{}, fakePinger{}, nil, fakeDNS{}, fakeStore{}, state.Empty(), slog.Default())
	handler := NewServer(Config{
		Token: "secret",
		CustomCSV: CustomCSVConfig{
			Enabled: true,
			Path:    path,
			TopN:    5,
		},
	}, service, config.Config{})

	body := "IP 地址,平均延迟,下载速度(MB/s)\nnot-an-ip,20,10\n"
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/custom-ips/csv", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, body = %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("CSV without valid rows was written, stat err = %v", err)
	}
}

func TestAdminCustomCSVWritesFileAndRunsSync(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.csv")
	providerClient := &countingProvider{}
	service := syncsvc.NewService(syncsvc.Config{
		NodeIDs:              []string{"ctcc"},
		ManagedPrefix:        "cf",
		ManagedBaseSubdomain: "cdn",
		DefaultNodeID:        "ctcc",
		MaxRecordsPerNode:    5,
		Domain:               "example.com",
		Custom: syncsvc.CustomConfig{
			Enabled: true,
			Source: provider.NewCustomCSVClient(provider.CustomCSVConfig{
				Path: path,
				TopN: 5,
			}),
		},
	}, providerClient, alivePinger{}, nil, fakeDNS{}, fakeStore{}, state.Empty(), slog.Default())
	handler := NewServer(Config{
		Token: "secret",
		CustomCSV: CustomCSVConfig{
			Enabled: true,
			Path:    path,
			TopN:    5,
		},
	}, service, config.Config{})

	csvBody := validCustomCSV()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/custom-ips/csv", strings.NewReader(csvBody))
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got customCSVUpdateResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Saved || got.Path != path || got.Candidates != 2 || got.Sync == nil || !got.Sync.Success {
		t.Fatalf("unexpected response: %#v", got)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != csvBody {
		t.Fatalf("saved CSV = %q, want %q", saved, csvBody)
	}
	if providerClient.calls != 1 {
		t.Fatalf("sync provider calls = %d, want 1", providerClient.calls)
	}
	records := service.Records()
	if len(records) != 2 || records[0].NodeID != provider.CustomNodeID || records[0].Value != "1.1.1.1" {
		t.Fatalf("sync did not publish custom records: %#v", records)
	}
}

func TestAdminCustomCSVSyncConflictStillLeavesSavedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.csv")
	providerClient := &blockingProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	service := syncsvc.NewService(syncsvc.Config{NodeIDs: []string{"ctcc"}}, providerClient, fakePinger{}, nil, fakeDNS{}, fakeStore{}, state.Empty(), slog.Default())
	handler := NewServer(Config{
		Token: "secret",
		CustomCSV: CustomCSVConfig{
			Enabled: true,
			Path:    path,
			TopN:    5,
		},
	}, service, config.Config{})
	runDone := make(chan error, 1)
	go func() {
		_, err := service.Run(context.Background())
		runDone <- err
	}()
	<-providerClient.started
	defer func() {
		close(providerClient.release)
		if err := <-runDone; err != nil {
			t.Errorf("background sync error = %v", err)
		}
	}()

	csvBody := validCustomCSV()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/custom-ips/csv", strings.NewReader(csvBody))
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("code = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got customCSVUpdateResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Saved || got.Error != syncsvc.ErrUpdateInProgress.Error() {
		t.Fatalf("unexpected response: %#v", got)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != csvBody {
		t.Fatalf("saved CSV = %q, want %q", saved, csvBody)
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
	if got := rr.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("cache-control = %q", got)
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

func TestPublicSubscriptionEndpointMergeModeDoesNotRequireTargets(t *testing.T) {
	service := syncsvc.NewService(syncsvc.Config{}, fakeProvider{}, fakePinger{}, nil, fakeDNS{}, fakeStore{}, state.Empty(), slog.Default())
	handler := NewServer(Config{
		Token: "secret",
		Subscriptions: []config.SubscriptionConfig{
			{
				Enabled:     true,
				PublicToken: "long-random-public-token",
				Key:         "subscription-key",
				Format:      "base64",
				Mode:        "merge",
				NodeIDs:     []string{"ctcc"},
				Shares: []string{
					" vless://uuid@old.example.com:443?security=tls&sni=sni.example.com#name ",
					"vless://uuid@old.example.com:443?security=tls&sni=sni.example.com#name",
					"trojan://pass@origin.example.com:8443?security=tls#trojan",
				},
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
	want := strings.Join([]string{
		"vless://uuid@old.example.com:443?security=tls&sni=sni.example.com#name",
		"vless://uuid@old.example.com:443?security=tls&sni=sni.example.com#name",
		"trojan://pass@origin.example.com:8443?security=tls#trojan",
	}, "\n") + "\n"
	if string(decoded) != want {
		t.Fatalf("merged subscription = %q, want %q", decoded, want)
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

func TestAdminSubscriptionsMergeModePublicEndpointUseWritableSubscriptions(t *testing.T) {
	manager, err := subscriptions.NewManager(nil, subscriptions.NewStore(filepath.Join(t.TempDir(), "subscriptions.json")))
	if err != nil {
		t.Fatal(err)
	}
	service := syncsvc.NewService(syncsvc.Config{}, fakeProvider{}, fakePinger{}, nil, fakeDNS{}, fakeStore{}, state.Empty(), slog.Default())
	handler := NewServer(Config{Token: "secret", SubscriptionManager: manager}, service, config.Config{})

	body := strings.NewReader(`{"name":"merge","enabled":true,"format":"base64","mode":"merge","nodeids":["ctcc"],"shares":["vless://uuid@old.example.com:443#name","trojan://pass@origin.example.com:443#trojan"]}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/subscriptions", body)
	req.Header.Set("Authorization", "Bearer secret")
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
	if created.Item.Mode != "merge" {
		t.Fatalf("created mode = %q", created.Item.Mode)
	}

	req = httptest.NewRequest(http.MethodGet, "/sub/"+created.Item.PublicToken+"?key="+created.Key+"&nodeids=bgp", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("subscription code = %d, body = %s", rr.Code, rr.Body.String())
	}
	decoded, err := base64.StdEncoding.DecodeString(rr.Body.String())
	if err != nil {
		t.Fatal(err)
	}
	want := "vless://uuid@old.example.com:443#name\ntrojan://pass@origin.example.com:443#trojan\n"
	if string(decoded) != want {
		t.Fatalf("merged subscription = %q, want %q", decoded, want)
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

type apiSubscriptionSourceResolver struct {
	resolution subscriptions.SourceResolution
	refreshes  []string
}

func (r *apiSubscriptionSourceResolver) ResolveCached(string, config.SubscriptionSourceConfig) subscriptions.SourceResolution {
	return r.resolution
}

func (r *apiSubscriptionSourceResolver) Refresh(_ context.Context, cacheKey string, _ config.SubscriptionSourceConfig) subscriptions.SourceResolution {
	r.refreshes = append(r.refreshes, cacheKey)
	return r.resolution
}

func TestAdminSubscriptionDetailRefreshPreviewRevealAndRestore(t *testing.T) {
	store := subscriptions.NewStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	static := []config.SubscriptionConfig{{
		ID:          "main",
		Name:        "from-config",
		Enabled:     true,
		PublicToken: "config-random-public-token",
		Key:         "config-key",
		Format:      "base64",
		Mode:        "rewrite",
		NodeIDs:     []string{"ctcc"},
		Sources: []config.SubscriptionSourceConfig{
			{ID: "direct", Name: "direct", Type: "share", Enabled: true, Share: "vless://uuid@old.example.com:443#direct"},
			{ID: "upstream", Name: "remote", Type: "remote", Enabled: true, URL: "https://upstream.example.com/sub"},
		},
	}}
	manager, err := subscriptions.NewManager(static, store)
	if err != nil {
		t.Fatal(err)
	}
	fetchedAt := time.Now().UTC().Truncate(time.Second)
	resolver := &apiSubscriptionSourceResolver{resolution: subscriptions.SourceResolution{
		Lines: []string{"trojan://pass@old.example.com:443#remote"},
		Status: subscriptions.SourceStatus{
			State: "healthy", LastSuccessAt: &fetchedAt, ResolvedCount: 1, Encoding: "plain", HasCache: true,
		},
	}}
	manager.SetSourceResolver(resolver)
	initial := state.State{Records: []state.Record{{
		Name: "cf-ctcc-01.cdn", FQDN: "cf-ctcc-01.cdn.example.com", NodeID: "ctcc", LatencyMS: 20,
	}}}
	service := syncsvc.NewService(syncsvc.Config{}, fakeProvider{}, fakePinger{}, nil, fakeDNS{}, fakeStore{}, initial, slog.Default())
	handler := NewServer(Config{Token: "secret", SubscriptionManager: manager}, service, config.Config{})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/subscriptions/config:main", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("detail code = %d, body = %s", rr.Code, rr.Body.String())
	}
	var detail struct {
		Item subscriptions.ListItem `json:"item"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&detail); err != nil {
		t.Fatal(err)
	}
	if detail.Item.ID != "config:main" || detail.Item.Overridden || detail.Item.SourceCount != 2 || len(detail.Item.Sources) != 2 {
		t.Fatalf("detail item = %#v", detail.Item)
	}
	if detail.Item.Sources[1].URL != "https://upstream.example.com/sub" || detail.Item.Sources[1].Status != "healthy" || detail.Item.Sources[1].ResolvedCount != 1 {
		t.Fatalf("remote source detail = %#v", detail.Item.Sources[1])
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/subscriptions/config:main/refresh-sources", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("refresh code = %d, body = %s", rr.Code, rr.Body.String())
	}
	if len(resolver.refreshes) != 1 || resolver.refreshes[0] != "config:main/upstream" {
		t.Fatalf("refresh calls = %#v", resolver.refreshes)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/subscriptions/config:main/preview", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview code = %d, body = %s", rr.Code, rr.Body.String())
	}
	var preview subscriptionPreviewResponse
	if err := json.NewDecoder(rr.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	if preview.ResolvedCount != 2 || preview.OutputCount != 2 || preview.RewrittenCount != 2 || preview.PassthroughCount != 0 || len(preview.Entries) != 2 {
		t.Fatalf("preview = %#v", preview)
	}
	if preview.Entries[0].Protocol != "vless" || preview.Entries[0].Outcome != "rewritten" || preview.Entries[1].Protocol != "trojan" {
		t.Fatalf("preview entries = %#v", preview.Entries)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/subscriptions/config:main/reveal-url", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("reveal code = %d, body = %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Cache-Control") != "no-store" || rr.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("reveal cache headers = %#v", rr.Header())
	}
	var revealed map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&revealed); err != nil {
		t.Fatal(err)
	}
	if revealed["url"] != "http://example.com/sub/config-random-public-token?key=config-key" {
		t.Fatalf("revealed URL = %q", revealed["url"])
	}

	if _, err := manager.Update("config:main", subscriptions.UpsertRequest{
		Name:        "runtime-override",
		Enabled:     true,
		PublicToken: "config-random-public-token",
		Key:         "config-key",
		Format:      "base64",
		Mode:        "merge",
		Sources: []config.SubscriptionSourceConfig{{
			ID: "direct", Type: "share", Enabled: true, Share: "vless://uuid@origin.example.com:443#override",
		}},
	}, "http://example.com"); err != nil {
		t.Fatal(err)
	}
	overridden, err := manager.Get("config:main", "http://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !overridden.Overridden || overridden.Name != "runtime-override" {
		t.Fatalf("override setup = %#v", overridden)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/subscriptions/config:main/restore", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("restore code = %d, body = %s", rr.Code, rr.Body.String())
	}
	var restored struct {
		Item subscriptions.ListItem `json:"item"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&restored); err != nil {
		t.Fatal(err)
	}
	if restored.Item.Overridden || restored.Item.Name != "from-config" || restored.Item.Mode != "rewrite" || restored.Item.SourceCount != 2 {
		t.Fatalf("restored API item = %#v", restored.Item)
	}
}

type fakeProvider struct{}

func (fakeProvider) Fetch(context.Context, []string) (map[string][]provider.Candidate, error) {
	return map[string][]provider.Candidate{}, nil
}

type countingProvider struct {
	calls int
}

func (p *countingProvider) Fetch(context.Context, []string) (map[string][]provider.Candidate, error) {
	p.calls++
	return map[string][]provider.Candidate{}, nil
}

type blockingProvider struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockingProvider) Fetch(ctx context.Context, _ []string) (map[string][]provider.Candidate, error) {
	close(p.started)
	select {
	case <-p.release:
		return map[string][]provider.Candidate{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type fakePinger struct{}

func (fakePinger) Check(context.Context, []provider.Candidate) []ping.Result {
	return nil
}

type alivePinger struct{}

func (alivePinger) Check(_ context.Context, candidates []provider.Candidate) []ping.Result {
	results := make([]ping.Result, 0, len(candidates))
	for _, candidate := range candidates {
		results = append(results, ping.Result{
			Candidate: candidate,
			Latency:   20 * time.Millisecond,
			Alive:     true,
		})
	}
	return results
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

func validCustomCSV() string {
	return "IP 地址,平均延迟,下载速度(MB/s)\n" +
		"1.1.1.1,20,10\n" +
		"1.1.1.2,30,9\n"
}
