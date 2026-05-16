package subscription

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/state"
)

func TestRewriteVMessReplacesAddOnly(t *testing.T) {
	obj := map[string]any{
		"v":    "2",
		"ps":   "node",
		"add":  "old.example.com",
		"port": "443",
		"id":   "uuid",
		"net":  "ws",
		"path": "/ray",
		"host": "origin.example.com",
		"sni":  "sni.example.com",
	}
	raw, _ := json.Marshal(obj)
	link := "vmess://" + base64.StdEncoding.EncodeToString(raw)

	rewritten, err := RewriteShare(link, "cf-ctcc-01.example.com")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(rewritten, "vmess://"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(decoded, &got); err != nil {
		t.Fatal(err)
	}
	if got["add"] != "cf-ctcc-01.example.com" {
		t.Fatalf("add was not replaced: %#v", got)
	}
	if got["sni"] != "sni.example.com" || got["host"] != "origin.example.com" || got["path"] != "/ray" {
		t.Fatalf("transport fields changed: %#v", got)
	}
}

func TestRewriteURLProtocolsReplaceAuthorityHostOnly(t *testing.T) {
	tests := []string{
		"vless://uuid@old.example.com:443?security=tls&sni=sni.example.com&type=ws&host=origin.example.com#name",
		"trojan://pass@old.example.com:8443?security=reality&sni=sni.example.com&pbk=key#name",
		"hysteria://auth@old.example.com:443?security=tls&sni=sni.example.com#name",
		"hysteria2://auth@old.example.com:443?security=tls&sni=sni.example.com#name",
	}

	for _, input := range tests {
		rewritten, err := RewriteShare(input, "cf-ctcc-01.example.com")
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		parsed, err := url.Parse(rewritten)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Hostname() != "cf-ctcc-01.example.com" {
			t.Fatalf("host was not replaced: %s", rewritten)
		}
		if parsed.Port() == "" {
			t.Fatalf("port was not preserved: %s", rewritten)
		}
		if parsed.Query().Get("sni") != "sni.example.com" {
			t.Fatalf("sni changed: %s", rewritten)
		}
		if parsed.Fragment != "name" {
			t.Fatalf("fragment changed: %s", rewritten)
		}
	}
}

func TestRewriteShadowsocksSIP002AndLegacy(t *testing.T) {
	sip002 := "ss://" + base64.StdEncoding.EncodeToString([]byte("aes-128-gcm:pass")) + "@old.example.com:8388?plugin=v2ray-plugin#ss"
	rewritten, err := RewriteShare(sip002, "cf-ctcc-01.example.com")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(rewritten)
	if parsed.Hostname() != "cf-ctcc-01.example.com" || parsed.Query().Get("plugin") != "v2ray-plugin" || parsed.Fragment != "ss" {
		t.Fatalf("unexpected sip002 rewrite: %s", rewritten)
	}

	legacyPayload := base64.StdEncoding.EncodeToString([]byte("aes-128-gcm:pass@old.example.com:8388"))
	legacy := "ss://" + legacyPayload + "#legacy"
	rewritten, err = RewriteShare(legacy, "cf-ctcc-02.example.com")
	if err != nil {
		t.Fatal(err)
	}
	encoded := strings.TrimPrefix(rewritten, "ss://")
	encoded, fragment, _ := strings.Cut(encoded, "#")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != "aes-128-gcm:pass@cf-ctcc-02.example.com:8388" || fragment != "legacy" {
		t.Fatalf("unexpected legacy rewrite: %s %s", decoded, fragment)
	}
}

func TestRewriteURLPreservesIPv6AndNoPort(t *testing.T) {
	rewritten, err := RewriteShare("vless://uuid@[2001:db8::1]:443?security=tls#v6", "cf.example.com")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(rewritten)
	if parsed.Hostname() != "cf.example.com" || parsed.Port() != "443" {
		t.Fatalf("unexpected IPv6 rewrite: %s", rewritten)
	}

	rewritten, err = RewriteShare("vless://uuid@old.example.com?security=none#noport", "cf.example.com")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ = url.Parse(rewritten)
	if parsed.Host != "cf.example.com" || parsed.Fragment != "noport" {
		t.Fatalf("unexpected no-port rewrite: %s", rewritten)
	}
}

func TestGenerateUsesPreferredFQDNsAndBase64Subscription(t *testing.T) {
	records := []state.Record{
		{Name: "*.cdn", FQDN: "*.cdn.example.com", NodeID: "fallback", LatencyMS: 1, UpdatedAt: time.Now()},
		{Name: "cf-bgp-01.cdn", FQDN: "cf-bgp-01.cdn.example.com", NodeID: "bgp", LatencyMS: 90, UpdatedAt: time.Now()},
		{Name: "cf-ctcc-01.cdn", FQDN: "cf-ctcc-01.cdn.example.com", NodeID: "ctcc", LatencyMS: 40, UpdatedAt: time.Now()},
	}
	out, err := Generate(Config{
		Format: "base64",
		Shares: []string{
			"vless://uuid@old.example.com:443?security=tls&sni=sni.example.com#vless",
			"trojan://pass@old.example.com:443?security=tls&sni=sni.example.com#trojan",
		},
	}, records)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(decoded)), "\n")
	if len(lines) != 4 {
		t.Fatalf("line count = %d: %q", len(lines), decoded)
	}
	if !strings.Contains(lines[0], "@cf-ctcc-01.cdn.example.com:443") || !strings.Contains(lines[1], "@cf-bgp-01.cdn.example.com:443") {
		t.Fatalf("targets not sorted by latency: %q", decoded)
	}
	if strings.Contains(string(decoded), "*.cdn.example.com") {
		t.Fatalf("fallback target leaked into subscription: %q", decoded)
	}
}

func TestGenerateFiltersTargetsByNodeID(t *testing.T) {
	records := []state.Record{
		{Name: "cf-bgp-01.cdn", FQDN: "cf-bgp-01.cdn.example.com", NodeID: "bgp", LatencyMS: 10, UpdatedAt: time.Now()},
		{Name: "cf-ctcc-01.cdn", FQDN: "cf-ctcc-01.cdn.example.com", NodeID: "ctcc", LatencyMS: 40, UpdatedAt: time.Now()},
	}
	out, err := Generate(Config{
		Format:  "base64",
		NodeIDs: []string{"ctcc"},
		Shares:  []string{"vless://uuid@old.example.com:443?security=tls#vless"},
	}, records)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(out)
	if err != nil {
		t.Fatal(err)
	}
	body := string(decoded)
	if !strings.Contains(body, "@cf-ctcc-01.cdn.example.com:443") {
		t.Fatalf("ctcc target missing: %q", body)
	}
	if strings.Contains(body, "@cf-bgp-01.cdn.example.com:443") {
		t.Fatalf("bgp target leaked into filtered subscription: %q", body)
	}
}
