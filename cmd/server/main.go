package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/api"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/config"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/dnspod"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/ping"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/provider"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/speedtest"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/state"
	syncsvc "github.com/sleep/tencent-ddns-for-cf-ip/internal/sync"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfgPath := os.Getenv("CONFIG_FILE")
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}

	store := state.NewStore(cfg.State.File)
	currentState, err := store.Load()
	if err != nil {
		logger.Warn("load state failed; starting with empty state", "error", err)
		currentState = state.Empty()
	}

	providerClient := provider.NewClient(provider.Config{
		Source:      cfg.Provider.Source,
		Endpoint:    cfg.Provider.URL,
		APIEndpoint: cfg.Provider.APIURL,
		WebURL:      cfg.Provider.WebURL,
		Username:    cfg.Provider.Username,
		Key:         cfg.Provider.Key,
		HTTPClient: &http.Client{
			Timeout: cfg.Provider.Timeout.Duration,
		},
	})
	pinger := ping.NewProber(ping.Config{
		Timeout:      cfg.Sync.PingTimeout.Duration,
		Threshold:    time.Duration(cfg.Sync.PingThresholdMS) * time.Millisecond,
		Concurrency:  cfg.Sync.PingConcurrency,
		PacketsCount: cfg.Sync.PingPackets,
	})
	var speedTester syncsvc.SpeedTester
	if cfg.Sync.SpeedTest.Enabled {
		speedTester = speedtest.NewTester(speedtest.Config{
			URL:           cfg.Sync.SpeedTest.URL,
			DownloadBytes: cfg.Sync.SpeedTest.DownloadBytes,
			Timeout:       cfg.Sync.SpeedTest.Timeout.Duration,
			Concurrency:   cfg.Sync.SpeedTest.Concurrency,
		})
	}
	dnsClient, err := dnspod.NewClient(dnspod.Config{
		SecretID:   cfg.DNSPod.SecretID,
		SecretKey:  cfg.DNSPod.SecretKey,
		Domain:     cfg.DNSPod.Domain,
		RecordLine: cfg.DNSPod.RecordLine,
		TTL:        cfg.DNSPod.TTL,
	})
	if err != nil {
		logger.Error("create dnspod client", "error", err)
		os.Exit(1)
	}

	service := syncsvc.NewService(syncsvc.Config{
		NodeIDs:              cfg.Provider.NodeIDs,
		ManagedPrefix:        cfg.Sync.ManagedPrefix,
		ManagedBaseSubdomain: cfg.Sync.ManagedBaseSubdomain,
		NodeLabels:           cfg.Sync.NodeLabels,
		DefaultNodeID:        cfg.Sync.DefaultNodeID,
		MaxRecordsPerNode:    cfg.Sync.MaxRecordsPerNode,
		Domain:               cfg.DNSPod.Domain,
		RecordLine:           cfg.DNSPod.RecordLine,
		TTL:                  cfg.DNSPod.TTL,
		Interval:             cfg.Sync.Interval.Duration,
		SpeedTest: syncsvc.SpeedTestConfig{
			Enabled:           cfg.Sync.SpeedTest.Enabled,
			CandidatesPerNode: cfg.Sync.SpeedTest.CandidatesPerNode,
		},
		Fallback: syncsvc.FallbackConfig{
			Enabled:           cfg.Sync.Fallback.Enabled,
			WildcardSubdomain: cfg.Sync.Fallback.WildcardSubdomain,
			Target:            cfg.Sync.Fallback.Target,
			Type:              cfg.Sync.Fallback.Type,
		},
	}, providerClient, pinger, speedTester, dnsClient, store, currentState, logger)

	root := api.NewServer(api.Config{
		Token:         cfg.API.BearerToken,
		Subscriptions: cfg.Subscriptions,
	}, service, cfg.Redacted())
	server := &http.Server{
		Addr:              cfg.API.ListenAddr,
		Handler:           root,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	service.Start(ctx)

	go func() {
		logger.Info("http server listening", "addr", cfg.API.ListenAddr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server failed", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	service.Stop()
}
