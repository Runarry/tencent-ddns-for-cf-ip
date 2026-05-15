package config

import "testing"

func TestRedactedHidesSubscriptionSecrets(t *testing.T) {
	cfg := Config{
		Subscription: SubscriptionConfig{
			Enabled:     true,
			PublicToken: "long-random-public-token",
			Format:      "base64",
			Shares:      []string{"vless://uuid@old.example.com:443#name"},
		},
	}
	redacted := cfg.Redacted()
	if redacted.Subscription.PublicToken != "" {
		t.Fatalf("public token was not redacted: %#v", redacted.Subscription)
	}
	if redacted.Subscription.Shares != nil {
		t.Fatalf("shares were not redacted: %#v", redacted.Subscription)
	}
}

func TestSubscriptionValidation(t *testing.T) {
	cfg := Config{
		Provider: ProviderConfig{Source: "web", NodeIDs: []string{"ctcc"}},
		DNSPod:   DNSPodConfig{SecretID: "id", SecretKey: "key", Domain: "example.com"},
		Sync: SyncConfig{
			ManagedPrefix:     "cf",
			DefaultNodeID:     "ctcc",
			MaxRecordsPerNode: 1,
			PingThresholdMS:   1,
			PingConcurrency:   1,
			PingPackets:       1,
		},
		API: APIConfig{BearerToken: "secret"},
		Subscription: SubscriptionConfig{
			Enabled:     true,
			PublicToken: "long-random-public-token",
			Format:      "base64",
			Shares:      []string{"vless://uuid@old.example.com:443#name"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	cfg.Subscription.PublicToken = "short"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected short subscription token to fail validation")
	}
}
