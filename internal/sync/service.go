package syncsvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	stdsync "sync"
	"time"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/dnspod"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/ping"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/provider"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/state"
)

var ErrUpdateInProgress = errors.New("update already in progress")

type Config struct {
	NodeIDs              []string
	ManagedPrefix        string
	ManagedBaseSubdomain string
	NodeLabels           map[string]string
	DefaultNodeID        string
	MaxRecordsPerNode    int
	Domain               string
	RecordLine           string
	TTL                  uint64
	Interval             time.Duration
	Fallback             FallbackConfig
}

type FallbackConfig struct {
	Enabled           bool
	WildcardSubdomain string
	Target            string
	Type              string
}

type Provider interface {
	Fetch(ctx context.Context, nodeIDs []string) (map[string][]provider.Candidate, error)
}

type Pinger interface {
	Check(ctx context.Context, candidates []provider.Candidate) []ping.Result
}

type StateStore interface {
	Load() (state.State, error)
	Save(state.State) error
}

type Summary struct {
	StartedAt time.Time      `json:"started_at"`
	EndedAt   time.Time      `json:"ended_at"`
	Success   bool           `json:"success"`
	Message   string         `json:"message,omitempty"`
	Created   int            `json:"created"`
	Updated   int            `json:"updated"`
	Deleted   int            `json:"deleted"`
	Kept      int            `json:"kept"`
	Records   []state.Record `json:"records"`
}

type Status struct {
	Running   bool        `json:"running"`
	State     state.State `json:"state"`
	NextRunAt *time.Time  `json:"next_run_at,omitempty"`
}

type Service struct {
	cfg      Config
	provider Provider
	pinger   Pinger
	dns      dnspod.Client
	store    StateStore
	logger   *slog.Logger

	updateMu stdsync.Mutex
	stateMu  stdsync.RWMutex
	state    state.State
	nextRun  *time.Time
	stopOnce stdsync.Once
	stopCh   chan struct{}
}

func NewService(cfg Config, provider Provider, pinger Pinger, dns dnspod.Client, store StateStore, initial state.State, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		cfg:      cfg,
		provider: provider,
		pinger:   pinger,
		dns:      dns,
		store:    store,
		logger:   logger,
		state:    initial,
		stopCh:   make(chan struct{}),
	}
}

func (s *Service) Start(ctx context.Context) {
	go s.loop(ctx)
}

func (s *Service) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
}

func (s *Service) Run(ctx context.Context) (Summary, error) {
	if !s.updateMu.TryLock() {
		return Summary{}, ErrUpdateInProgress
	}
	defer s.updateMu.Unlock()
	return s.runLocked(ctx)
}

func (s *Service) Status() Status {
	running := !s.updateMu.TryLock()
	if !running {
		s.updateMu.Unlock()
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return Status{
		Running:   running,
		State:     s.state,
		NextRunAt: s.nextRun,
	}
}

func (s *Service) Records() []state.Record {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	records := append([]state.Record(nil), s.state.Records...)
	return records
}

func (s *Service) loop(ctx context.Context) {
	scheduleNext := func() *time.Timer {
		next := time.Now().Add(s.cfg.Interval)
		s.stateMu.Lock()
		s.nextRun = &next
		s.state.NextRunAt = &next
		s.stateMu.Unlock()
		return time.NewTimer(s.cfg.Interval)
	}

	go func() {
		if _, err := s.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			s.logger.Warn("initial sync failed", "error", err)
		}
	}()

	timer := scheduleNext()
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-timer.C:
			if _, err := s.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.logger.Warn("scheduled sync failed", "error", err)
			}
			timer = scheduleNext()
		}
	}
}

func (s *Service) runLocked(ctx context.Context) (Summary, error) {
	started := time.Now().UTC()
	summary := Summary{StartedAt: started}
	records, changes, err := s.sync(ctx, started)
	summary.EndedAt = time.Now().UTC()
	if err != nil {
		summary.Success = false
		summary.Message = err.Error()
		s.saveFailure(summary, err)
		return summary, err
	}
	summary.Success = true
	summary.Message = "sync completed"
	summary.Records = records
	summary.Created = changes.Created
	summary.Updated = changes.Updated
	summary.Deleted = changes.Deleted
	summary.Kept = changes.Kept
	s.applySuccess(summary, records)
	return summary, nil
}

type changeCounts struct {
	Created int
	Updated int
	Deleted int
	Kept    int
}

func (s *Service) sync(ctx context.Context, now time.Time) ([]state.Record, changeCounts, error) {
	grouped, err := s.provider.Fetch(ctx, s.cfg.NodeIDs)
	if err != nil {
		return nil, changeCounts{}, fmt.Errorf("fetch provider IPs: %w", err)
	}

	all := make([]provider.Candidate, 0)
	for _, nodeID := range s.cfg.NodeIDs {
		all = append(all, grouped[nodeID]...)
	}
	checked := s.pinger.Check(ctx, all)
	selected := selectByNode(checked, s.cfg.MaxRecordsPerNode)
	desired := desiredRecords(selected, s.cfg, now)

	existing, err := s.dns.ListRecords(ctx)
	if err != nil {
		return nil, changeCounts{}, fmt.Errorf("list DNSPod records: %w", err)
	}
	plan := PlanChanges(existing, desired, s.cfg.ManagedPrefix, s.cfg.ManagedBaseSubdomain, s.managedExactNames(desired))
	changes := changeCounts{
		Created: len(plan.ToCreate),
		Updated: len(plan.ToModify),
		Deleted: len(plan.ToDelete),
		Kept:    len(plan.Kept),
	}

	for _, record := range plan.ToModify {
		if err := s.dns.ModifyRecord(ctx, record); err != nil {
			return nil, changes, fmt.Errorf("modify record %s: %w", record.Name, err)
		}
	}
	for i, record := range plan.ToCreate {
		id, err := s.dns.CreateRecord(ctx, record)
		if err != nil {
			return nil, changes, fmt.Errorf("create record %s: %w", record.Name, err)
		}
		plan.ToCreate[i].ID = id
		desired[record.Name] = withID(desired[record.Name], id)
	}
	for _, record := range plan.ToDelete {
		if err := s.dns.DeleteRecord(ctx, record.ID); err != nil {
			return nil, changes, fmt.Errorf("delete record %s: %w", record.Name, err)
		}
	}

	final := make([]state.Record, 0, len(desired))
	for _, record := range desired {
		final = append(final, record)
	}
	sort.Slice(final, func(i, j int) bool { return final[i].Name < final[j].Name })
	return final, changes, nil
}

func (s *Service) applySuccess(summary Summary, records []state.Record) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	st := s.state
	st.LastSync = &state.SyncSnapshot{
		StartedAt: summary.StartedAt,
		EndedAt:   summary.EndedAt,
		Success:   true,
		Message:   summary.Message,
		Created:   summary.Created,
		Updated:   summary.Updated,
		Deleted:   summary.Deleted,
		Kept:      summary.Kept,
	}
	st.Records = records
	st.LastError = ""
	st.UpdatedAt = time.Now().UTC()
	s.state = st
	if err := s.store.Save(st); err != nil {
		s.logger.Warn("save state failed", "error", err)
	}
}

func (s *Service) saveFailure(summary Summary, err error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	st := s.state
	st.LastSync = &state.SyncSnapshot{
		StartedAt: summary.StartedAt,
		EndedAt:   summary.EndedAt,
		Success:   false,
		Message:   summary.Message,
	}
	st.LastError = err.Error()
	st.UpdatedAt = time.Now().UTC()
	s.state = st
	if saveErr := s.store.Save(st); saveErr != nil {
		s.logger.Warn("save failure state failed", "error", saveErr)
	}
}

type ChangePlan struct {
	ToCreate []dnspod.Record
	ToModify []dnspod.Record
	ToDelete []dnspod.Record
	Kept     []dnspod.Record
}

func PlanChanges(existing []dnspod.Record, desired map[string]state.Record, prefix string, baseSubdomain string, exactNames []string) ChangePlan {
	managed := managedRecords(existing, prefix, baseSubdomain, exactNames)
	plan := ChangePlan{}
	for name, desiredRecord := range desired {
		record := dnspod.Record{
			Name:  name,
			Type:  desiredRecord.Type,
			Value: desiredRecord.Value,
		}
		current, ok := managed.primary[name]
		if !ok {
			plan.ToCreate = append(plan.ToCreate, record)
			continue
		}
		record.ID = current.ID
		desiredRecord.RecordID = current.ID
		desired[name] = desiredRecord
		if current.Type == record.Type && current.Value == record.Value {
			plan.Kept = append(plan.Kept, current)
			continue
		}
		plan.ToModify = append(plan.ToModify, record)
	}

	for name, record := range managed.primary {
		if _, ok := desired[name]; !ok {
			plan.ToDelete = append(plan.ToDelete, record)
		}
	}
	plan.ToDelete = append(plan.ToDelete, managed.duplicates...)
	sortRecords(plan.ToCreate)
	sortRecords(plan.ToModify)
	sortRecords(plan.ToDelete)
	sortRecords(plan.Kept)
	return plan
}

type managedSet struct {
	primary    map[string]dnspod.Record
	duplicates []dnspod.Record
}

func managedRecords(records []dnspod.Record, prefix string, baseSubdomain string, exactNames []string) managedSet {
	pattern := managedPattern(prefix, baseSubdomain)
	exact := map[string]struct{}{}
	for _, name := range exactNames {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			exact[name] = struct{}{}
		}
	}
	result := managedSet{primary: map[string]dnspod.Record{}}
	for _, record := range records {
		name := strings.ToLower(record.Name)
		_, isExact := exact[name]
		if !isExact && !pattern.MatchString(name) {
			continue
		}
		if _, exists := result.primary[name]; exists {
			result.duplicates = append(result.duplicates, record)
			continue
		}
		record.Name = name
		result.primary[name] = record
	}
	return result
}

func managedPattern(prefix string, baseSubdomain string) *regexp.Regexp {
	namePattern := regexp.QuoteMeta(strings.ToLower(strings.TrimSpace(prefix))) + `-[a-z0-9]+-[0-9]+`
	baseSubdomain = strings.Trim(strings.ToLower(strings.TrimSpace(baseSubdomain)), ".")
	if baseSubdomain != "" {
		namePattern += `\.` + regexp.QuoteMeta(baseSubdomain)
	}
	return regexp.MustCompile("^" + namePattern + "$")
}

func (s *Service) managedExactNames(desired map[string]state.Record) []string {
	names := make([]string, 0, 2)
	base := strings.Trim(strings.ToLower(strings.TrimSpace(s.cfg.ManagedBaseSubdomain)), ".")
	if base != "" {
		if _, ok := desired[base]; ok {
			names = append(names, base)
		}
	}
	if s.cfg.Fallback.Enabled && strings.TrimSpace(s.cfg.Fallback.WildcardSubdomain) != "" {
		names = append(names, s.cfg.Fallback.WildcardSubdomain)
	}
	return names
}

func selectByNode(results []ping.Result, limit int) map[string][]ping.Result {
	byNode := map[string][]ping.Result{}
	for _, result := range results {
		if !result.Alive {
			continue
		}
		nodeID := strings.ToLower(result.Candidate.NodeID)
		byNode[nodeID] = append(byNode[nodeID], result)
	}
	for nodeID := range byNode {
		sort.SliceStable(byNode[nodeID], func(i, j int) bool {
			return byNode[nodeID][i].Latency < byNode[nodeID][j].Latency
		})
		if len(byNode[nodeID]) > limit {
			byNode[nodeID] = byNode[nodeID][:limit]
		}
	}
	return byNode
}

func desiredRecords(selected map[string][]ping.Result, cfg Config, now time.Time) map[string]state.Record {
	desired := map[string]state.Record{}
	for nodeID, results := range selected {
		nodeLabel := labelForNode(nodeID, cfg.NodeLabels)
		for i, result := range results {
			name := joinSubdomain(fmt.Sprintf("%s-%s-%02d", cfg.ManagedPrefix, nodeLabel, i+1), cfg.ManagedBaseSubdomain)
			value := result.Candidate.IP
			desired[name] = state.Record{
				Name:      name,
				FQDN:      name + "." + cfg.Domain,
				Type:      dnspod.TypeForIP(value),
				Value:     value,
				NodeID:    nodeID,
				LatencyMS: result.Latency.Milliseconds(),
				UpdatedAt: now,
			}
		}
	}
	if fallbackDefault, ok := defaultRecord(selected, cfg, now); ok {
		desired[fallbackDefault.Name] = fallbackDefault
	}
	if cfg.Fallback.Enabled {
		fallbackType := strings.ToUpper(strings.TrimSpace(cfg.Fallback.Type))
		if fallbackType == "" {
			fallbackType = "CNAME"
		}
		name := strings.ToLower(strings.TrimSpace(cfg.Fallback.WildcardSubdomain))
		desired[name] = state.Record{
			Name:      name,
			FQDN:      name + "." + cfg.Domain,
			Type:      fallbackType,
			Value:     strings.TrimSpace(cfg.Fallback.Target),
			NodeID:    "fallback",
			UpdatedAt: now,
		}
	}
	return desired
}

func defaultRecord(selected map[string][]ping.Result, cfg Config, now time.Time) (state.Record, bool) {
	defaultNode := strings.ToLower(strings.TrimSpace(cfg.DefaultNodeID))
	results := selected[defaultNode]
	if len(results) == 0 || strings.TrimSpace(cfg.ManagedBaseSubdomain) == "" {
		return state.Record{}, false
	}
	best := results[0]
	for _, result := range results[1:] {
		if result.Latency < best.Latency {
			best = result
		}
	}
	value := best.Candidate.IP
	name := strings.ToLower(strings.TrimSpace(cfg.ManagedBaseSubdomain))
	return state.Record{
		Name:      name,
		FQDN:      name + "." + cfg.Domain,
		Type:      dnspod.TypeForIP(value),
		Value:     value,
		NodeID:    defaultNode,
		LatencyMS: best.Latency.Milliseconds(),
		UpdatedAt: now,
	}, true
}

func labelForNode(nodeID string, labels map[string]string) string {
	nodeID = strings.ToLower(strings.TrimSpace(nodeID))
	if labels == nil {
		return nodeID
	}
	if label := strings.ToLower(strings.TrimSpace(labels[nodeID])); label != "" {
		return label
	}
	return nodeID
}

func joinSubdomain(left string, right string) string {
	left = strings.Trim(strings.ToLower(strings.TrimSpace(left)), ".")
	right = strings.Trim(strings.ToLower(strings.TrimSpace(right)), ".")
	if right == "" {
		return left
	}
	if left == "" {
		return right
	}
	return left + "." + right
}

func withID(record state.Record, id uint64) state.Record {
	record.RecordID = id
	return record
}

func sortRecords(records []dnspod.Record) {
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })
}
