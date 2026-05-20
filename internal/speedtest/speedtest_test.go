package speedtest

import (
	"bytes"
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/provider"
)

func TestTesterUsesForcedIPHostSNIRangeAndReadLimit(t *testing.T) {
	var mu sync.Mutex
	var gotSNI string
	var gotHost string
	var gotRange string

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotHost = r.Host
		gotRange = r.Header.Get("Range")
		mu.Unlock()
		_, _ = w.Write(bytes.Repeat([]byte("a"), 64))
	}))
	server.TLS = &tls.Config{
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			mu.Lock()
			gotSNI = hello.ServerName
			mu.Unlock()
			return nil, nil
		},
	}
	server.StartTLS()
	defer server.Close()

	host, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	targetHost := net.JoinHostPort("speed.test", port)
	tester := NewTester(Config{
		URL:           "https://" + targetHost + "/file.bin",
		DownloadBytes: 16,
		Timeout:       time.Second,
		Concurrency:   1,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	})

	results := tester.Check(context.Background(), []provider.Candidate{{NodeID: "ctcc", IP: host}})
	if len(results) != 1 {
		t.Fatalf("results = %#v", results)
	}
	result := results[0]
	if !result.Success {
		t.Fatalf("result failed: %#v", result)
	}
	if result.DownloadBytes != 16 {
		t.Fatalf("download bytes = %d", result.DownloadBytes)
	}
	if result.SpeedBPS <= 0 || result.DownloadDuration <= 0 || result.TTFB <= 0 {
		t.Fatalf("missing metrics: %#v", result)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotHost != targetHost {
		t.Fatalf("host = %q, want %q", gotHost, targetHost)
	}
	if gotSNI != "speed.test" {
		t.Fatalf("sni = %q", gotSNI)
	}
	if gotRange != "bytes=0-15" {
		t.Fatalf("range = %q", gotRange)
	}
}

func TestTesterReportsTimeout(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte("slow"))
	}))
	defer server.Close()

	host, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	tester := NewTester(Config{
		URL:           "https://" + net.JoinHostPort("speed.test", port) + "/slow",
		DownloadBytes: 4,
		Timeout:       20 * time.Millisecond,
		Concurrency:   1,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	})

	results := tester.Check(context.Background(), []provider.Candidate{{NodeID: "ctcc", IP: host}})
	if len(results) != 1 {
		t.Fatalf("results = %#v", results)
	}
	if results[0].Success || results[0].Error == "" {
		t.Fatalf("expected timeout error, got %#v", results[0])
	}
}

func TestTesterReportsHTTPError(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer server.Close()

	host, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	tester := NewTester(Config{
		URL:           "https://" + net.JoinHostPort("speed.test", port) + "/missing",
		DownloadBytes: 4,
		Timeout:       time.Second,
		Concurrency:   1,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
	})

	results := tester.Check(context.Background(), []provider.Candidate{{NodeID: "ctcc", IP: host}})
	if len(results) != 1 {
		t.Fatalf("results = %#v", results)
	}
	if results[0].Success || results[0].Error != "HTTP 404" {
		t.Fatalf("expected HTTP 404, got %#v", results[0])
	}
}
