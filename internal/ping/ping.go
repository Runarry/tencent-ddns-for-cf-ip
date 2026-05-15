package ping

import (
	"context"
	"net"
	"sort"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/provider"
)

type Config struct {
	Timeout      time.Duration
	Threshold    time.Duration
	Concurrency  int
	PacketsCount int
}

type Result struct {
	Candidate provider.Candidate
	Latency   time.Duration
	Alive     bool
	Error     string
}

type Prober struct {
	cfg Config
}

func NewProber(cfg Config) *Prober {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 3 * time.Second
	}
	if cfg.Threshold <= 0 {
		cfg.Threshold = 800 * time.Millisecond
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 32
	}
	if cfg.PacketsCount <= 0 {
		cfg.PacketsCount = 3
	}
	return &Prober{cfg: cfg}
}

func (p *Prober) Check(ctx context.Context, candidates []provider.Candidate) []Result {
	unique := dedupe(candidates)
	jobs := make(chan provider.Candidate)
	results := make(chan Result, len(unique))
	var wg sync.WaitGroup

	for i := 0; i < p.cfg.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for candidate := range jobs {
				results <- p.checkOne(ctx, candidate)
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
		return collected[i].Latency < collected[j].Latency
	})
	return collected
}

func (p *Prober) checkOne(ctx context.Context, candidate provider.Candidate) Result {
	if net.ParseIP(candidate.IP) == nil {
		return Result{Candidate: candidate, Error: "invalid IP"}
	}
	pinger, err := probing.NewPinger(candidate.IP)
	if err != nil {
		return Result{Candidate: candidate, Error: err.Error()}
	}
	pinger.Count = p.cfg.PacketsCount
	pinger.Timeout = p.cfg.Timeout
	pinger.SetPrivileged(true)

	done := make(chan error, 1)
	go func() {
		done <- pinger.Run()
	}()

	select {
	case <-ctx.Done():
		pinger.Stop()
		return Result{Candidate: candidate, Error: ctx.Err().Error()}
	case err := <-done:
		if err != nil {
			return Result{Candidate: candidate, Error: err.Error()}
		}
		stats := pinger.Statistics()
		if stats.PacketsRecv == 0 {
			return Result{Candidate: candidate, Error: "no ping response"}
		}
		latency := stats.AvgRtt
		if latency > p.cfg.Threshold {
			return Result{Candidate: candidate, Latency: latency, Error: "latency exceeds threshold"}
		}
		return Result{Candidate: candidate, Latency: latency, Alive: true}
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
