package subscriptions

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/config"
)

const defaultFormat = "base64"

var (
	ErrNotFound     = errors.New("subscription not found")
	ErrNotEditable  = errors.New("subscription is not editable")
	ErrInvalidInput = errors.New("invalid subscription")
)

type Entry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name,omitempty"`
	Enabled     bool     `json:"enabled"`
	PublicToken string   `json:"public_token"`
	Key         string   `json:"key"`
	Shares      []string `json:"shares,omitempty"`
	Format      string   `json:"format"`
	NodeIDs     []string `json:"nodeids,omitempty"`
}

type File struct {
	Subscriptions []Entry `json:"subscriptions"`
}

type Store struct {
	path string
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Load() ([]Entry, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	entries := append([]Entry(nil), file.Subscriptions...)
	for i := range entries {
		normalizeEntry(&entries[i])
	}
	return entries, nil
}

func (s *Store) Save(entries []Entry) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	file := File{Subscriptions: append([]Entry(nil), entries...)}
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

type Manager struct {
	mu      sync.RWMutex
	static  []config.SubscriptionConfig
	store   *Store
	entries []Entry
}

func NewManager(static []config.SubscriptionConfig, store *Store) (*Manager, error) {
	var entries []Entry
	if store != nil {
		loaded, err := store.Load()
		if err != nil {
			return nil, err
		}
		entries = loaded
	}
	static = cloneStatic(static)
	for i := range static {
		normalizeConfig(&static[i])
	}
	for i := range entries {
		normalizeEntry(&entries[i])
		if err := validateEntry(entries[i]); err != nil {
			return nil, fmt.Errorf("subscriptions[%d]: %w", i, err)
		}
	}
	if err := validateUniqueTokens(static, entries); err != nil {
		return nil, err
	}
	return &Manager{static: static, store: store, entries: entries}, nil
}

func (m *Manager) PublicSubscriptions() []config.SubscriptionConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.publicSubscriptionsLocked()
}

func (m *Manager) publicSubscriptionsLocked() []config.SubscriptionConfig {
	result := cloneStatic(m.static)
	for _, entry := range m.entries {
		result = append(result, entry.Config())
	}
	return result
}

type ListItem struct {
	ID          string   `json:"id"`
	Source      string   `json:"source"`
	Editable    bool     `json:"editable"`
	Name        string   `json:"name,omitempty"`
	Enabled     bool     `json:"enabled"`
	PublicToken string   `json:"public_token,omitempty"`
	HasKey      bool     `json:"has_key"`
	Format      string   `json:"format"`
	NodeIDs     []string `json:"nodeids,omitempty"`
	Shares      []string `json:"shares,omitempty"`
	ShareCount  int      `json:"share_count"`
	URLTemplate string   `json:"url_template,omitempty"`
}

func (m *Manager) List(baseURL string) []ListItem {
	m.mu.RLock()
	defer m.mu.RUnlock()
	items := make([]ListItem, 0, len(m.static)+len(m.entries))
	for i, sub := range m.static {
		items = append(items, listItemFromConfig(fmt.Sprintf("config:%d", i), "config", false, sub, baseURL))
	}
	for _, entry := range m.entries {
		items = append(items, listItemFromConfig(entry.ID, "state", true, entry.Config(), baseURL))
	}
	return items
}

type UpsertRequest struct {
	Name        string   `json:"name"`
	Enabled     bool     `json:"enabled"`
	PublicToken string   `json:"public_token"`
	Key         string   `json:"key"`
	Shares      []string `json:"shares"`
	Format      string   `json:"format"`
	NodeIDs     []string `json:"nodeids"`
}

type MutationResult struct {
	Item ListItem `json:"item"`
	Key  string   `json:"key,omitempty"`
}

func (m *Manager) Create(req UpsertRequest, baseURL string) (MutationResult, error) {
	entry := Entry{
		ID:          randomToken(18),
		Name:        req.Name,
		Enabled:     req.Enabled,
		PublicToken: req.PublicToken,
		Key:         req.Key,
		Shares:      req.Shares,
		Format:      req.Format,
		NodeIDs:     req.NodeIDs,
	}
	if strings.TrimSpace(entry.PublicToken) == "" {
		entry.PublicToken = randomToken(24)
	}
	if strings.TrimSpace(entry.Key) == "" {
		entry.Key = randomToken(32)
	}
	normalizeEntry(&entry)
	if err := validateEntry(entry); err != nil {
		return MutationResult{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if tokenExistsLocked(m.static, m.entries, entry.PublicToken, "") {
		return MutationResult{}, fmt.Errorf("%w: public_token must be unique", ErrInvalidInput)
	}
	m.entries = append(m.entries, entry)
	if err := m.saveLocked(); err != nil {
		m.entries = m.entries[:len(m.entries)-1]
		return MutationResult{}, err
	}
	return MutationResult{Item: listItemFromConfig(entry.ID, "state", true, entry.Config(), baseURL), Key: entry.Key}, nil
}

func (m *Manager) Update(id string, req UpsertRequest, baseURL string) (ListItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.HasPrefix(id, "config:") {
		return ListItem{}, ErrNotEditable
	}
	index := m.indexLocked(id)
	if index < 0 {
		return ListItem{}, ErrNotFound
	}
	updated := Entry{
		ID:          m.entries[index].ID,
		Name:        req.Name,
		Enabled:     req.Enabled,
		PublicToken: req.PublicToken,
		Key:         req.Key,
		Shares:      req.Shares,
		Format:      req.Format,
		NodeIDs:     req.NodeIDs,
	}
	if strings.TrimSpace(updated.PublicToken) == "" {
		updated.PublicToken = m.entries[index].PublicToken
	}
	if strings.TrimSpace(updated.Key) == "" {
		updated.Key = m.entries[index].Key
	}
	normalizeEntry(&updated)
	if err := validateEntry(updated); err != nil {
		return ListItem{}, err
	}
	if tokenExistsLocked(m.static, m.entries, updated.PublicToken, updated.ID) {
		return ListItem{}, fmt.Errorf("%w: public_token must be unique", ErrInvalidInput)
	}
	previous := m.entries[index]
	m.entries[index] = updated
	if err := m.saveLocked(); err != nil {
		m.entries[index] = previous
		return ListItem{}, err
	}
	return listItemFromConfig(updated.ID, "state", true, updated.Config(), baseURL), nil
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
	if strings.HasPrefix(id, "config:") {
		return MutationResult{}, ErrNotEditable
	}
	index := m.indexLocked(id)
	if index < 0 {
		return MutationResult{}, ErrNotFound
	}
	previous := m.entries[index]
	updated := previous
	if target == "key" || target == "both" {
		updated.Key = randomToken(32)
	}
	if target == "public_token" || target == "both" {
		for {
			updated.PublicToken = randomToken(24)
			if !tokenExistsLocked(m.static, m.entries, updated.PublicToken, updated.ID) {
				break
			}
		}
	}
	m.entries[index] = updated
	if err := m.saveLocked(); err != nil {
		m.entries[index] = previous
		return MutationResult{}, err
	}
	result := MutationResult{Item: listItemFromConfig(updated.ID, "state", true, updated.Config(), baseURL)}
	if target == "key" || target == "both" {
		result.Key = updated.Key
	}
	return result, nil
}

func (m *Manager) ConfigForToken(token string) (config.SubscriptionConfig, bool) {
	token = strings.TrimSpace(token)
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, sub := range m.publicSubscriptionsLocked() {
		if sub.Enabled && sub.PublicToken == token {
			return sub, true
		}
	}
	return config.SubscriptionConfig{}, false
}

func (m *Manager) saveLocked() error {
	if m.store == nil {
		return nil
	}
	return m.store.Save(m.entries)
}

func (m *Manager) indexLocked(id string) int {
	for i, entry := range m.entries {
		if entry.ID == id {
			return i
		}
	}
	return -1
}

func (e Entry) Config() config.SubscriptionConfig {
	return config.SubscriptionConfig{
		Name:        e.Name,
		Enabled:     e.Enabled,
		PublicToken: e.PublicToken,
		Key:         e.Key,
		Shares:      append([]string(nil), e.Shares...),
		Format:      e.Format,
		NodeIDs:     append([]string(nil), e.NodeIDs...),
	}
}

func listItemFromConfig(id string, source string, editable bool, sub config.SubscriptionConfig, baseURL string) ListItem {
	normalizeConfig(&sub)
	item := ListItem{
		ID:          id,
		Source:      source,
		Editable:    editable,
		Name:        sub.Name,
		Enabled:     sub.Enabled,
		PublicToken: sub.PublicToken,
		HasKey:      strings.TrimSpace(sub.Key) != "",
		Format:      sub.Format,
		NodeIDs:     append([]string(nil), sub.NodeIDs...),
		Shares:      append([]string(nil), sub.Shares...),
		ShareCount:  len(sub.Shares),
	}
	if sub.PublicToken != "" {
		item.URLTemplate = strings.TrimRight(baseURL, "/") + "/sub/" + sub.PublicToken + "?key=<key>"
	}
	return item
}

func normalizeEntry(entry *Entry) {
	entry.ID = strings.TrimSpace(entry.ID)
	entry.Name = strings.TrimSpace(entry.Name)
	entry.PublicToken = strings.TrimSpace(entry.PublicToken)
	entry.Key = strings.TrimSpace(entry.Key)
	entry.Format = strings.ToLower(strings.TrimSpace(entry.Format))
	if entry.Format == "" {
		entry.Format = defaultFormat
	}
	entry.Shares = normalizeStrings(entry.Shares, false)
	entry.NodeIDs = normalizeStrings(entry.NodeIDs, true)
}

func normalizeConfig(sub *config.SubscriptionConfig) {
	sub.Name = strings.TrimSpace(sub.Name)
	sub.PublicToken = strings.TrimSpace(sub.PublicToken)
	sub.Key = strings.TrimSpace(sub.Key)
	sub.Format = strings.ToLower(strings.TrimSpace(sub.Format))
	if sub.Format == "" {
		sub.Format = defaultFormat
	}
	sub.Shares = normalizeStrings(sub.Shares, false)
	sub.NodeIDs = normalizeStrings(sub.NodeIDs, true)
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
	if entry.Enabled {
		if entry.PublicToken == "" {
			return fmt.Errorf("%w: public_token must not be empty when enabled", ErrInvalidInput)
		}
		if entry.Key == "" {
			return fmt.Errorf("%w: key must not be empty when enabled", ErrInvalidInput)
		}
		if len(entry.Shares) == 0 {
			return fmt.Errorf("%w: shares must not be empty when enabled", ErrInvalidInput)
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

func validateUniqueTokens(static []config.SubscriptionConfig, entries []Entry) error {
	seen := map[string]string{}
	for i, sub := range static {
		token := strings.TrimSpace(sub.PublicToken)
		if token == "" {
			continue
		}
		if previous, ok := seen[token]; ok {
			return fmt.Errorf("%w: duplicate public_token between %s and config:%d", ErrInvalidInput, previous, i)
		}
		seen[token] = fmt.Sprintf("config:%d", i)
	}
	for _, entry := range entries {
		token := strings.TrimSpace(entry.PublicToken)
		if token == "" {
			continue
		}
		if previous, ok := seen[token]; ok {
			return fmt.Errorf("%w: duplicate public_token between %s and %s", ErrInvalidInput, previous, entry.ID)
		}
		seen[token] = entry.ID
	}
	return nil
}

func tokenExistsLocked(static []config.SubscriptionConfig, entries []Entry, token string, ignoreID string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	for _, sub := range static {
		if sub.PublicToken == token {
			return true
		}
	}
	for _, entry := range entries {
		if entry.ID != ignoreID && entry.PublicToken == token {
			return true
		}
	}
	return false
}

func cloneStatic(in []config.SubscriptionConfig) []config.SubscriptionConfig {
	out := append([]config.SubscriptionConfig(nil), in...)
	for i := range out {
		out[i].Shares = append([]string(nil), out[i].Shares...)
		out[i].NodeIDs = append([]string(nil), out[i].NodeIDs...)
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
