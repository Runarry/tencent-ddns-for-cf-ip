package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var raw string
	if err := value.Decode(&raw); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("parse duration %q: %w", raw, err)
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Duration.String())
}

type Config struct {
	Provider               ProviderConfig       `yaml:"provider" json:"provider"`
	DNSPod                 DNSPodConfig         `yaml:"dnspod" json:"dnspod"`
	Sync                   SyncConfig           `yaml:"sync" json:"sync"`
	API                    APIConfig            `yaml:"api" json:"api"`
	State                  StateConfig          `yaml:"state" json:"state"`
	Subscriptions          []SubscriptionConfig `yaml:"subscriptions" json:"subscriptions,omitempty"`
	DeprecatedSubscription *SubscriptionConfig  `yaml:"subscription" json:"-"`
}

type ProviderConfig struct {
	Source   string   `yaml:"source" json:"source"`
	URL      string   `yaml:"url" json:"url"`
	APIURL   string   `yaml:"api_url" json:"api_url"`
	WebURL   string   `yaml:"web_url" json:"web_url"`
	Username string   `yaml:"username" json:"username"`
	Key      string   `yaml:"key" json:"-"`
	NodeIDs  []string `yaml:"nodeids" json:"nodeids"`
	Timeout  Duration `yaml:"timeout" json:"timeout"`
}

type DNSPodConfig struct {
	SecretID   string `yaml:"secret_id" json:"secret_id"`
	SecretKey  string `yaml:"secret_key" json:"-"`
	Domain     string `yaml:"domain" json:"domain"`
	RecordLine string `yaml:"record_line" json:"record_line"`
	TTL        uint64 `yaml:"ttl" json:"ttl"`
}

type SyncConfig struct {
	Interval             Duration          `yaml:"interval" json:"interval"`
	ManagedPrefix        string            `yaml:"managed_prefix" json:"managed_prefix"`
	ManagedBaseSubdomain string            `yaml:"managed_base_subdomain" json:"managed_base_subdomain"`
	NodeLabels           map[string]string `yaml:"node_labels" json:"node_labels,omitempty"`
	DefaultNodeID        string            `yaml:"default_nodeid" json:"default_nodeid"`
	MaxRecordsPerNode    int               `yaml:"max_records_per_node" json:"max_records_per_node"`
	PingTimeout          Duration          `yaml:"ping_timeout" json:"ping_timeout"`
	PingThresholdMS      int               `yaml:"ping_threshold_ms" json:"ping_threshold_ms"`
	PingConcurrency      int               `yaml:"ping_concurrency" json:"ping_concurrency"`
	PingPackets          int               `yaml:"ping_packets" json:"ping_packets"`
	Fallback             FallbackConfig    `yaml:"fallback" json:"fallback"`
}

type FallbackConfig struct {
	Enabled           bool   `yaml:"enabled" json:"enabled"`
	WildcardSubdomain string `yaml:"wildcard_subdomain" json:"wildcard_subdomain"`
	Target            string `yaml:"target" json:"target"`
	Type              string `yaml:"type" json:"type"`
}

type APIConfig struct {
	ListenAddr  string `yaml:"listen_addr" json:"listen_addr"`
	BearerToken string `yaml:"bearer_token" json:"-"`
}

type StateConfig struct {
	File string `yaml:"state_file" json:"state_file"`
}

type SubscriptionConfig struct {
	Name        string   `yaml:"name" json:"name,omitempty"`
	Enabled     bool     `yaml:"enabled" json:"enabled"`
	PublicToken string   `yaml:"public_token" json:"public_token,omitempty"`
	Key         string   `yaml:"key" json:"-"`
	Shares      []string `yaml:"shares" json:"shares,omitempty"`
	Format      string   `yaml:"format" json:"format"`
	NodeIDs     []string `yaml:"nodeids" json:"nodeids,omitempty"`
}

func Load(path string) (Config, error) {
	cfg := defaults()
	if _, err := os.Stat(path); err == nil {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, err
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return Config{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, err
	}

	applyEnv(&cfg)
	normalize(&cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func normalize(cfg *Config) {
	cfg.Provider.Source = strings.ToLower(strings.TrimSpace(cfg.Provider.Source))
	if cfg.Provider.URL == "" {
		if cfg.Provider.Source == "api" {
			cfg.Provider.URL = cfg.Provider.APIURL
		} else {
			cfg.Provider.URL = cfg.Provider.WebURL
		}
	}
	for i := range cfg.Subscriptions {
		sub := &cfg.Subscriptions[i]
		sub.Name = strings.TrimSpace(sub.Name)
		sub.PublicToken = strings.TrimSpace(sub.PublicToken)
		sub.Key = strings.TrimSpace(sub.Key)
		sub.Format = strings.ToLower(strings.TrimSpace(sub.Format))
		if sub.Format == "" {
			sub.Format = "base64"
		}
		shares := make([]string, 0, len(sub.Shares))
		for _, share := range sub.Shares {
			if share = strings.TrimSpace(share); share != "" {
				shares = append(shares, share)
			}
		}
		sub.Shares = shares

		nodeIDs := make([]string, 0, len(sub.NodeIDs))
		for _, nodeID := range sub.NodeIDs {
			if nodeID = strings.ToLower(strings.TrimSpace(nodeID)); nodeID != "" {
				nodeIDs = append(nodeIDs, nodeID)
			}
		}
		sub.NodeIDs = nodeIDs
	}
}

func defaults() Config {
	return Config{
		Provider: ProviderConfig{
			Source:  "web",
			URL:     "https://api.uouin.com/cloudflare.html",
			APIURL:  "https://api.uouin.com/app/cloudflare",
			WebURL:  "https://api.uouin.com/cloudflare.html",
			NodeIDs: []string{"ctcc", "cmcc", "cucc"},
			Timeout: Duration{
				Duration: 20 * time.Second,
			},
		},
		DNSPod: DNSPodConfig{
			RecordLine: "默认",
			TTL:        600,
		},
		Sync: SyncConfig{
			Interval:          Duration{Duration: 10 * time.Minute},
			ManagedPrefix:     "cf",
			DefaultNodeID:     "ctcc",
			MaxRecordsPerNode: 5,
			PingTimeout:       Duration{Duration: 3 * time.Second},
			PingThresholdMS:   800,
			PingConcurrency:   32,
			PingPackets:       3,
			Fallback: FallbackConfig{
				Type: "CNAME",
			},
		},
		API: APIConfig{
			ListenAddr: ":8080",
		},
		State: StateConfig{
			File: "/data/state.json",
		},
	}
}

func applyEnv(cfg *Config) {
	setString(&cfg.Provider.URL, "PROVIDER_URL")
	setString(&cfg.Provider.Source, "PROVIDER_SOURCE")
	setString(&cfg.Provider.APIURL, "PROVIDER_API_URL")
	setString(&cfg.Provider.WebURL, "PROVIDER_WEB_URL")
	setString(&cfg.Provider.Username, "PROVIDER_USERNAME")
	setString(&cfg.Provider.Key, "PROVIDER_KEY")
	setCSV(&cfg.Provider.NodeIDs, "PROVIDER_NODEIDS")
	setDuration(&cfg.Provider.Timeout, "PROVIDER_TIMEOUT")

	setString(&cfg.DNSPod.SecretID, "DNSPOD_SECRET_ID")
	setString(&cfg.DNSPod.SecretKey, "DNSPOD_SECRET_KEY")
	setString(&cfg.DNSPod.Domain, "DNSPOD_DOMAIN")
	setString(&cfg.DNSPod.RecordLine, "DNSPOD_RECORD_LINE")
	setUint64(&cfg.DNSPod.TTL, "DNSPOD_TTL")

	setDuration(&cfg.Sync.Interval, "SYNC_INTERVAL")
	setString(&cfg.Sync.ManagedPrefix, "SYNC_MANAGED_PREFIX")
	setString(&cfg.Sync.ManagedBaseSubdomain, "SYNC_MANAGED_BASE_SUBDOMAIN")
	setString(&cfg.Sync.DefaultNodeID, "SYNC_DEFAULT_NODEID")
	setInt(&cfg.Sync.MaxRecordsPerNode, "SYNC_MAX_RECORDS_PER_NODE")
	setDuration(&cfg.Sync.PingTimeout, "SYNC_PING_TIMEOUT")
	setInt(&cfg.Sync.PingThresholdMS, "SYNC_PING_THRESHOLD_MS")
	setInt(&cfg.Sync.PingConcurrency, "SYNC_PING_CONCURRENCY")
	setInt(&cfg.Sync.PingPackets, "SYNC_PING_PACKETS")
	setBool(&cfg.Sync.Fallback.Enabled, "SYNC_FALLBACK_ENABLED")
	setString(&cfg.Sync.Fallback.WildcardSubdomain, "SYNC_FALLBACK_WILDCARD_SUBDOMAIN")
	setString(&cfg.Sync.Fallback.Target, "SYNC_FALLBACK_TARGET")
	setString(&cfg.Sync.Fallback.Type, "SYNC_FALLBACK_TYPE")

	setString(&cfg.API.ListenAddr, "API_LISTEN_ADDR")
	setString(&cfg.API.BearerToken, "API_BEARER_TOKEN")
	setString(&cfg.State.File, "STATE_FILE")
}

func (c Config) Validate() error {
	var missing []string
	if strings.EqualFold(c.Provider.Source, "api") && c.Provider.Username == "" {
		missing = append(missing, "provider.username or PROVIDER_USERNAME")
	}
	if strings.EqualFold(c.Provider.Source, "api") && c.Provider.Key == "" {
		missing = append(missing, "provider.key or PROVIDER_KEY")
	}
	if c.DNSPod.SecretID == "" {
		missing = append(missing, "dnspod.secret_id or DNSPOD_SECRET_ID")
	}
	if c.DNSPod.SecretKey == "" {
		missing = append(missing, "dnspod.secret_key or DNSPOD_SECRET_KEY")
	}
	if c.DNSPod.Domain == "" {
		missing = append(missing, "dnspod.domain or DNSPOD_DOMAIN")
	}
	if c.API.BearerToken == "" {
		missing = append(missing, "api.bearer_token or API_BEARER_TOKEN")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required config: %s", strings.Join(missing, ", "))
	}
	if len(c.Provider.NodeIDs) == 0 {
		return errors.New("provider.nodeids must not be empty")
	}
	if c.Provider.Source != "web" && c.Provider.Source != "api" {
		return errors.New("provider.source must be web or api")
	}
	if c.Sync.ManagedPrefix == "" {
		return errors.New("sync.managed_prefix must not be empty")
	}
	if c.Sync.DefaultNodeID == "" {
		return errors.New("sync.default_nodeid must not be empty")
	}
	if c.Sync.MaxRecordsPerNode < 1 {
		return errors.New("sync.max_records_per_node must be greater than 0")
	}
	if c.Sync.PingThresholdMS < 1 {
		return errors.New("sync.ping_threshold_ms must be greater than 0")
	}
	if c.Sync.PingConcurrency < 1 {
		return errors.New("sync.ping_concurrency must be greater than 0")
	}
	if c.Sync.PingPackets < 1 {
		return errors.New("sync.ping_packets must be greater than 0")
	}
	if c.Sync.Fallback.Enabled {
		if c.Sync.Fallback.WildcardSubdomain == "" {
			return errors.New("sync.fallback.wildcard_subdomain must not be empty when fallback is enabled")
		}
		if c.Sync.Fallback.Target == "" {
			return errors.New("sync.fallback.target must not be empty when fallback is enabled")
		}
		if c.Sync.Fallback.Type == "" {
			return errors.New("sync.fallback.type must not be empty when fallback is enabled")
		}
	}
	if c.DeprecatedSubscription != nil {
		return errors.New("subscription is no longer supported; use subscriptions list instead")
	}
	seenTokens := map[string]struct{}{}
	for i, sub := range c.Subscriptions {
		format := strings.ToLower(strings.TrimSpace(sub.Format))
		if format == "" {
			format = "base64"
		}
		if format != "base64" {
			return fmt.Errorf("subscriptions[%d].format must be base64", i)
		}
		if !sub.Enabled {
			continue
		}
		if sub.PublicToken == "" {
			return fmt.Errorf("subscriptions[%d].public_token must not be empty when subscription is enabled", i)
		}
		if sub.Key == "" {
			return fmt.Errorf("subscriptions[%d].key must not be empty when subscription is enabled", i)
		}
		if len(sub.PublicToken) < 16 {
			return fmt.Errorf("subscriptions[%d].public_token must be at least 16 characters", i)
		}
		if strings.Contains(sub.PublicToken, "/") {
			return fmt.Errorf("subscriptions[%d].public_token must be a single path segment", i)
		}
		if _, exists := seenTokens[sub.PublicToken]; exists {
			return fmt.Errorf("subscriptions[%d].public_token must be unique among enabled subscriptions", i)
		}
		seenTokens[sub.PublicToken] = struct{}{}
		if len(sub.Shares) == 0 {
			return fmt.Errorf("subscriptions[%d].shares must not be empty when subscription is enabled", i)
		}
	}
	return nil
}

func (c Config) Redacted() Config {
	c.Provider.Key = ""
	c.DNSPod.SecretKey = ""
	c.API.BearerToken = ""
	c.Subscriptions = append([]SubscriptionConfig(nil), c.Subscriptions...)
	for i := range c.Subscriptions {
		c.Subscriptions[i].PublicToken = ""
		c.Subscriptions[i].Key = ""
		c.Subscriptions[i].Shares = nil
	}
	return c
}

func setString(target *string, key string) {
	if value := os.Getenv(key); value != "" {
		*target = value
	}
}

func setCSV(target *[]string, key string) {
	value := os.Getenv(key)
	if value == "" {
		return
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	*target = result
}

func setDuration(target *Duration, key string) {
	value := os.Getenv(key)
	if value == "" {
		return
	}
	parsed, err := time.ParseDuration(value)
	if err == nil {
		target.Duration = parsed
	}
}

func setInt(target *int, key string) {
	value := os.Getenv(key)
	if value == "" {
		return
	}
	parsed, err := strconv.Atoi(value)
	if err == nil {
		*target = parsed
	}
}

func setBool(target *bool, key string) {
	value := os.Getenv(key)
	if value == "" {
		return
	}
	parsed, err := strconv.ParseBool(value)
	if err == nil {
		*target = parsed
	}
}

func setUint64(target *uint64, key string) {
	value := os.Getenv(key)
	if value == "" {
		return
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err == nil {
		*target = parsed
	}
}
