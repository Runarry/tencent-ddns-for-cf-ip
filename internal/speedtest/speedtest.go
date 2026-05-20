package speedtest

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/provider"
)

type Config struct {
	URL             string
	DownloadBytes   int64
	Timeout         time.Duration
	Concurrency     int
	TLSClientConfig *tls.Config
}

type Result struct {
	Candidate        provider.Candidate
	SpeedBPS         int64
	DownloadBytes    int64
	DownloadDuration time.Duration
	TTFB             time.Duration
	Success          bool
	Error            string
}

type Tester struct {
	cfg Config
}

func NewTester(cfg Config) *Tester {
	if cfg.DownloadBytes <= 0 {
		cfg.DownloadBytes = 1024 * 1024
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 8 * time.Second
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 8
	}
	return &Tester{cfg: cfg}
}

func (t *Tester) Check(ctx context.Context, candidates []provider.Candidate) []Result {
	unique := dedupe(candidates)
	jobs := make(chan provider.Candidate)
	results := make(chan Result, len(unique))
	var wg sync.WaitGroup

	for i := 0; i < t.cfg.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for candidate := range jobs {
				results <- t.checkOne(ctx, candidate)
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, candidate := range unique {
			select {
			case <-ctx.Done():
				return
			case jobs <- candidate:
			}
		}
	}()

	wg.Wait()
	close(results)

	collected := make([]Result, 0, len(unique))
	for result := range results {
		collected = append(collected, result)
	}
	sort.SliceStable(collected, func(i, j int) bool {
		if collected[i].Candidate.NodeID != collected[j].Candidate.NodeID {
			return collected[i].Candidate.NodeID < collected[j].Candidate.NodeID
		}
		return collected[i].Candidate.IP < collected[j].Candidate.IP
	})
	return collected
}

func (t *Tester) checkOne(ctx context.Context, candidate provider.Candidate) Result {
	if net.ParseIP(candidate.IP) == nil {
		return Result{Candidate: candidate, Error: "invalid IP"}
	}
	endpoint, err := url.Parse(t.cfg.URL)
	if err != nil {
		return Result{Candidate: candidate, Error: err.Error()}
	}
	if endpoint.Scheme != "https" {
		return Result{Candidate: candidate, Error: "speed test URL must use https"}
	}
	serverName := endpoint.Hostname()
	if serverName == "" {
		return Result{Candidate: candidate, Error: "speed test URL host is empty"}
	}
	port := endpoint.Port()
	if port == "" {
		port = "443"
	}
	dialAddress := net.JoinHostPort(candidate.IP, port)

	reqCtx, cancel := context.WithTimeout(ctx, t.cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return Result{Candidate: candidate, Error: err.Error()}
	}
	req.Host = endpoint.Host
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", t.cfg.DownloadBytes-1))

	tlsConfig := &tls.Config{ServerName: serverName}
	if t.cfg.TLSClientConfig != nil {
		tlsConfig = t.cfg.TLSClientConfig.Clone()
		tlsConfig.ServerName = serverName
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network string, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, dialAddress)
		},
		TLSClientConfig:   tlsConfig,
		DisableKeepAlives: true,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	started := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return Result{Candidate: candidate, Error: err.Error()}
	}
	defer resp.Body.Close()
	ttfb := time.Since(started)

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return Result{Candidate: candidate, TTFB: ttfb, Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}

	downloadStarted := time.Now()
	n, err := io.CopyN(io.Discard, resp.Body, t.cfg.DownloadBytes)
	if err != nil && !errors.Is(err, io.EOF) {
		return Result{Candidate: candidate, TTFB: ttfb, DownloadBytes: n, Error: err.Error()}
	}
	if n == 0 {
		return Result{Candidate: candidate, TTFB: ttfb, Error: "no response body"}
	}
	downloadDuration := time.Since(downloadStarted)
	if downloadDuration <= 0 {
		downloadDuration = time.Nanosecond
	}
	speedBPS := int64(float64(n) / downloadDuration.Seconds())
	return Result{
		Candidate:        candidate,
		SpeedBPS:         speedBPS,
		DownloadBytes:    n,
		DownloadDuration: downloadDuration,
		TTFB:             ttfb,
		Success:          true,
	}
}

func dedupe(candidates []provider.Candidate) []provider.Candidate {
	seen := map[string]struct{}{}
	unique := make([]provider.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		key := candidate.NodeID + "|" + candidate.IP
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, candidate)
	}
	return unique
}
