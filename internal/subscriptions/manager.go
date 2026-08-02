package subscriptions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/config"
)

const (
	defaultFormat      = "base64"
	defaultMode        = "rewrite"
	currentFileVersion = 2
)

var (
	ErrNotFound     = errors.New("subscription not found")
	ErrNotEditable  = errors.New("subscription is not editable")
	ErrInvalidInput = errors.New("invalid subscription")
)

type Entry struct {
	ID              string                            `json:"id"`
	Name            string                            `json:"name,omitempty"`
	Enabled         bool                              `json:"enabled"`
	PublicToken     string                            `json:"public_token"`
	Key             string                            `json:"key"`
	Shares          []string                          `json:"shares,omitempty"`
	Sources         []config.SubscriptionSourceConfig `json:"sources,omitempty"`
	Format          string                            `json:"format"`
	NodeIDs         []string                          `json:"nodeids,omitempty"`
	Mode            string                            `json:"mode"`
	BaseFingerprint string                            `json:"base_fingerprint,omitempty"`
}

type File struct {
	Version         int              `json:"version,omitempty"`
	Subscriptions   []Entry          `json:"subscriptions"`
	ConfigOverrides map[string]Entry `json:"config_overrides,omitempty"`
}

type Store struct {
	path string
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Load() ([]Entry, error) {
	file, err := s.LoadFile()
	return file.Subscriptions, err
}

func (s *Store) LoadFile() (File, error) {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return File{Version: currentFileVersion}, nil
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return File{Version: currentFileVersion}, nil
	}
	if err != nil {
		return File{}, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return File{Version: currentFileVersion}, nil
	}
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return File{}, err
	}
	if file.Version == 0 {
		file.Version = 1
	}
	file.Subscriptions = append([]Entry(nil), file.Subscriptions...)
	for i := range file.Subscriptions {
		normalizeEntry(&file.Subscriptions[i], false)
	}
	if file.ConfigOverrides == nil {
		file.ConfigOverrides = map[string]Entry{}
	}
	for id, entry := range file.ConfigOverrides {
		entry.ID = id
		normalizeEntry(&entry, false)
		file.ConfigOverrides[id] = entry
	}
	return file, nil
}

func (s *Store) Save(entries []Entry) error {
	file := File{Version: currentFileVersion, Subscriptions: append([]Entry(nil), entries...)}
	if current, err := s.LoadFile(); err == nil {
		file.ConfigOverrides = current.ConfigOverrides
	}
	return s.SaveFile(file)
}

func (s *Store) SaveFile(file File) error {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	file.Version = currentFileVersion
	file.Subscriptions = append([]Entry(nil), file.Subscriptions...)
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(s.path)
		return os.Rename(tmp, s.path)
	}
	return nil
}

type SourceStatus struct {
	State         string     `json:"status"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	ResolvedCount int        `json:"resolved_count"`
	Encoding      string     `json:"encoding,omitempty"`
	Error         string     `json:"error,omitempty"`
	HasCache      bool       `json:"has_cache"`
}

type SourceResolution struct {
	Lines  []string
	Status SourceStatus
}

type SourceResolver interface {
	ResolveCached(cacheKey string, source config.SubscriptionSourceConfig) SourceResolution
	Refresh(context.Context, string, config.SubscriptionSourceConfig) SourceResolution
}

type Manager struct {
	mu                sync.RWMutex
	static            []config.SubscriptionConfig
	store             *Store
	entries           []Entry
	overrides         map[string]Entry
	orphanOverrides   map[string]Entry
	overrideConflicts []OverrideConflict
	resolver          SourceResolver
}

type OverrideConflict struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

func NewManager(static []config.SubscriptionConfig, store *Store) (*Manager, error) {
	file := File{Version: currentFileVersion, ConfigOverrides: map[string]Entry{}}
	var err error
	if store != nil {
		file, err = store.LoadFile()
		if err != nil {
			return nil, err
		}
	}
	static = cloneStatic(static)
	for i := range static {
		normalizeConfig(&static[i], staticSubscriptionID(i, static[i]), false)
	}
	entries := append([]Entry(nil), file.Subscriptions...)
	for i := range entries {
		normalizeEntry(&entries[i], false)
		if err := validateEntry(entries[i]); err != nil {
			return nil, fmt.Errorf("subscriptions[%d]: %w", i, err)
		}
	}
	overrides := map[string]Entry{}
	orphanOverrides := map[string]Entry{}
	conflicts := make([]OverrideConflict, 0)
	staticByID := make(map[string]config.SubscriptionConfig, len(static))
	for i, sub := range static {
		id := staticSubscriptionID(i, sub)
		if _, exists := staticByID[id]; exists {
			return nil, fmt.Errorf("duplicate YAML subscription id %q", id)
		}
		staticByID[id] = sub
	}
	for id, entry := range file.ConfigOverrides {
		base, ok := staticByID[id]
		if !strings.HasPrefix(id, "config:") || !ok {
			orphanOverrides[id] = entry
			conflicts = append(conflicts, OverrideConflict{ID: id, Reason: "未找到对应的 YAML 订阅，覆盖已停用"})
			continue
		}
		entry.ID = id
		normalizeEntry(&entry, false)
		if err := validateEntry(entry); err != nil {
			orphanOverrides[id] = entry
			conflicts = append(conflicts, OverrideConflict{ID: id, Reason: "覆盖内容无效，已停用: " + err.Error()})
			continue
		}
		fingerprint := staticSubscriptionFingerprint(base)
		if entry.BaseFingerprint != "" && entry.BaseFingerprint != fingerprint {
			orphanOverrides[id] = entry
			conflicts = append(conflicts, OverrideConflict{ID: id, Reason: "YAML 基础配置已变化，旧覆盖已停用，请重新编辑后保存"})
			continue
		}
		entry.BaseFingerprint = fingerprint
		overrides[id] = entry
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].ID < conflicts[j].ID })
	manager := &Manager{
		static: static, store: store, entries: entries, overrides: overrides,
		orphanOverrides: orphanOverrides, overrideConflicts: conflicts,
	}
	if err := manager.validateUniqueTokensLocked(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *Manager) SetSourceResolver(resolver SourceResolver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resolver = resolver
}

func (m *Manager) OverrideConflicts() []OverrideConflict {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]OverrideConflict(nil), m.overrideConflicts...)
}

func (m *Manager) PublicSubscriptions() []config.SubscriptionConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	refs := m.effectiveSubscriptionsLocked()
	result := make([]config.SubscriptionConfig, 0, len(refs))
	for _, ref := range refs {
		result = append(result, m.resolveConfigLocked(ref.ID, ref.Config))
	}
	return result
}

type effectiveSubscription struct {
	ID         string
	Source     string
	Overridden bool
	Config     config.SubscriptionConfig
}

func (m *Manager) effectiveSubscriptionsLocked() []effectiveSubscription {
	result := make([]effectiveSubscription, 0, len(m.static)+len(m.entries))
	for i, sub := range m.static {
		id := staticSubscriptionID(i, sub)
		if override, ok := m.overrides[id]; ok {
			result = append(result, effectiveSubscription{ID: id, Source: "config", Overridden: true, Config: override.Config()})
			continue
		}
		result = append(result, effectiveSubscription{ID: id, Source: "config", Config: cloneConfig(sub)})
	}
	for _, entry := range m.entries {
		result = append(result, effectiveSubscription{ID: entry.ID, Source: "state", Config: entry.Config()})
	}
	return result
}

type SourceListItem struct {
	ID            string     `json:"id"`
	Name          string     `json:"name,omitempty"`
	Type          string     `json:"type"`
	Enabled       bool       `json:"enabled"`
	Share         string     `json:"share,omitempty"`
	URL           string     `json:"url,omitempty"`
	Status        string     `json:"status"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	ResolvedCount int        `json:"resolved_count"`
	Encoding      string     `json:"encoding,omitempty"`
	Error         string     `json:"error,omitempty"`
}

type ListItem struct {
	ID                 string           `json:"id"`
	Source             string           `json:"source"`
	Editable           bool             `json:"editable"`
	Overridden         bool             `json:"overridden"`
	Name               string           `json:"name,omitempty"`
	Enabled            bool             `json:"enabled"`
	PublicToken        string           `json:"public_token,omitempty"`
	HasKey             bool             `json:"has_key"`
	Format             string           `json:"format"`
	NodeIDs            []string         `json:"nodeids,omitempty"`
	Mode               string           `json:"mode"`
	Shares             []string         `json:"shares,omitempty"`
	Sources            []SourceListItem `json:"sources,omitempty"`
	ShareCount         int              `json:"share_count"`
	SourceCount        int              `json:"source_count"`
	DirectSourceCount  int              `json:"direct_source_count"`
	RemoteSourceCount  int              `json:"remote_source_count"`
	ResolvedShareCount int              `json:"resolved_share_count"`
	OutputCount        int              `json:"output_count"`
	Health             string           `json:"health"`
	LastRefreshAt      *time.Time       `json:"last_refresh_at,omitempty"`
	URLTemplate        string           `json:"url_template,omitempty"`
}

func (m *Manager) List(baseURL string) []ListItem {
	m.mu.RLock()
	defer m.mu.RUnlock()
	refs := m.effectiveSubscriptionsLocked()
	items := make([]ListItem, 0, len(refs))
	for _, ref := range refs {
		items = append(items, m.listItemLocked(ref, baseURL, false))
	}
	return items
}

func (m *Manager) Get(id string, baseURL string) (ListItem, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ref, ok := m.findEffectiveLocked(id)
	if !ok {
		return ListItem{}, ErrNotFound
	}
	return m.listItemLocked(ref, baseURL, true), nil
}

type UpsertRequest struct {
	Name        string                            `json:"name"`
	Enabled     bool                              `json:"enabled"`
	PublicToken string                            `json:"public_token"`
	Key         string                            `json:"key"`
	Shares      []string                          `json:"shares"`
	Sources     []config.SubscriptionSourceConfig `json:"sources"`
	Format      string                            `json:"format"`
	NodeIDs     []string                          `json:"nodeids"`
	Mode        string                            `json:"mode"`
}

type MutationResult struct {
	Item ListItem `json:"item"`
	Key  string   `json:"key,omitempty"`
}

func (m *Manager) Create(req UpsertRequest, baseURL string) (MutationResult, error) {
	entry := entryFromRequest(randomToken(18), req, Entry{})
	if entry.PublicToken == "" {
		entry.PublicToken = randomToken(24)
	}
	if entry.Key == "" {
		entry.Key = randomToken(32)
	}
	normalizeEntry(&entry, true)
	if err := validateEntry(entry); err != nil {
		return MutationResult{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, entry)
	if err := m.validateUniqueTokensLocked(); err != nil {
		m.entries = m.entries[:len(m.entries)-1]
		return MutationResult{}, err
	}
	if err := m.saveLocked(); err != nil {
		m.entries = m.entries[:len(m.entries)-1]
		return MutationResult{}, err
	}
	ref := effectiveSubscription{ID: entry.ID, Source: "state", Config: entry.Config()}
	return MutationResult{Item: m.listItemLocked(ref, baseURL, true), Key: entry.Key}, nil
}

func (m *Manager) Update(id string, req UpsertRequest, baseURL string) (ListItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ref, ok := m.findEffectiveLocked(id)
	if !ok {
		return ListItem{}, ErrNotFound
	}
	current := entryFromConfig(id, ref.Config)
	updated := entryFromRequest(id, req, current)
	normalizeEntry(&updated, true)
	if err := validateEntry(updated); err != nil {
		return ListItem{}, err
	}

	if strings.HasPrefix(id, "config:") {
		updated.BaseFingerprint = m.staticFingerprintLocked(id)
		previous, existed := m.overrides[id]
		m.overrides[id] = updated
		if err := m.validateUniqueTokensLocked(); err != nil {
			if existed {
				m.overrides[id] = previous
			} else {
				delete(m.overrides, id)
			}
			return ListItem{}, err
		}
		if err := m.saveLocked(); err != nil {
			if existed {
				m.overrides[id] = previous
			} else {
				delete(m.overrides, id)
			}
			return ListItem{}, err
		}
		m.clearOverrideConflictLocked(id)
		return m.listItemLocked(effectiveSubscription{ID: id, Source: "config", Overridden: true, Config: updated.Config()}, baseURL, true), nil
	}

	index := m.indexLocked(id)
	if index < 0 {
		return ListItem{}, ErrNotFound
	}
	previous := m.entries[index]
	m.entries[index] = updated
	if err := m.validateUniqueTokensLocked(); err != nil {
		m.entries[index] = previous
		return ListItem{}, err
	}
	if err := m.saveLocked(); err != nil {
		m.entries[index] = previous
		return ListItem{}, err
	}
	return m.listItemLocked(effectiveSubscription{ID: id, Source: "state", Config: updated.Config()}, baseURL, true), nil
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.HasPrefix(id, "config:") {
		return ErrNotEditable
	}
	index := m.indexLocked(id)
	if index < 0 {
		return ErrNotFound
	}
	previous := append([]Entry(nil), m.entries...)
	m.entries = append(m.entries[:index], m.entries[index+1:]...)
	if err := m.saveLocked(); err != nil {
		m.entries = previous
		return err
	}
	return nil
}

func (m *Manager) Restore(id string, baseURL string) (ListItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !strings.HasPrefix(id, "config:") {
		return ListItem{}, ErrNotEditable
	}
	if _, ok := m.overrides[id]; !ok {
		return ListItem{}, ErrNotFound
	}
	previous := m.overrides[id]
	delete(m.overrides, id)
	if err := m.validateUniqueTokensLocked(); err != nil {
		m.overrides[id] = previous
		return ListItem{}, err
	}
	if err := m.saveLocked(); err != nil {
		m.overrides[id] = previous
		return ListItem{}, err
	}
	ref, ok := m.findEffectiveLocked(id)
	if !ok {
		return ListItem{}, ErrNotFound
	}
	return m.listItemLocked(ref, baseURL, true), nil
}

type RotateRequest struct {
	Target string `json:"target"`
}

func (m *Manager) RotateSecret(id string, target string, baseURL string) (MutationResult, error) {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		target = "key"
	}
	if target != "key" && target != "public_token" && target != "both" {
		return MutationResult{}, fmt.Errorf("%w: target must be key, public_token, or both", ErrInvalidInput)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	ref, ok := m.findEffectiveLocked(id)
	if !ok {
		return MutationResult{}, ErrNotFound
	}
	updated := entryFromConfig(id, ref.Config)
	if target == "key" || target == "both" {
		updated.Key = randomToken(32)
	}
	if target == "public_token" || target == "both" {
		updated.PublicToken = randomToken(24)
	}

	if strings.HasPrefix(id, "config:") {
		updated.BaseFingerprint = m.staticFingerprintLocked(id)
		previous, existed := m.overrides[id]
		m.overrides[id] = updated
		if err := m.validateUniqueTokensLocked(); err != nil {
			if existed {
				m.overrides[id] = previous
			} else {
				delete(m.overrides, id)
			}
			return MutationResult{}, err
		}
		if err := m.saveLocked(); err != nil {
			if existed {
				m.overrides[id] = previous
			} else {
				delete(m.overrides, id)
			}
			return MutationResult{}, err
		}
		m.clearOverrideConflictLocked(id)
		result := MutationResult{Item: m.listItemLocked(effectiveSubscription{ID: id, Source: "config", Overridden: true, Config: updated.Config()}, baseURL, true)}
		if target == "key" || target == "both" {
			result.Key = updated.Key
		}
		return result, nil
	}

	index := m.indexLocked(id)
	if index < 0 {
		return MutationResult{}, ErrNotFound
	}
	previous := m.entries[index]
	m.entries[index] = updated
	if err := m.validateUniqueTokensLocked(); err != nil {
		m.entries[index] = previous
		return MutationResult{}, err
	}
	if err := m.saveLocked(); err != nil {
		m.entries[index] = previous
		return MutationResult{}, err
	}
	result := MutationResult{Item: m.listItemLocked(effectiveSubscription{ID: id, Source: "state", Config: updated.Config()}, baseURL, true)}
	if target == "key" || target == "both" {
		result.Key = updated.Key
	}
	return result, nil
}

func (m *Manager) SecretURL(id string, baseURL string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ref, ok := m.findEffectiveLocked(id)
	if !ok {
		return "", ErrNotFound
	}
	if ref.Config.PublicToken == "" || ref.Config.Key == "" {
		return "", fmt.Errorf("%w: subscription token and key must be configured", ErrInvalidInput)
	}
	return strings.TrimRight(baseURL, "/") + "/sub/" + url.PathEscape(ref.Config.PublicToken) + "?key=" + url.QueryEscape(ref.Config.Key), nil
}

func (m *Manager) ConfigForToken(token string) (config.SubscriptionConfig, bool) {
	token = strings.TrimSpace(token)
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, ref := range m.effectiveSubscriptionsLocked() {
		if ref.Config.Enabled && ref.Config.PublicToken == token {
			return m.resolveConfigLocked(ref.ID, ref.Config), true
		}
	}
	return config.SubscriptionConfig{}, false
}

func (m *Manager) ConfigForID(id string) (config.SubscriptionConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ref, ok := m.findEffectiveLocked(id)
	if !ok {
		return config.SubscriptionConfig{}, false
	}
	return m.resolveConfigLocked(ref.ID, ref.Config), true
}

// ResolveDraft validates an optional unsaved request against an existing
// subscription, optionally refreshes its remote sources, and returns the
// resolved shares without persisting the draft.
func (m *Manager) ResolveDraft(ctx context.Context, id string, req *UpsertRequest, refresh bool) (config.SubscriptionConfig, error) {
	m.mu.RLock()
	ref, ok := m.findEffectiveLocked(id)
	resolver := m.resolver
	m.mu.RUnlock()
	if !ok {
		return config.SubscriptionConfig{}, ErrNotFound
	}
	sub := cloneConfig(ref.Config)
	resolutionID := id
	if req != nil {
		entry := entryFromRequest(id, *req, entryFromConfig(id, sub))
		normalizeEntry(&entry, true)
		if err := validateEntry(entry); err != nil {
			return config.SubscriptionConfig{}, err
		}
		sub = entry.Config()
		resolutionID = draftSubscriptionCacheID(id, sub)
	}
	if refresh && resolver != nil {
		refreshRemoteSources(ctx, resolver, []effectiveSubscription{{ID: resolutionID, Config: sub}})
	}
	return resolveConfigWithResolver(resolutionID, sub, resolver), nil
}

func (m *Manager) RefreshDraftSources(ctx context.Context, id string, req *UpsertRequest, baseURL string) (ListItem, error) {
	sub, err := m.ResolveDraft(ctx, id, req, true)
	if err != nil {
		return ListItem{}, err
	}
	m.mu.RLock()
	ref, ok := m.findEffectiveLocked(id)
	if !ok {
		m.mu.RUnlock()
		return ListItem{}, ErrNotFound
	}
	ref.Config = sub
	if req != nil {
		ref.ID = draftSubscriptionCacheID(id, sub)
	}
	item := m.listItemLocked(ref, baseURL, true)
	item.ID = id
	m.mu.RUnlock()
	return item, nil
}

func (m *Manager) RefreshSources(ctx context.Context, id string, baseURL string) (ListItem, error) {
	m.mu.RLock()
	ref, ok := m.findEffectiveLocked(id)
	resolver := m.resolver
	m.mu.RUnlock()
	if !ok {
		return ListItem{}, ErrNotFound
	}
	if resolver != nil {
		refreshRemoteSources(ctx, resolver, []effectiveSubscription{ref})
	}
	return m.Get(id, baseURL)
}

func (m *Manager) RefreshAllSources(ctx context.Context) {
	m.mu.RLock()
	refs := m.effectiveSubscriptionsLocked()
	resolver := m.resolver
	m.mu.RUnlock()
	if resolver == nil {
		return
	}
	refreshRemoteSources(ctx, resolver, refs)
}

func refreshRemoteSources(ctx context.Context, resolver SourceResolver, refs []effectiveSubscription) {
	if resolver == nil {
		return
	}
	semaphore := make(chan struct{}, 4)
	var wait sync.WaitGroup
	for _, ref := range refs {
		for _, source := range sourcesFromConfig(ref.Config) {
			if !source.Enabled || source.Type != "remote" {
				continue
			}
			cacheKey := sourceCacheKey(ref.ID, source.ID)
			wait.Add(1)
			go func() {
				defer wait.Done()
				select {
				case semaphore <- struct{}{}:
					defer func() { <-semaphore }()
				case <-ctx.Done():
					return
				}
				resolver.Refresh(ctx, cacheKey, source)
			}()
		}
	}
	wait.Wait()
}

func (m *Manager) saveLocked() error {
	if m.store == nil {
		return nil
	}
	overrides := cloneOverrides(m.orphanOverrides)
	for id, entry := range m.overrides {
		overrides[id] = entry
	}
	return m.store.SaveFile(File{
		Version:         currentFileVersion,
		Subscriptions:   append([]Entry(nil), m.entries...),
		ConfigOverrides: overrides,
	})
}

func (m *Manager) staticFingerprintLocked(id string) string {
	for i, sub := range m.static {
		if staticSubscriptionID(i, sub) == id {
			return staticSubscriptionFingerprint(sub)
		}
	}
	return ""
}

func (m *Manager) clearOverrideConflictLocked(id string) {
	delete(m.orphanOverrides, id)
	conflicts := m.overrideConflicts[:0]
	for _, conflict := range m.overrideConflicts {
		if conflict.ID != id {
			conflicts = append(conflicts, conflict)
		}
	}
	m.overrideConflicts = conflicts
}

func (m *Manager) indexLocked(id string) int {
	for i, entry := range m.entries {
		if entry.ID == id {
			return i
		}
	}
	return -1
}

func (m *Manager) findEffectiveLocked(id string) (effectiveSubscription, bool) {
	for _, ref := range m.effectiveSubscriptionsLocked() {
		if ref.ID == id {
			return ref, true
		}
	}
	return effectiveSubscription{}, false
}

func (m *Manager) resolveConfigLocked(id string, sub config.SubscriptionConfig) config.SubscriptionConfig {
	return resolveConfigWithResolver(id, sub, m.resolver)
}

func resolveConfigWithResolver(id string, sub config.SubscriptionConfig, resolver SourceResolver) config.SubscriptionConfig {
	sub = cloneConfig(sub)
	sources := sourcesFromConfig(sub)
	if len(sources) == 0 {
		return sub
	}
	shares := make([]string, 0)
	for _, source := range sources {
		if !source.Enabled {
			continue
		}
		if source.Type == "share" {
			shares = append(shares, source.Share)
			continue
		}
		if source.Type == "remote" && resolver != nil {
			resolution := resolver.ResolveCached(sourceCacheKey(id, source.ID), source)
			shares = append(shares, resolution.Lines...)
		}
	}
	sub.Shares = shares
	return sub
}

func (m *Manager) listItemLocked(ref effectiveSubscription, baseURL string, includeSources bool) ListItem {
	sub := cloneConfig(ref.Config)
	sources := sourcesFromConfig(sub)
	item := ListItem{
		ID:          ref.ID,
		Source:      ref.Source,
		Editable:    ref.Source == "state" || m.store != nil,
		Overridden:  ref.Overridden,
		Name:        sub.Name,
		Enabled:     sub.Enabled,
		PublicToken: sub.PublicToken,
		HasKey:      strings.TrimSpace(sub.Key) != "",
		Format:      sub.Format,
		NodeIDs:     append([]string(nil), sub.NodeIDs...),
		Mode:        sub.Mode,
		Health:      "healthy",
		SourceCount: len(sources),
	}
	if sub.PublicToken != "" {
		item.URLTemplate = strings.TrimRight(baseURL, "/") + "/sub/" + sub.PublicToken + "?key=<key>"
	}
	if len(sources) == 0 {
		item.Shares = append([]string(nil), sub.Shares...)
		item.ShareCount = len(sub.Shares)
		item.DirectSourceCount = len(sub.Shares)
		item.SourceCount = len(sub.Shares)
		item.ResolvedShareCount = len(sub.Shares)
		item.OutputCount = len(sub.Shares)
		return item
	}

	if includeSources {
		item.Sources = make([]SourceListItem, 0, len(sources))
	}
	for _, source := range sources {
		status := SourceStatus{State: "healthy"}
		if source.Type == "share" {
			item.DirectSourceCount++
			if source.Enabled {
				status.ResolvedCount = 1
			}
		} else {
			item.RemoteSourceCount++
			status = SourceStatus{State: "never"}
			if m.resolver != nil {
				status = m.resolver.ResolveCached(sourceCacheKey(ref.ID, source.ID), source).Status
			}
			if status.LastSuccessAt != nil && (item.LastRefreshAt == nil || status.LastSuccessAt.After(*item.LastRefreshAt)) {
				value := *status.LastSuccessAt
				item.LastRefreshAt = &value
			}
			if source.Enabled {
				switch status.State {
				case "error":
					item.Health = "error"
				case "warning", "never":
					if item.Health != "error" {
						item.Health = "warning"
					}
				}
			}
		}
		if source.Enabled {
			item.ResolvedShareCount += status.ResolvedCount
		}
		if includeSources {
			item.Sources = append(item.Sources, SourceListItem{
				ID: source.ID, Name: source.Name, Type: source.Type, Enabled: source.Enabled,
				Share: source.Share, URL: source.URL, Status: status.State,
				LastSuccessAt: status.LastSuccessAt, ResolvedCount: status.ResolvedCount,
				Encoding: status.Encoding, Error: status.Error,
			})
		}
	}
	item.ShareCount = item.ResolvedShareCount
	item.OutputCount = item.ResolvedShareCount
	return item
}

func (e Entry) Config() config.SubscriptionConfig {
	return config.SubscriptionConfig{
		ID:          e.ID,
		Name:        e.Name,
		Enabled:     e.Enabled,
		PublicToken: e.PublicToken,
		Key:         e.Key,
		Shares:      append([]string(nil), e.Shares...),
		Sources:     cloneSources(e.Sources),
		Format:      e.Format,
		NodeIDs:     append([]string(nil), e.NodeIDs...),
		Mode:        e.Mode,
	}
}

func entryFromConfig(id string, sub config.SubscriptionConfig) Entry {
	return Entry{
		ID: id, Name: sub.Name, Enabled: sub.Enabled, PublicToken: sub.PublicToken,
		Key: sub.Key, Shares: append([]string(nil), sub.Shares...), Sources: cloneSources(sub.Sources),
		Format: sub.Format, NodeIDs: append([]string(nil), sub.NodeIDs...), Mode: sub.Mode,
	}
}

func entryFromRequest(id string, req UpsertRequest, current Entry) Entry {
	entry := Entry{
		ID: id, Name: req.Name, Enabled: req.Enabled, PublicToken: strings.TrimSpace(req.PublicToken),
		Key: strings.TrimSpace(req.Key), Shares: append([]string(nil), req.Shares...), Sources: cloneSources(req.Sources),
		Format: req.Format, NodeIDs: append([]string(nil), req.NodeIDs...), Mode: req.Mode,
		BaseFingerprint: current.BaseFingerprint,
	}
	if entry.PublicToken == "" {
		entry.PublicToken = current.PublicToken
	}
	if entry.Key == "" {
		entry.Key = current.Key
	}
	if req.Sources == nil && req.Shares == nil && (len(current.Sources) > 0 || len(current.Shares) > 0) {
		entry.Sources = cloneSources(current.Sources)
		entry.Shares = append([]string(nil), current.Shares...)
	}
	return entry
}

func normalizeEntry(entry *Entry, assignRandomSourceIDs bool) {
	entry.ID = strings.TrimSpace(entry.ID)
	entry.BaseFingerprint = strings.TrimSpace(entry.BaseFingerprint)
	entry.Name = strings.TrimSpace(entry.Name)
	entry.PublicToken = strings.TrimSpace(entry.PublicToken)
	entry.Key = strings.TrimSpace(entry.Key)
	entry.Format = strings.ToLower(strings.TrimSpace(entry.Format))
	if entry.Format == "" {
		entry.Format = defaultFormat
	}
	entry.Mode = normalizeMode(entry.Mode)
	entry.Shares = normalizeStrings(entry.Shares, false)
	entry.NodeIDs = normalizeStrings(entry.NodeIDs, true)
	entry.Sources = normalizeSources(entry.Sources, entry.Shares, entry.ID, assignRandomSourceIDs)
}

func normalizeConfig(sub *config.SubscriptionConfig, subscriptionID string, assignRandomSourceIDs bool) {
	sub.ID = strings.TrimSpace(sub.ID)
	sub.Name = strings.TrimSpace(sub.Name)
	sub.PublicToken = strings.TrimSpace(sub.PublicToken)
	sub.Key = strings.TrimSpace(sub.Key)
	sub.Format = strings.ToLower(strings.TrimSpace(sub.Format))
	if sub.Format == "" {
		sub.Format = defaultFormat
	}
	sub.Mode = normalizeMode(sub.Mode)
	sub.Shares = normalizeStrings(sub.Shares, false)
	sub.NodeIDs = normalizeStrings(sub.NodeIDs, true)
	sub.Sources = normalizeSources(sub.Sources, sub.Shares, subscriptionID, assignRandomSourceIDs)
}

func normalizeSources(values []config.SubscriptionSourceConfig, legacyShares []string, seed string, randomIDs bool) []config.SubscriptionSourceConfig {
	if len(values) == 0 && len(legacyShares) > 0 {
		values = make([]config.SubscriptionSourceConfig, 0, len(legacyShares))
		for i, share := range legacyShares {
			values = append(values, config.SubscriptionSourceConfig{
				ID: deterministicSourceID(seed, i, share), Name: fmt.Sprintf("分享 %d", i+1),
				Type: "share", Enabled: true, Share: share,
			})
		}
	}
	out := make([]config.SubscriptionSourceConfig, 0, len(values))
	for i, source := range values {
		source.ID = strings.TrimSpace(source.ID)
		source.Name = strings.TrimSpace(source.Name)
		source.Type = strings.ToLower(strings.TrimSpace(source.Type))
		source.Share = strings.TrimSpace(source.Share)
		source.URL = strings.TrimSpace(source.URL)
		if source.ID == "" {
			if randomIDs {
				source.ID = randomToken(9)
			} else {
				value := source.Share
				if source.Type == "remote" {
					value = source.URL
				}
				source.ID = deterministicSourceID(seed, i, value)
			}
		}
		if source.Name == "" {
			source.Name = fmt.Sprintf("来源 %d", i+1)
		}
		out = append(out, source)
	}
	return out
}

func normalizeMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return defaultMode
	}
	return mode
}

func normalizeStrings(values []string, lower bool) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if lower {
			value = strings.ToLower(value)
		}
		out = append(out, value)
	}
	return out
}

func validateEntry(entry Entry) error {
	if entry.ID == "" {
		return fmt.Errorf("%w: id must not be empty", ErrInvalidInput)
	}
	if entry.Format != defaultFormat {
		return fmt.Errorf("%w: format must be base64", ErrInvalidInput)
	}
	if entry.Mode != "rewrite" && entry.Mode != "merge" {
		return fmt.Errorf("%w: mode must be rewrite or merge", ErrInvalidInput)
	}
	seenSourceIDs := map[string]struct{}{}
	enabledSources := 0
	for i, source := range entry.Sources {
		if source.ID == "" {
			return fmt.Errorf("%w: sources[%d].id must not be empty", ErrInvalidInput, i)
		}
		if _, exists := seenSourceIDs[source.ID]; exists {
			return fmt.Errorf("%w: sources[%d].id must be unique", ErrInvalidInput, i)
		}
		seenSourceIDs[source.ID] = struct{}{}
		if source.Enabled {
			enabledSources++
		}
		switch source.Type {
		case "share":
			if source.Share == "" {
				return fmt.Errorf("%w: sources[%d].share must not be empty", ErrInvalidInput, i)
			}
			if strings.ContainsAny(source.Share, "\r\n") {
				return fmt.Errorf("%w: sources[%d].share must contain exactly one share", ErrInvalidInput, i)
			}
		case "remote":
			parsed, err := url.Parse(source.URL)
			if err != nil || parsed.Host == "" || (strings.ToLower(parsed.Scheme) != "http" && strings.ToLower(parsed.Scheme) != "https") {
				return fmt.Errorf("%w: sources[%d].url must use http or https", ErrInvalidInput, i)
			}
		default:
			return fmt.Errorf("%w: sources[%d].type must be share or remote", ErrInvalidInput, i)
		}
	}
	if entry.Enabled {
		if entry.PublicToken == "" {
			return fmt.Errorf("%w: public_token must not be empty when enabled", ErrInvalidInput)
		}
		if entry.Key == "" {
			return fmt.Errorf("%w: key must not be empty when enabled", ErrInvalidInput)
		}
		if (len(entry.Sources) > 0 && enabledSources == 0) || (len(entry.Sources) == 0 && len(entry.Shares) == 0) {
			return fmt.Errorf("%w: at least one enabled source is required when enabled", ErrInvalidInput)
		}
	}
	if entry.PublicToken != "" {
		if len(entry.PublicToken) < 16 {
			return fmt.Errorf("%w: public_token must be at least 16 characters", ErrInvalidInput)
		}
		if strings.Contains(entry.PublicToken, "/") {
			return fmt.Errorf("%w: public_token must be a single path segment", ErrInvalidInput)
		}
	}
	return nil
}

func (m *Manager) validateUniqueTokensLocked() error {
	seen := map[string]string{}
	for _, ref := range m.effectiveSubscriptionsLocked() {
		token := strings.TrimSpace(ref.Config.PublicToken)
		if token == "" {
			continue
		}
		if previous, ok := seen[token]; ok {
			return fmt.Errorf("%w: duplicate public_token between %s and %s", ErrInvalidInput, previous, ref.ID)
		}
		seen[token] = ref.ID
	}
	return nil
}

func staticSubscriptionID(index int, sub config.SubscriptionConfig) string {
	if id := strings.TrimSpace(sub.ID); id != "" {
		return "config:" + id
	}
	return fmt.Sprintf("config:%d", index)
}

func staticSubscriptionFingerprint(sub config.SubscriptionConfig) string {
	data, _ := json.Marshal(sub)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func deterministicSourceID(seed string, index int, value string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", seed, index, value)))
	return "src-" + hex.EncodeToString(sum[:6])
}

func draftSubscriptionCacheID(id string, sub config.SubscriptionConfig) string {
	parts := make([]string, 0, len(sub.Sources)+1)
	parts = append(parts, id)
	for _, source := range sourcesFromConfig(sub) {
		parts = append(parts, source.ID, source.Type, source.URL)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return id + "@draft-" + hex.EncodeToString(sum[:6])
}

func sourceCacheKey(subscriptionID string, sourceID string) string {
	return subscriptionID + "/" + sourceID
}

func sourcesFromConfig(sub config.SubscriptionConfig) []config.SubscriptionSourceConfig {
	return normalizeSources(sub.Sources, sub.Shares, sub.ID, false)
}

func cloneStatic(in []config.SubscriptionConfig) []config.SubscriptionConfig {
	out := append([]config.SubscriptionConfig(nil), in...)
	for i := range out {
		out[i] = cloneConfig(out[i])
	}
	return out
}

func cloneConfig(in config.SubscriptionConfig) config.SubscriptionConfig {
	in.Shares = append([]string(nil), in.Shares...)
	in.NodeIDs = append([]string(nil), in.NodeIDs...)
	in.Sources = cloneSources(in.Sources)
	return in
}

func cloneSources(in []config.SubscriptionSourceConfig) []config.SubscriptionSourceConfig {
	return append([]config.SubscriptionSourceConfig(nil), in...)
}

func cloneOverrides(in map[string]Entry) map[string]Entry {
	out := make(map[string]Entry, len(in))
	for id, entry := range in {
		entry.Shares = append([]string(nil), entry.Shares...)
		entry.NodeIDs = append([]string(nil), entry.NodeIDs...)
		entry.Sources = cloneSources(entry.Sources)
		out[id] = entry
	}
	return out
}

func randomToken(bytes int) string {
	if bytes < 16 {
		bytes = 16
	}
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
