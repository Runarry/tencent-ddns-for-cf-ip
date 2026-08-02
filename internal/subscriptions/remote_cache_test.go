package subscriptions

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/config"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/remotesource"
)

type stubRemoteFetcher struct {
	result remotesource.Result
	err    error
}

func (f *stubRemoteFetcher) Fetch(context.Context, string) (remotesource.Result, error) {
	return f.result, f.err
}

func TestRemoteCacheKeepsLastSuccessAfterRefreshFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	now := time.Now().UTC().Truncate(time.Second)
	fetcher := &stubRemoteFetcher{result: remotesource.Result{
		Lines: []string{"vless://one", "newproto://two"}, Encoding: remotesource.EncodingBase64, FetchedAt: now,
	}}
	cache, err := NewRemoteCache(path, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	source := config.SubscriptionSourceConfig{ID: "remote", Type: "remote", Enabled: true, URL: "https://example.com/sub"}
	resolution := cache.Refresh(context.Background(), "sub/remote", source)
	if resolution.Status.State != "healthy" || len(resolution.Lines) != 2 {
		t.Fatalf("successful resolution = %#v", resolution)
	}

	fetcher.err = errors.New("upstream unavailable")
	resolution = cache.Refresh(context.Background(), "sub/remote", source)
	if resolution.Status.State != "warning" || !resolution.Status.HasCache || len(resolution.Lines) != 2 {
		t.Fatalf("failed refresh did not retain cache: %#v", resolution)
	}

	reloaded, err := NewRemoteCache(path, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	resolution = reloaded.ResolveCached("sub/remote", source)
	if resolution.Status.State != "warning" || len(resolution.Lines) != 2 {
		t.Fatalf("reloaded cache = %#v", resolution)
	}
}

func TestRemoteCacheDoesNotReuseContentWhenURLChanges(t *testing.T) {
	fetcher := &stubRemoteFetcher{result: remotesource.Result{
		Lines: []string{"vless://one"}, Encoding: remotesource.EncodingPlain, FetchedAt: time.Now(),
	}}
	cache, err := NewRemoteCache("", fetcher)
	if err != nil {
		t.Fatal(err)
	}
	source := config.SubscriptionSourceConfig{ID: "remote", Type: "remote", Enabled: true, URL: "https://example.com/one"}
	cache.Refresh(context.Background(), "sub/remote", source)
	source.URL = "https://example.com/two"
	resolution := cache.ResolveCached("sub/remote", source)
	if resolution.Status.State != "never" || len(resolution.Lines) != 0 {
		t.Fatalf("changed URL reused old cache: %#v", resolution)
	}
}
