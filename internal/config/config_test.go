package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMultipleSubscriptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`
provider:
  source: web
  nodeids: ["ctcc"]
dnspod:
  secret_id: id
  secret_key: key
  domain: example.com
sync:
  managed_prefix: cf
  default_nodeid: ctcc
  max_records_per_node: 1
  ping_threshold_ms: 1
  ping_concurrency: 1
  ping_packets: 1
api:
  bearer_token: secret
subscriptions:
  - name: ctcc-main
    enabled: true
    public_token: long-random-public-token
    key: " subscription-key "
    nodeids: ["CTCC"]
    shares:
      - "vless://uuid@old.example.com:443#name"
  - name: disabled
    enabled: false
    public_token: ""
    shares: []
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Subscriptions) != 2 {
		t.Fatalf("subscriptions = %#v", cfg.Subscriptions)
	}
	if cfg.Subscriptions[0].Format != "base64" || cfg.Subscriptions[0].Key != "subscription-key" || cfg.Subscriptions[0].NodeIDs[0] != "ctcc" {
		t.Fatalf("subscription was not normalized: %#v", cfg.Subscriptions[0])
	}
}

func TestLoadSpeedTestConfigAndEnvOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`
provider:
  source: web
  nodeids: ["ctcc"]
dnspod:
  secret_id: id
  secret_key: key
  domain: example.com
sync:
  managed_prefix: cf
  default_nodeid: ctcc
  max_records_per_node: 2
  ping_threshold_ms: 1
  ping_concurrency: 1
  ping_packets: 1
  speed_test:
    enabled: true
    url: "https://download.example.com/probe.bin"
api:
  bearer_token: secret
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNC_SPEED_TEST_DOWNLOAD_BYTES", "2048")
	t.Setenv("SYNC_SPEED_TEST_TIMEOUT", "5s")
	t.Setenv("SYNC_SPEED_TEST_CONCURRENCY", "3")
	t.Setenv("SYNC_SPEED_TEST_CANDIDATES_PER_NODE", "4")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Sync.SpeedTest.Enabled || cfg.Sync.SpeedTest.URL != "https://download.example.com/probe.bin" {
		t.Fatalf("speed test config was not loaded: %#v", cfg.Sync.SpeedTest)
	}
	if cfg.Sync.SpeedTest.DownloadBytes != 2048 || cfg.Sync.SpeedTest.Timeout.Duration.String() != "5s" || cfg.Sync.SpeedTest.Concurrency != 3 || cfg.Sync.SpeedTest.CandidatesPerNode != 4 {
		t.Fatalf("speed test env overrides were not applied: %#v", cfg.Sync.SpeedTest)
	}
}

func TestLoadSpeedTestDefaultCandidatesPerNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`
provider:
  source: web
  nodeids: ["ctcc"]
dnspod:
  secret_id: id
  secret_key: key
  domain: example.com
sync:
  managed_prefix: cf
  default_nodeid: ctcc
  max_records_per_node: 2
  ping_threshold_ms: 1
  ping_concurrency: 1
  ping_packets: 1
api:
  bearer_token: secret
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sync.SpeedTest.CandidatesPerNode != 6 {
		t.Fatalf("candidates_per_node = %d", cfg.Sync.SpeedTest.CandidatesPerNode)
	}
}

func TestRedactedHidesSubscriptionSecrets(t *testing.T) {
	cfg := Config{
		Subscriptions: []SubscriptionConfig{
			{
				Name:        "ctcc-main",
				Enabled:     true,
				PublicToken: "long-random-public-token",
				Key:         "subscription-key",
				Format:      "base64",
				NodeIDs:     []string{"ctcc"},
				Shares:      []string{"vless://uuid@old.example.com:443#name"},
			},
		},
	}
	redacted := cfg.Redacted()
	if redacted.Subscriptions[0].PublicToken != "" {
		t.Fatalf("public token was not redacted: %#v", redacted.Subscriptions[0])
	}
	if redacted.Subscriptions[0].Key != "" {
		t.Fatalf("subscription key was not redacted: %#v", redacted.Subscriptions[0])
	}
	if redacted.Subscriptions[0].Shares != nil {
		t.Fatalf("shares were not redacted: %#v", redacted.Subscriptions[0])
	}
	if redacted.Subscriptions[0].Name != "ctcc-main" || redacted.Subscriptions[0].NodeIDs[0] != "ctcc" {
		t.Fatalf("diagnostic fields were not preserved: %#v", redacted.Subscriptions[0])
	}
	if cfg.Subscriptions[0].PublicToken == "" || cfg.Subscriptions[0].Key == "" || cfg.Subscriptions[0].Shares == nil {
		t.Fatalf("redaction mutated original config: %#v", cfg.Subscriptions[0])
	}
}

func TestSubscriptionValidation(t *testing.T) {
	cfg := validConfig()
	cfg.Subscriptions = []SubscriptionConfig{
		{
			Enabled:     true,
			PublicToken: "long-random-public-token",
			Key:         "subscription-key",
			Format:      "base64",
			Shares:      []string{"vless://uuid@old.example.com:443#name"},
		},
		{
			Enabled:     true,
			PublicToken: "another-random-public-token",
			Key:         "another-subscription-key",
			Format:      "base64",
			Shares:      []string{"trojan://pass@old.example.com:443#name"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{
			name: "duplicate token",
			mutate: func(c *Config) {
				c.Subscriptions[1].PublicToken = c.Subscriptions[0].PublicToken
			},
		},
		{
			name: "empty key",
			mutate: func(c *Config) {
				c.Subscriptions[0].Key = ""
			},
		},
		{
			name: "short token",
			mutate: func(c *Config) {
				c.Subscriptions[0].PublicToken = "short"
			},
		},
		{
			name: "empty shares",
			mutate: func(c *Config) {
				c.Subscriptions[0].Shares = nil
			},
		},
		{
			name: "invalid format",
			mutate: func(c *Config) {
				c.Subscriptions[0].Format = "plain"
			},
		},
		{
			name: "deprecated single subscription",
			mutate: func(c *Config) {
				c.DeprecatedSubscription = &SubscriptionConfig{}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := cfg
			candidate.Subscriptions = append([]SubscriptionConfig(nil), cfg.Subscriptions...)
			tt.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestSpeedTestValidation(t *testing.T) {
	cfg := validConfig()
	cfg.Sync.SpeedTest.Enabled = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing speed test URL error")
	}
	cfg.Sync.SpeedTest.URL = "http://example.com/probe.bin"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected non-https speed test URL error")
	}
	cfg.Sync.SpeedTest.URL = "https://example.com/probe.bin"
	cfg.Sync.SpeedTest.DownloadBytes = 1024
	cfg.Sync.SpeedTest.Timeout.Duration = 8 * time.Second
	cfg.Sync.SpeedTest.Concurrency = 1
	cfg.Sync.SpeedTest.CandidatesPerNode = 1
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func validConfig() Config {
	return Config{
		Provider: ProviderConfig{Source: "web", NodeIDs: []string{"ctcc"}},
		DNSPod:   DNSPodConfig{SecretID: "id", SecretKey: "key", Domain: "example.com"},
		Sync: SyncConfig{
			ManagedPrefix:     "cf",
			DefaultNodeID:     "ctcc",
			MaxRecordsPerNode: 1,
			PingThresholdMS:   1,
			PingConcurrency:   1,
			PingPackets:       1,
			SpeedTest: SpeedTestConfig{
				DownloadBytes:     1024 * 1024,
				Timeout:           Duration{Duration: 8 * time.Second},
				Concurrency:       8,
				CandidatesPerNode: 3,
			},
		},
		API: APIConfig{BearerToken: "secret"},
	}
}
