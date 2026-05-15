package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
