package subscriptions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/config"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/remotesource"
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
		Mode:        " MERGE ",
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
	if len(got) != 1 || got[0].NodeIDs[0] != "ctcc" || got[0].Mode != "merge" || got[0].Shares[0] != "vless://uuid@old.example.com:443#name" {
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
		Mode:        "merge",
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
	if configSub.Mode != "rewrite" {
		t.Fatalf("static subscription mode was not defaulted: %#v", configSub)
	}
	stateSub, ok := manager.ConfigForToken("state-random-public-token")
	if !ok || stateSub.Name != "state" {
		t.Fatalf("missing writable subscription: %#v", stateSub)
	}
	if stateSub.Mode != "merge" {
		t.Fatalf("writable subscription mode was not preserved: %#v", stateSub)
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
	if created.Item.ShareCount != 1 || created.Item.NodeIDs[0] != "ctcc" || created.Item.Mode != "rewrite" {
		t.Fatalf("unexpected normalized item: %#v", created.Item)
	}

	updated, err := manager.Update(created.Item.ID, UpsertRequest{
		Name:        "renamed",
		Enabled:     true,
		PublicToken: created.Item.PublicToken,
		Format:      "base64",
		Mode:        "merge",
		NodeIDs:     []string{"bgp"},
		Shares:      []string{"trojan://pass@old.example.com:443#name"},
	}, "https://admin.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "renamed" || updated.NodeIDs[0] != "bgp" || updated.Mode != "merge" {
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

func TestManagerRejectsInvalidMode(t *testing.T) {
	manager, err := NewManager(nil, NewStore(filepath.Join(t.TempDir(), "subscriptions.json")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create(UpsertRequest{
		Enabled: true,
		Format:  "base64",
		Mode:    "invalid",
		Shares:  []string{"vless://uuid@old.example.com:443#name"},
	}, "https://admin.example.com"); err == nil {
		t.Fatal("expected invalid mode error")
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

func TestManagerMigratesLegacySharesToSources(t *testing.T) {
	static := []config.SubscriptionConfig{{
		ID:          "legacy",
		Name:        "legacy",
		Enabled:     true,
		PublicToken: "legacy-random-public-token",
		Key:         "legacy-key",
		Shares: []string{
			"vless://uuid@old.example.com:443#one",
			"trojan://pass@old.example.com:443#two",
		},
	}}
	manager, err := NewManager(static, NewStore(filepath.Join(t.TempDir(), "subscriptions.json")))
	if err != nil {
		t.Fatal(err)
	}

	item, err := manager.Get("config:legacy", "https://admin.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if item.SourceCount != 2 || item.DirectSourceCount != 2 || len(item.Sources) != 2 {
		t.Fatalf("legacy shares were not exposed as sources: %#v", item)
	}
	for i, source := range item.Sources {
		if source.ID == "" || source.Type != "share" || !source.Enabled || source.Share != static[0].Shares[i] {
			t.Fatalf("source[%d] = %#v", i, source)
		}
	}
	resolved, ok := manager.ConfigForToken(static[0].PublicToken)
	if !ok || len(resolved.Sources) != 2 || len(resolved.Shares) != 2 {
		t.Fatalf("resolved legacy subscription = %#v, ok = %v", resolved, ok)
	}
}

func TestManagerStaticOverridePersistsAndRestoreReturnsToConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	store := NewStore(path)
	static := []config.SubscriptionConfig{{
		ID:          "main",
		Name:        "from-config",
		Enabled:     true,
		PublicToken: "config-random-public-token",
		Key:         "config-key",
		Format:      "base64",
		Mode:        "rewrite",
		NodeIDs:     []string{"ctcc"},
		Sources: []config.SubscriptionSourceConfig{{
			ID: "direct", Type: "share", Enabled: true, Share: "vless://uuid@old.example.com:443#config",
		}},
	}}
	manager, err := NewManager(static, store)
	if err != nil {
		t.Fatal(err)
	}

	updated, err := manager.Update("config:main", UpsertRequest{
		Name:        "runtime-override",
		Enabled:     true,
		PublicToken: "override-random-public-token",
		Key:         "override-key",
		Format:      "base64",
		Mode:        "merge",
		NodeIDs:     []string{"bgp"},
		Sources: []config.SubscriptionSourceConfig{{
			ID: "direct", Type: "share", Enabled: true, Share: "trojan://pass@origin.example.com:443#override",
		}},
	}, "https://admin.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Overridden || updated.Source != "config" || updated.Name != "runtime-override" {
		t.Fatalf("updated static item = %#v", updated)
	}

	restarted, err := NewManager(static, store)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := restarted.Get("config:main", "https://admin.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.Overridden || persisted.Name != "runtime-override" || persisted.PublicToken != "override-random-public-token" || persisted.Mode != "merge" {
		t.Fatalf("persisted override = %#v", persisted)
	}
	if _, ok := restarted.ConfigForToken("config-random-public-token"); ok {
		t.Fatal("original token remained active while override was installed")
	}

	restored, err := restarted.Restore("config:main", "https://admin.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Overridden || restored.Name != "from-config" || restored.PublicToken != "config-random-public-token" || restored.Mode != "rewrite" {
		t.Fatalf("restored item = %#v", restored)
	}

	restartedAgain, err := NewManager(static, store)
	if err != nil {
		t.Fatal(err)
	}
	final, err := restartedAgain.Get("config:main", "https://admin.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if final.Overridden || final.Name != "from-config" {
		t.Fatalf("restore did not persist: %#v", final)
	}
}

type managerRemoteFetcher struct {
	result remotesource.Result
	err    error
}

func (f *managerRemoteFetcher) Fetch(context.Context, string) (remotesource.Result, error) {
	return f.result, f.err
}

func TestManagerConfigForTokenUsesLastGoodRemoteCache(t *testing.T) {
	cachePath := filepath.Join(t.TempDir(), "remote-cache.json")
	fetchedAt := time.Now().UTC().Truncate(time.Second)
	fetcher := &managerRemoteFetcher{result: remotesource.Result{
		Lines:     []string{"vless://remote-one", "trojan://remote-two"},
		Encoding:  remotesource.EncodingPlain,
		FetchedAt: fetchedAt,
	}}
	cache, err := NewRemoteCache(cachePath, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	static := []config.SubscriptionConfig{{
		ID:          "remote-sub",
		Enabled:     true,
		PublicToken: "remote-random-public-token",
		Key:         "remote-key",
		Format:      "base64",
		Mode:        "merge",
		Sources: []config.SubscriptionSourceConfig{{
			ID: "upstream", Type: "remote", Enabled: true, URL: "https://example.com/subscription",
		}},
	}}
	manager, err := NewManager(static, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetSourceResolver(cache)
	if _, err := manager.RefreshSources(context.Background(), "config:remote-sub", "https://admin.example.com"); err != nil {
		t.Fatal(err)
	}
	resolved, ok := manager.ConfigForToken("remote-random-public-token")
	if !ok || len(resolved.Shares) != 2 || resolved.Shares[0] != "vless://remote-one" {
		t.Fatalf("resolved remote subscription = %#v, ok = %v", resolved, ok)
	}

	fetcher.err = errors.New("upstream unavailable")
	if _, err := manager.RefreshSources(context.Background(), "config:remote-sub", "https://admin.example.com"); err != nil {
		t.Fatal(err)
	}
	resolved, ok = manager.ConfigForToken("remote-random-public-token")
	if !ok || len(resolved.Shares) != 2 || resolved.Shares[1] != "trojan://remote-two" {
		t.Fatalf("failed refresh discarded last-good lines: %#v, ok = %v", resolved, ok)
	}
	item, err := manager.Get("config:remote-sub", "https://admin.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if item.Health != "warning" || len(item.Sources) != 1 || item.Sources[0].Status != "warning" || item.Sources[0].ResolvedCount != 2 {
		t.Fatalf("failed refresh status = %#v", item)
	}

	reloadedCache, err := NewRemoteCache(cachePath, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewManager(static, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted.SetSourceResolver(reloadedCache)
	resolved, ok = restarted.ConfigForToken("remote-random-public-token")
	if !ok || len(resolved.Shares) != 2 {
		t.Fatalf("restarted manager lost last-good lines: %#v, ok = %v", resolved, ok)
	}
}

func TestManagerDraftRefreshDoesNotReplacePublishedRemoteCache(t *testing.T) {
	fetcher := &managerRemoteFetcher{result: remotesource.Result{
		Lines: []string{"vless://published"}, Encoding: remotesource.EncodingPlain, FetchedAt: time.Now().UTC(),
	}}
	cache, err := NewRemoteCache(filepath.Join(t.TempDir(), "remote-cache.json"), fetcher)
	if err != nil {
		t.Fatal(err)
	}
	static := []config.SubscriptionConfig{{
		ID: "main", Enabled: true, PublicToken: "draft-safe-public-token", Key: "draft-safe-key", Mode: "merge",
		Sources: []config.SubscriptionSourceConfig{{
			ID: "upstream", Type: "remote", Enabled: true, URL: "https://example.com/published",
		}},
	}}
	manager, err := NewManager(static, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetSourceResolver(cache)
	if _, err := manager.RefreshSources(context.Background(), "config:main", "https://admin.example.com"); err != nil {
		t.Fatal(err)
	}

	fetcher.result = remotesource.Result{
		Lines: []string{"vless://draft"}, Encoding: remotesource.EncodingPlain, FetchedAt: time.Now().UTC(),
	}
	draft := &UpsertRequest{
		Name: "draft", Enabled: true, PublicToken: "draft-safe-public-token", Format: "base64", Mode: "merge",
		Sources: []config.SubscriptionSourceConfig{{
			ID: "upstream", Type: "remote", Enabled: true, URL: "https://example.com/draft",
		}},
	}
	preview, err := manager.ResolveDraft(context.Background(), "config:main", draft, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Shares) != 1 || preview.Shares[0] != "vless://draft" {
		t.Fatalf("draft preview = %#v", preview.Shares)
	}
	published, ok := manager.ConfigForToken("draft-safe-public-token")
	if !ok || len(published.Shares) != 1 || published.Shares[0] != "vless://published" {
		t.Fatalf("draft refresh changed published cache: %#v, ok = %v", published.Shares, ok)
	}
}

func TestManagerYAMLTokenChangeDisablesStaleOverrideWithoutBlockingStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subscriptions.json")
	store := NewStore(path)
	base := []config.SubscriptionConfig{{
		Name: "YAML base", Enabled: true, PublicToken: "original-public-token", Key: "original-key", Mode: "merge",
		Sources: []config.SubscriptionSourceConfig{{
			ID: "origin", Type: "share", Enabled: true, Share: "vless://original",
		}},
	}}
	manager, err := NewManager(base, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Update("config:0", UpsertRequest{
		Name: "Web override", Enabled: true, PublicToken: "override-public-token", Key: "override-key", Format: "base64", Mode: "merge",
		Sources: []config.SubscriptionSourceConfig{{
			ID: "origin", Type: "share", Enabled: true, Share: "vless://override",
		}},
	}, "https://admin.example.com"); err != nil {
		t.Fatal(err)
	}

	changed := cloneStatic(base)
	changed[0].PublicToken = "changed-yaml-public-token"
	restarted, err := NewManager(changed, store)
	if err != nil {
		t.Fatalf("stale override blocked startup: %v", err)
	}
	item, err := restarted.Get("config:0", "https://admin.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if item.Overridden || item.Name != "YAML base" || item.PublicToken != "changed-yaml-public-token" {
		t.Fatalf("stale override remained active: %#v", item)
	}
	conflicts := restarted.OverrideConflicts()
	if len(conflicts) != 1 || conflicts[0].ID != "config:0" {
		t.Fatalf("override conflicts = %#v", conflicts)
	}

	if _, err := restarted.Create(UpsertRequest{
		Name: "state", Enabled: true, PublicToken: "state-public-token-123", Key: "state-key", Format: "base64", Mode: "merge",
		Sources: []config.SubscriptionSourceConfig{{Type: "share", Enabled: true, Share: "vless://state"}},
	}, "https://admin.example.com"); err != nil {
		t.Fatal(err)
	}
	file, err := store.LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := file.ConfigOverrides["config:0"]; !ok {
		t.Fatal("saving another subscription discarded the isolated override")
	}

	if _, err := restarted.Update("config:0", UpsertRequest{
		Name: "replacement override", Enabled: true, PublicToken: "replacement-public-token", Key: "replacement-key", Format: "base64", Mode: "merge",
		Sources: []config.SubscriptionSourceConfig{{
			ID: "origin", Type: "share", Enabled: true, Share: "vless://replacement",
		}},
	}, "https://admin.example.com"); err != nil {
		t.Fatal(err)
	}
	if conflicts := restarted.OverrideConflicts(); len(conflicts) != 0 {
		t.Fatalf("replacement override did not clear conflict: %#v", conflicts)
	}
	restartedAgain, err := NewManager(changed, store)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := restartedAgain.Get("config:0", "https://admin.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if !replacement.Overridden || replacement.Name != "replacement override" {
		t.Fatalf("replacement override did not persist: %#v", replacement)
	}
}

func TestManagerRejectsDuplicateYAMLSubscriptionIDs(t *testing.T) {
	static := []config.SubscriptionConfig{{ID: "duplicate"}, {ID: "duplicate"}}
	if _, err := NewManager(static, nil); err == nil {
		t.Fatal("expected duplicate YAML subscription id error")
	}
}
