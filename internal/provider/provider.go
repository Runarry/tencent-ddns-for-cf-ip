package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

type Config struct {
	Source      string
	Endpoint    string
	APIEndpoint string
	WebURL      string
	Username    string
	Key         string
	HTTPClient  *http.Client
}

type Client struct {
	source      string
	endpoint    string
	apiEndpoint string
	webURL      string
	username    string
	key         string
	httpClient  *http.Client
}

type Candidate struct {
	NodeID    string `json:"nodeid"`
	IP        string `json:"ip"`
	APIPing   string `json:"api_ping,omitempty"`
	Loss      string `json:"loss,omitempty"`
	Speed     string `json:"speed,omitempty"`
	Bandwidth string `json:"bandwidth,omitempty"`
}

type response struct {
	Message string              `json:"msg"`
	Data    map[string]nodeData `json:"data"`
	Status  string              `json:"statu"`
	Code    flexibleInt         `json:"code"`
}

type nodeData struct {
	Code   flexibleInt `json:"code"`
	Info   []ipInfo    `json:"info"`
	Uptime int64       `json:"uptime"`
}

type ipInfo struct {
	IP        string `json:"ip"`
	Loss      string `json:"loss"`
	Ping      string `json:"ping"`
	Speed     string `json:"speed"`
	Bandwidth string `json:"bandwidth"`
}

type flexibleInt int

func (f *flexibleInt) UnmarshalJSON(data []byte) error {
	var n int
	if err := json.Unmarshal(data, &n); err == nil {
		*f = flexibleInt(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return err
	}
	*f = flexibleInt(parsed)
	return nil
}

func NewClient(cfg Config) *Client {
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	source := strings.ToLower(strings.TrimSpace(cfg.Source))
	if source == "" {
		source = "web"
	}
	if cfg.Endpoint == "" {
		if source == "api" {
			cfg.Endpoint = cfg.APIEndpoint
		} else {
			cfg.Endpoint = cfg.WebURL
		}
	}
	return &Client{
		source:      source,
		endpoint:    cfg.Endpoint,
		apiEndpoint: cfg.APIEndpoint,
		webURL:      cfg.WebURL,
		username:    cfg.Username,
		key:         cfg.Key,
		httpClient:  httpClient,
	}
}

func (c *Client) Fetch(ctx context.Context, nodeIDs []string) (map[string][]Candidate, error) {
	if c.source == "api" {
		return c.fetchAPI(ctx, nodeIDs)
	}
	return c.fetchWeb(ctx, nodeIDs)
}

func (c *Client) fetchAPI(ctx context.Context, nodeIDs []string) (map[string][]Candidate, error) {
	endpoint, err := url.Parse(c.endpoint)
	if err != nil {
		return nil, err
	}
	q := endpoint.Query()
	q.Set("username", c.username)
	q.Set("key", c.key)
	if len(nodeIDs) > 0 {
		q.Set("nodeid", strings.Join(nodeIDs, "|"))
	}
	q.Set("url", "cloudflare")
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}

	var parsed response
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	if int(parsed.Code) != 200 || parsed.Status != "true" {
		return nil, fmt.Errorf("provider returned code %d: %s", parsed.Code, parsed.Message)
	}
	result := make(map[string][]Candidate, len(parsed.Data))
	for nodeID, data := range parsed.Data {
		if int(data.Code) != 200 {
			continue
		}
		for _, item := range data.Info {
			ip := strings.TrimSpace(item.IP)
			if ip == "" {
				continue
			}
			result[nodeID] = append(result[nodeID], Candidate{
				NodeID:    nodeID,
				IP:        ip,
				APIPing:   item.Ping,
				Loss:      item.Loss,
				Speed:     item.Speed,
				Bandwidth: item.Bandwidth,
			})
		}
	}
	return result, nil
}

func (c *Client) fetchWeb(ctx context.Context, nodeIDs []string) (map[string][]Candidate, error) {
	endpoint := c.endpoint
	if endpoint == "" {
		endpoint = c.webURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provider web page returned HTTP %d", resp.StatusCode)
	}
	candidates, err := parseWebPage(resp.Body)
	if err != nil {
		return nil, err
	}
	return filterNodes(candidates, nodeIDs), nil
}

func parseWebPage(r io.Reader) (map[string][]Candidate, error) {
	root, err := html.Parse(r)
	if err != nil {
		return nil, err
	}
	tokens := textTokens(root)
	result := map[string][]Candidate{}
	for i := 0; i < len(tokens)-1; i++ {
		nodeID, ok := lineToNodeID(tokens[i])
		if !ok {
			continue
		}
		ip := strings.Trim(tokens[i+1], "[]")
		if net.ParseIP(ip) == nil {
			continue
		}
		candidate := Candidate{NodeID: nodeID, IP: ip}
		if i+2 < len(tokens) {
			candidate.Loss = tokens[i+2]
		}
		if i+3 < len(tokens) {
			candidate.APIPing = tokens[i+3]
		}
		if i+4 < len(tokens) {
			candidate.Speed = tokens[i+4]
		}
		if i+5 < len(tokens) {
			candidate.Bandwidth = tokens[i+5]
		}
		result[nodeID] = append(result[nodeID], candidate)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no Cloudflare IP rows found in provider web page")
	}
	return result, nil
}

func textTokens(node *html.Node) []string {
	var tokens []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			for _, token := range strings.Fields(n.Data) {
				token = strings.TrimSpace(token)
				if token != "" {
					tokens = append(tokens, token)
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return tokens
}

func lineToNodeID(line string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(line)) {
	case "电信":
		return "ctcc", true
	case "联通":
		return "cucc", true
	case "移动":
		return "cmcc", true
	case "多线":
		return "bgp", true
	case "IPV6", "IPv6", "ipv6":
		return "ipv6", true
	default:
		return "", false
	}
}

func filterNodes(input map[string][]Candidate, nodeIDs []string) map[string][]Candidate {
	if len(nodeIDs) == 0 {
		return input
	}
	allowed := map[string]struct{}{}
	for _, nodeID := range nodeIDs {
		allowed[strings.ToLower(strings.TrimSpace(nodeID))] = struct{}{}
	}
	result := map[string][]Candidate{}
	for nodeID, candidates := range input {
		if _, ok := allowed[nodeID]; ok {
			result[nodeID] = candidates
		}
	}
	return result
}
