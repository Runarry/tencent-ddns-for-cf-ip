package api

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/config"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/dnspod"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/ping"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/provider"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/state"
	syncsvc "github.com/sleep/tencent-ddns-for-cf-ip/internal/sync"
)

func TestAuth(t *testing.T) {
	service := syncsvc.NewService(syncsvc.Config{}, fakeProvider{}, fakePinger{}, fakeDNS{}, fakeStore{}, state.Empty(), slog.Default())
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

func TestPublicSubscriptionEndpoint(t *testing.T) {
	initial := state.State{
		Records: []state.Record{
			{Name: "cf-ctcc-01.cdn", FQDN: "cf-ctcc-01.cdn.example.com", NodeID: "ctcc", LatencyMS: 20},
		},
	}
	service := syncsvc.NewService(syncsvc.Config{}, fakeProvider{}, fakePinger{}, fakeDNS{}, fakeStore{}, initial, slog.Default())
	handler := NewServer(Config{
		Token: "secret",
		Subscription: config.SubscriptionConfig{
			Enabled:     true,
			PublicToken: "long-random-public-token",
			Format:      "base64",
			Shares:      []string{"vless://uuid@old.example.com:443?security=tls&sni=sni.example.com#name"},
		},
	}, service, config.Config{})

	req := httptest.NewRequest(http.MethodGet, "/sub/long-random-public-token", nil)
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
	if !strings.Contains(string(decoded), "sni=sni.example.com") {
		t.Fatalf("subscription changed sni: %s", decoded)
	}

	req = httptest.NewRequest(http.MethodGet, "/sub/wrong-token", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("wrong-token code = %d", rr.Code)
	}
}

func TestPublicSubscriptionEndpointReportsNoTargets(t *testing.T) {
	service := syncsvc.NewService(syncsvc.Config{}, fakeProvider{}, fakePinger{}, fakeDNS{}, fakeStore{}, state.Empty(), slog.Default())
	handler := NewServer(Config{
		Token: "secret",
		Subscription: config.SubscriptionConfig{
			Enabled:     true,
			PublicToken: "long-random-public-token",
			Format:      "base64",
			Shares:      []string{"vless://uuid@old.example.com:443#name"},
		},
	}, service, config.Config{})

	req := httptest.NewRequest(http.MethodGet, "/sub/long-random-public-token", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(rr.Body)
		t.Fatalf("code = %d, body = %s", rr.Code, body)
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
