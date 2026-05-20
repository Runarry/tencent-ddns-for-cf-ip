package subscriptions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/config"
)

func TestStoreLoadMissingEmptyInvalidAndRoundTrip(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	entries, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("missing file entries = %#v", entries)
	}

	if err := os.WriteFile(store.path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("empty file entries = %#v", entries)
	}

	if err := os.WriteFile(store.path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("expected invalid JSON error")
	}

	want := []Entry{{
		ID:          "sub-1",
		Name:        "main",
		Enabled:     true,
		PublicToken: "long-random-public-token",
		Key:         "subscription-key",
		Format:      "base64",
		NodeIDs:     []string{"CTCC"},
		Shares:      []string{" vless://uuid@old.example.com:443#name "},
	}}
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].NodeIDs[0] != "ctcc" || got[0].Shares[0] != "vless://uuid@old.example.com:443#name" {
		t.Fatalf("round-trip entries = %#v", got)
	}
}

func TestManagerMergesStaticAndWritableSubscriptions(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	if err := store.Save([]Entry{{
		ID:          "state-sub",
		Name:        "state",
		Enabled:     true,
		PublicToken: "state-random-public-token",
		Key:         "state-key",
		Format:      "base64",
		NodeIDs:     []string{"bgp"},
		Shares:      []string{"trojan://pass@old.example.com:443#name"},
	}}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager([]config.SubscriptionConfig{{
		Name:        "config",
		Enabled:     true,
		PublicToken: "config-random-public-token",
		Key:         "config-key",
		Format:      "base64",
		NodeIDs:     []string{"ctcc"},
		Shares:      []string{"vless://uuid@old.example.com:443#name"},
	}}, store)
	if err != nil {
		t.Fatal(err)
	}

	configSub, ok := manager.ConfigForToken("config-random-public-token")
	if !ok || configSub.Name != "config" {
		t.Fatalf("missing static subscription: %#v", configSub)
	}
	stateSub, ok := manager.ConfigForToken("state-random-public-token")
	if !ok || stateSub.Name != "state" {
		t.Fatalf("missing writable subscription: %#v", stateSub)
	}
}

func TestManagerCreateUpdateDeleteAndRotate(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "subscriptions.json"))
	manager, err := NewManager(nil, store)
	if err != nil {
		t.Fatal(err)
	}

	created, err := manager.Create(UpsertRequest{
		Name:    "main",
		Enabled: true,
		Format:  "base64",
		NodeIDs: []string{"CTCC"},
		Shares:  []string{"vless://uuid@old.example.com:443#name"},
	}, "https://admin.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if created.Key == "" || created.Item.PublicToken == "" || !created.Item.Editable || !created.Item.HasKey {
		t.Fatalf("unexpected create response: %#v", created)
	}
	if created.Item.ShareCount != 1 || created.Item.NodeIDs[0] != "ctcc" {
		t.Fatalf("unexpected normalized item: %#v", created.Item)
	}

	updated, err := manager.Update(created.Item.ID, UpsertRequest{
		Name:        "renamed",
		Enabled:     true,
		PublicToken: created.Item.PublicToken,
		Format:      "base64",
		NodeIDs:     []string{"bgp"},
		Shares:      []string{"trojan://pass@old.example.com:443#name"},
	}, "https://admin.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "renamed" || updated.NodeIDs[0] != "bgp" {
		t.Fatalf("unexpected update response: %#v", updated)
	}

	rotated, err := manager.RotateSecret(created.Item.ID, "both", "https://admin.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Key == "" || rotated.Item.PublicToken == created.Item.PublicToken {
		t.Fatalf("unexpected rotate response: %#v", rotated)
	}

	if err := manager.Delete(created.Item.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.ConfigForToken(rotated.Item.PublicToken); ok {
		t.Fatal("deleted subscription is still available")
	}

	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	var saved File
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Subscriptions) != 0 {
		t.Fatalf("expected empty saved file, got %#v", saved.Subscriptions)
	}
}

func TestManagerRejectsDuplicateStaticToken(t *testing.T) {
	manager, err := NewManager([]config.SubscriptionConfig{{
		Enabled:     true,
		PublicToken: "long-random-public-token",
		Key:         "key",
		Format:      "base64",
		Shares:      []string{"vless://uuid@old.example.com:443#name"},
	}}, NewStore(filepath.Join(t.TempDir(), "subscriptions.json")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(UpsertRequest{
		Enabled:     true,
		PublicToken: "long-random-public-token",
		Key:         "another-key",
		Format:      "base64",
		Shares:      []string{"trojan://pass@old.example.com:443#name"},
	}, "https://admin.example.com"); err == nil {
		t.Fatal("expected duplicate token error")
	}
}
