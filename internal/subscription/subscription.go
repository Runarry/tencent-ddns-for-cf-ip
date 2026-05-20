package subscription

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"sort"
	"strings"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/state"
)

var (
	ErrNoTargets     = errors.New("no preferred subscription targets available")
	ErrNoValidShares = errors.New("no valid subscription shares available")
	ErrUnsupported   = errors.New("unsupported share protocol")
)

type Config struct {
	Shares  []string
	Format  string
	NodeIDs []string
}

func Generate(cfg Config, records []state.Record) (string, error) {
	targets := PreferredTargets(records, cfg.NodeIDs)
	if len(targets) == 0 {
		return "", ErrNoTargets
	}

	var lines []string
	for _, share := range cfg.Shares {
		share = strings.TrimSpace(share)
		if share == "" {
			continue
		}
		for _, target := range targets {
			rewritten, err := RewriteShare(share, target)
			if err != nil {
				continue
			}
			lines = append(lines, rewritten)
		}
	}
	if len(lines) == 0 {
		return "", ErrNoValidShares
	}

	body := strings.Join(lines, "\n") + "\n"
	return base64.StdEncoding.EncodeToString([]byte(body)), nil
}

func PreferredTargets(records []state.Record, nodeIDs []string) []string {
	allowed := allowedNodeIDs(nodeIDs)
	candidates := make([]state.Record, 0, len(records))
	for _, record := range records {
		fqdn := strings.TrimSpace(record.FQDN)
		if fqdn == "" || record.NodeID == "fallback" || strings.HasPrefix(fqdn, "*.") || strings.HasPrefix(record.Name, "*.") {
			continue
		}
		if len(allowed) > 0 {
			if _, ok := allowed[strings.ToLower(strings.TrimSpace(record.NodeID))]; !ok {
				continue
			}
		}
		record.FQDN = strings.TrimSuffix(fqdn, ".")
		candidates = append(candidates, record)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].SpeedBPS != candidates[j].SpeedBPS {
			return candidates[i].SpeedBPS > candidates[j].SpeedBPS
		}
		if candidates[i].LatencyMS != candidates[j].LatencyMS {
			return candidates[i].LatencyMS < candidates[j].LatencyMS
		}
		return candidates[i].FQDN < candidates[j].FQDN
	})

	targets := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		key := strings.ToLower(candidate.FQDN)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, candidate.FQDN)
	}
	return targets
}

func allowedNodeIDs(nodeIDs []string) map[string]struct{} {
	if len(nodeIDs) == 0 {
		return nil
	}
	allowed := map[string]struct{}{}
	for _, nodeID := range nodeIDs {
		if nodeID = strings.ToLower(strings.TrimSpace(nodeID)); nodeID != "" {
			allowed[nodeID] = struct{}{}
		}
	}
	return allowed
}

func RewriteShare(share string, target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", ErrNoTargets
	}
	share = strings.TrimSpace(share)

	scheme, rest, ok := strings.Cut(share, "://")
	if !ok {
		return "", ErrUnsupported
	}
	switch strings.ToLower(scheme) {
	case "vmess":
		return rewriteVMess(rest, target)
	case "vless", "trojan", "hysteria", "hysteria2":
		return rewriteURLShare(share, target)
	case "ss":
		return rewriteShadowsocks(share, target)
	default:
		return "", ErrUnsupported
	}
}

func rewriteVMess(payload string, target string) (string, error) {
	decoded, err := decodeBase64Flexible(payload)
	if err != nil {
		return "", err
	}
	var obj map[string]any
	if err := json.Unmarshal(decoded, &obj); err != nil {
		return "", err
	}
	obj["add"] = target

	encoded, err := json.Marshal(obj)
	if err != nil {
		return "", err
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(encoded), nil
}

func rewriteURLShare(share string, target string) (string, error) {
	parsed, err := url.Parse(share)
	if err != nil {
		return "", err
	}
	if parsed.Host == "" {
		return "", ErrUnsupported
	}
	replaceHost(parsed, target)
	return parsed.String(), nil
}

func rewriteShadowsocks(share string, target string) (string, error) {
	parsed, err := url.Parse(share)
	if err == nil && parsed.Host != "" && parsed.User != nil {
		replaceHost(parsed, target)
		return parsed.String(), nil
	}
	return rewriteLegacyShadowsocks(share, target)
}

func rewriteLegacyShadowsocks(share string, target string) (string, error) {
	payload := strings.TrimPrefix(share, "ss://")
	encoded, fragment, _ := strings.Cut(payload, "#")
	decoded, err := decodeBase64Flexible(encoded)
	if err != nil {
		return "", err
	}

	parsed, err := url.Parse("ss://" + string(decoded))
	if err != nil {
		return "", err
	}
	if parsed.Host == "" {
		return "", ErrUnsupported
	}
	replaceHost(parsed, target)

	rebuilt := strings.TrimPrefix(parsed.String(), "ss://")
	result := "ss://" + base64.StdEncoding.EncodeToString([]byte(rebuilt))
	if fragment != "" {
		result += "#" + fragment
	}
	return result, nil
}

func replaceHost(parsed *url.URL, target string) {
	port := parsed.Port()
	if port == "" {
		parsed.Host = hostLiteral(target)
		return
	}
	parsed.Host = net.JoinHostPort(target, port)
}

func hostLiteral(host string) string {
	if strings.Contains(host, ":") && net.ParseIP(host) != nil {
		return "[" + host + "]"
	}
	return host
}

func decodeBase64Flexible(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.URLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.StdEncoding.DecodeString(padBase64(value))
}

func padBase64(value string) string {
	if rem := len(value) % 4; rem != 0 {
		value += strings.Repeat("=", 4-rem)
	}
	return value
}
