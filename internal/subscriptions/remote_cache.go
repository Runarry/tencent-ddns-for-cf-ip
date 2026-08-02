package subscriptions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/config"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/remotesource"
)

type remoteFetcher interface {
	Fetch(context.Context, string) (remotesource.Result, error)
}

type remoteCacheEntry struct {
	URL           string    `json:"url"`
	Lines         []string  `json:"lines,omitempty"`
	Encoding      string    `json:"encoding,omitempty"`
	LastAttemptAt time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	Error         string    `json:"error,omitempty"`
}

type remoteCacheFile struct {
	Version int                         `json:"version"`
	Entries map[string]remoteCacheEntry `json:"entries"`
}

// RemoteCache persists the last successful content for remote subscription
// sources. Public subscription reads only call ResolveCached and never perform
// network I/O.
type RemoteCache struct {
	mu         sync.RWMutex
	path       string
	fetcher    remoteFetcher
	entries    map[string]remoteCacheEntry
	refreshing map[string]bool
}

func NewRemoteCache(path string, fetcher remoteFetcher) (*RemoteCache, error) {
	cache := &RemoteCache{
		path:       strings.TrimSpace(path),
		fetcher:    fetcher,
		entries:    map[string]remoteCacheEntry{},
		refreshing: map[string]bool{},
	}
	if cache.path == "" {
		return cache, nil
	}
	data, err := os.ReadFile(cache.path)
	if errors.Is(err, os.ErrNotExist) {
		return cache, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return cache, nil
	}
	var file remoteCacheFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	for key, entry := range file.Entries {
		entry.Lines = append([]string(nil), entry.Lines...)
		cache.entries[key] = entry
	}
	return cache, nil
}

func (c *RemoteCache) ResolveCached(cacheKey string, source config.SubscriptionSourceConfig) SourceResolution {
	if c == nil {
		return SourceResolution{Status: SourceStatus{State: "never"}}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.resolveLocked(cacheKey, source)
}

func (c *RemoteCache) Refresh(ctx context.Context, cacheKey string, source config.SubscriptionSourceConfig) SourceResolution {
	if c == nil || c.fetcher == nil {
		return SourceResolution{Status: SourceStatus{State: "error", Error: "remote source fetcher is not configured"}}
	}
	cacheKey = strings.TrimSpace(cacheKey)
	c.mu.Lock()
	if c.refreshing[cacheKey] {
		resolution := c.resolveLocked(cacheKey, source)
		resolution.Status.State = "refreshing"
		c.mu.Unlock()
		return resolution
	}
	c.refreshing[cacheKey] = true
	c.mu.Unlock()

	result, fetchErr := c.fetcher.Fetch(ctx, source.URL)
	now := time.Now().UTC()
	c.mu.Lock()
	delete(c.refreshing, cacheKey)
	entry := c.entries[cacheKey]
	if entry.URL != source.URL {
		entry = remoteCacheEntry{URL: source.URL}
	}
	entry.LastAttemptAt = now
	if fetchErr != nil {
		entry.Error = fetchErr.Error()
	} else {
		entry.URL = source.URL
		entry.Lines = append([]string(nil), result.Lines...)
		entry.Encoding = result.Encoding
		entry.LastSuccessAt = result.FetchedAt.UTC()
		entry.Error = ""
	}
	c.entries[cacheKey] = entry
	saveErr := c.saveLocked()
	resolution := c.resolveLocked(cacheKey, source)
	if saveErr != nil {
		if resolution.Status.Error == "" {
			resolution.Status.Error = "save remote source cache: " + saveErr.Error()
		}
		if resolution.Status.HasCache {
			resolution.Status.State = "warning"
		} else {
			resolution.Status.State = "error"
		}
	}
	c.mu.Unlock()
	return resolution
}

func (c *RemoteCache) resolveLocked(cacheKey string, source config.SubscriptionSourceConfig) SourceResolution {
	entry, ok := c.entries[cacheKey]
	if !ok || entry.URL != source.URL {
		if c.refreshing[cacheKey] {
			return SourceResolution{Status: SourceStatus{State: "refreshing"}}
		}
		return SourceResolution{Status: SourceStatus{State: "never"}}
	}
	status := SourceStatus{
		State:         "healthy",
		ResolvedCount: len(entry.Lines),
		Encoding:      entry.Encoding,
		Error:         entry.Error,
		HasCache:      !entry.LastSuccessAt.IsZero(),
	}
	if !entry.LastSuccessAt.IsZero() {
		value := entry.LastSuccessAt
		status.LastSuccessAt = &value
	}
	if entry.Error != "" {
		if status.HasCache {
			status.State = "warning"
		} else {
			status.State = "error"
		}
	} else if !status.HasCache {
		status.State = "never"
	}
	if c.refreshing[cacheKey] {
		status.State = "refreshing"
	}
	return SourceResolution{Lines: append([]string(nil), entry.Lines...), Status: status}
}

func (c *RemoteCache) saveLocked() error {
	if c.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return err
	}
	file := remoteCacheFile{Version: 1, Entries: make(map[string]remoteCacheEntry, len(c.entries))}
	for key, entry := range c.entries {
		entry.Lines = append([]string(nil), entry.Lines...)
		file.Entries[key] = entry
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		_ = os.Remove(c.path)
		return os.Rename(tmp, c.path)
	}
	return nil
}
