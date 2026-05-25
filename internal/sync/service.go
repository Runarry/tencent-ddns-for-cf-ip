package syncsvc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
	stdsync "sync"
	"time"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/dnspod"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/ping"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/provider"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/speedtest"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/state"
)

var (
	ErrUpdateInProgress        = errors.New("update already in progress")
	ErrTemporarySpeedTestEmpty = errors.New("no speed test candidates available")
	ErrTemporarySpeedTestURL   = errors.New("speed test URL must use https")
	ErrTemporarySpeedTestGone  = errors.New("temporary speed test not found")
)

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
	SpeedTest            SpeedTestConfig
	Fallback             FallbackConfig
}

type SpeedTestConfig struct {
	Enabled           bool
	CandidatesPerNode int
	DownloadBytes     int64
	Timeout           time.Duration
	Concurrency       int
	NewTester         func(url string) SpeedTester
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

type SpeedTester interface {
	Check(ctx context.Context, candidates []provider.Candidate) []speedtest.Result
}

type StateStore interface {
	Load() (state.State, error)
	Save(state.State) error
}

type selectedResult struct {
	Candidate        provider.Candidate
	Latency          time.Duration
	SpeedBPS         int64
	DownloadBytes    int64
	DownloadDuration time.Duration
	TTFB             time.Duration
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

type TemporarySpeedTest struct {
	ID        string                     `json:"id"`
	URL       string                     `json:"url"`
	StartedAt time.Time                  `json:"started_at"`
	EndedAt   time.Time                  `json:"ended_at"`
	Results   []TemporarySpeedTestResult `json:"results"`
}

type TemporarySpeedTestResult struct {
	NodeID        string `json:"nodeid"`
	Name          string `json:"name"`
	FQDN          string `json:"fqdn"`
	IP            string `json:"ip"`
	LatencyMS     int64  `json:"latency_ms"`
	SpeedBPS      int64  `json:"speed_bps"`
	DownloadBytes int64  `json:"download_bytes"`
	DownloadMS    int64  `json:"download_ms"`
	TTFBMS        int64  `json:"ttfb_ms"`
	Success       bool   `json:"success"`
	Error         string `json:"error,omitempty"`
}

type TemporarySpeedTestApplyResult struct {
	Applied bool           `json:"applied"`
	Records []state.Record `json:"records"`
}

type cachedTemporarySpeedTest struct {
	test    TemporarySpeedTest
	records []temporaryMeasuredRecord
}

type temporaryMeasuredRecord struct {
	record  state.Record
	success bool
	error   string
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
	speed    SpeedTester
	dns      dnspod.Client
	store    StateStore
	logger   *slog.Logger

	updateMu stdsync.Mutex
	stateMu  stdsync.RWMutex
	state    state.State
	nextRun  *time.Time
	stopOnce stdsync.Once
	stopCh   chan struct{}

	tempMu    stdsync.Mutex
	tempTests map[string]cachedTemporarySpeedTest
}

func NewService(cfg Config, provider Provider, pinger Pinger, speed SpeedTester, dns dnspod.Client, store StateStore, initial state.State, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		cfg:       cfg,
		provider:  provider,
		pinger:    pinger,
		speed:     speed,
		dns:       dns,
		store:     store,
		logger:    logger,
		state:     initial,
		stopCh:    make(chan struct{}),
		tempTests: map[string]cachedTemporarySpeedTest{},
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

func (s *Service) RunTemporarySpeedTest(ctx context.Context, rawURL string) (TemporarySpeedTest, error) {
	rawURL = strings.TrimSpace(rawURL)
	endpoint, err := url.Parse(rawURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" {
		return TemporarySpeedTest{}, ErrTemporarySpeedTestURL
	}

	s.stateMu.RLock()
	records := temporarySpeedTestRecords(s.state.Records)
	s.stateMu.RUnlock()
	if len(records) == 0 {
		return TemporarySpeedTest{}, ErrTemporarySpeedTestEmpty
	}

	started := time.Now().UTC()
	candidates := make([]provider.Candidate, 0, len(records))
	byCandidate := map[string]int{}
	for i, record := range records {
		candidate := provider.Candidate{NodeID: record.NodeID, IP: record.Value}
		candidates = append(candidates, candidate)
		byCandidate[resultKey(candidate)] = i
	}

	tester := s.speedTesterForURL(rawURL)
	speedResults := tester.Check(ctx, candidates)
	measured := make([]temporaryMeasuredRecord, len(records))
	for i, record := range records {
		measured[i] = temporaryMeasuredRecord{record: record}
	}
	for _, result := range speedResults {
		index, ok := byCandidate[resultKey(result.Candidate)]
		if !ok {
			continue
		}
		record := measured[index].record
		if result.Success {
			record.SpeedBPS = result.SpeedBPS
			record.DownloadBytes = result.DownloadBytes
			record.DownloadMS = result.DownloadDuration.Milliseconds()
			record.TTFBMS = result.TTFB.Milliseconds()
			record.UpdatedAt = time.Now().UTC()
			measured[index] = temporaryMeasuredRecord{record: record, success: true}
			continue
		}
		measured[index].error = result.Error
	}

	results := make([]TemporarySpeedTestResult, 0, len(measured))
	successes := 0
	for _, item := range measured {
		if item.success {
			successes++
		}
		results = append(results, temporarySpeedTestResult(item))
	}
	if successes == 0 {
		return TemporarySpeedTest{}, ErrTemporarySpeedTestEmpty
	}

	test := TemporarySpeedTest{
		ID:        randomTemporarySpeedTestID(),
		URL:       rawURL,
		StartedAt: started,
		EndedAt:   time.Now().UTC(),
		Results:   results,
	}
	s.tempMu.Lock()
	s.purgeTemporarySpeedTestsLocked(time.Now().UTC())
	s.tempTests[test.ID] = cachedTemporarySpeedTest{test: test, records: measured}
	s.tempMu.Unlock()
	return test, nil
}

func (s *Service) ApplyTemporarySpeedTest(ctx context.Context, id string) (TemporarySpeedTestApplyResult, error) {
	if !s.updateMu.TryLock() {
		return TemporarySpeedTestApplyResult{}, ErrUpdateInProgress
	}
	defer s.updateMu.Unlock()

	id = strings.TrimSpace(id)
	s.tempMu.Lock()
	s.purgeTemporarySpeedTestsLocked(time.Now().UTC())
	cached, ok := s.tempTests[id]
	if ok {
		delete(s.tempTests, id)
	}
	s.tempMu.Unlock()
	if !ok {
		return TemporarySpeedTestApplyResult{}, ErrTemporarySpeedTestGone
	}

	s.stateMu.RLock()
	current := append([]state.Record(nil), s.state.Records...)
	s.stateMu.RUnlock()
	records := s.applyTemporarySpeedTestRecords(current, cached.records, time.Now().UTC())

	desired := map[string]state.Record{}
	for _, record := range records {
		desired[record.Name] = record
	}
	existing, err := s.dns.ListRecords(ctx)
	if err != nil {
		return TemporarySpeedTestApplyResult{}, fmt.Errorf("list DNSPod records: %w", err)
	}
	plan := PlanChanges(existing, desired, s.cfg.ManagedPrefix, s.cfg.ManagedBaseSubdomain, s.managedExactNames(desired))
	for _, record := range plan.ToModify {
		if err := s.dns.ModifyRecord(ctx, record); err != nil {
			return TemporarySpeedTestApplyResult{}, fmt.Errorf("modify record %s: %w", record.Name, err)
		}
	}
	for i, record := range plan.ToCreate {
		record.Line = s.cfg.RecordLine
		record.TTL = s.cfg.TTL
		id, err := s.dns.CreateRecord(ctx, record)
		if err != nil {
			return TemporarySpeedTestApplyResult{}, fmt.Errorf("create record %s: %w", record.Name, err)
		}
		plan.ToCreate[i].ID = id
		if desiredRecord, ok := desired[record.Name]; ok {
			desiredRecord.RecordID = id
			desired[record.Name] = desiredRecord
		}
	}
	for _, record := range plan.ToDelete {
		if err := s.dns.DeleteRecord(ctx, record.ID); err != nil {
			return TemporarySpeedTestApplyResult{}, fmt.Errorf("delete record %s: %w", record.Name, err)
		}
	}
	records = make([]state.Record, 0, len(desired))
	for _, record := range desired {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Name < records[j].Name })

	s.stateMu.Lock()
	st := s.state
	st.Records = records
	st.LastError = ""
	st.UpdatedAt = time.Now().UTC()
	s.state = st
	s.stateMu.Unlock()
	if err := s.store.Save(st); err != nil {
		s.logger.Warn("save state failed", "error", err)
	}
	return TemporarySpeedTestApplyResult{Applied: true, Records: records}, nil
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
	selected := s.selectByNode(ctx, checked)
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

func (s *Service) selectByNode(ctx context.Context, results []ping.Result) map[string][]selectedResult {
	byNode := groupAliveByNode(results)
	if !s.cfg.SpeedTest.Enabled || s.speed == nil {
		return limitByPing(byNode, s.cfg.MaxRecordsPerNode)
	}

	candidatesPerNode := s.cfg.SpeedTest.CandidatesPerNode
	if candidatesPerNode <= 0 {
		candidatesPerNode = s.cfg.MaxRecordsPerNode * 3
	}
	if candidatesPerNode <= 0 {
		candidatesPerNode = 1
	}

	toMeasure := make([]provider.Candidate, 0)
	for _, results := range byNode {
		for _, result := range firstPingResults(results, candidatesPerNode) {
			toMeasure = append(toMeasure, result.Candidate)
		}
	}
	speedResults := s.speed.Check(ctx, toMeasure)
	byCandidate := map[string]speedtest.Result{}
	for _, result := range speedResults {
		if !result.Success {
			continue
		}
		byCandidate[resultKey(result.Candidate)] = result
	}

	selected := map[string][]selectedResult{}
	for nodeID, pingResults := range byNode {
		candidates := firstPingResults(pingResults, candidatesPerNode)
		measured := make([]selectedResult, 0, len(candidates))
		for _, pingResult := range candidates {
			speedResult, ok := byCandidate[resultKey(pingResult.Candidate)]
			if !ok {
				continue
			}
			measured = append(measured, selectedResult{
				Candidate:        pingResult.Candidate,
				Latency:          pingResult.Latency,
				SpeedBPS:         speedResult.SpeedBPS,
				DownloadBytes:    speedResult.DownloadBytes,
				DownloadDuration: speedResult.DownloadDuration,
				TTFB:             speedResult.TTFB,
			})
		}
		if len(measured) == 0 {
			selected[nodeID] = selectByPing(pingResults, s.cfg.MaxRecordsPerNode)
			continue
		}
		sort.SliceStable(measured, func(i, j int) bool {
			if measured[i].SpeedBPS != measured[j].SpeedBPS {
				return measured[i].SpeedBPS > measured[j].SpeedBPS
			}
			if measured[i].TTFB != measured[j].TTFB {
				return measured[i].TTFB < measured[j].TTFB
			}
			return measured[i].Latency < measured[j].Latency
		})
		if len(measured) > s.cfg.MaxRecordsPerNode {
			measured = measured[:s.cfg.MaxRecordsPerNode]
		}
		selected[nodeID] = measured
	}
	return selected
}

func groupAliveByNode(results []ping.Result) map[string][]ping.Result {
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
	}
	return byNode
}

func limitByPing(byNode map[string][]ping.Result, limit int) map[string][]selectedResult {
	selected := map[string][]selectedResult{}
	for nodeID, results := range byNode {
		selected[nodeID] = selectByPing(results, limit)
	}
	return selected
}

func selectByPing(results []ping.Result, limit int) []selectedResult {
	results = firstPingResults(results, limit)
	selected := make([]selectedResult, 0, len(results))
	for _, result := range results {
		selected = append(selected, selectedResult{
			Candidate: result.Candidate,
			Latency:   result.Latency,
		})
	}
	return selected
}

func firstPingResults(results []ping.Result, limit int) []ping.Result {
	if limit <= 0 {
		return nil
	}
	if len(results) > limit {
		return results[:limit]
	}
	return results
}

func resultKey(candidate provider.Candidate) string {
	return strings.ToLower(candidate.NodeID) + "|" + candidate.IP
}

func desiredRecords(selected map[string][]selectedResult, cfg Config, now time.Time) map[string]state.Record {
	desired := map[string]state.Record{}
	for nodeID, results := range selected {
		nodeLabel := labelForNode(nodeID, cfg.NodeLabels)
		for i, result := range results {
			name := joinSubdomain(fmt.Sprintf("%s-%s-%02d", cfg.ManagedPrefix, nodeLabel, i+1), cfg.ManagedBaseSubdomain)
			value := result.Candidate.IP
			desired[name] = state.Record{
				Name:          name,
				FQDN:          name + "." + cfg.Domain,
				Type:          dnspod.TypeForIP(value),
				Value:         value,
				NodeID:        nodeID,
				LatencyMS:     result.Latency.Milliseconds(),
				SpeedBPS:      result.SpeedBPS,
				DownloadBytes: result.DownloadBytes,
				DownloadMS:    result.DownloadDuration.Milliseconds(),
				TTFBMS:        result.TTFB.Milliseconds(),
				UpdatedAt:     now,
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

func defaultRecord(selected map[string][]selectedResult, cfg Config, now time.Time) (state.Record, bool) {
	defaultNode := strings.ToLower(strings.TrimSpace(cfg.DefaultNodeID))
	results := selected[defaultNode]
	if len(results) == 0 || strings.TrimSpace(cfg.ManagedBaseSubdomain) == "" {
		return state.Record{}, false
	}
	best := results[0]
	value := best.Candidate.IP
	name := strings.ToLower(strings.TrimSpace(cfg.ManagedBaseSubdomain))
	return state.Record{
		Name:          name,
		FQDN:          name + "." + cfg.Domain,
		Type:          dnspod.TypeForIP(value),
		Value:         value,
		NodeID:        defaultNode,
		LatencyMS:     best.Latency.Milliseconds(),
		SpeedBPS:      best.SpeedBPS,
		DownloadBytes: best.DownloadBytes,
		DownloadMS:    best.DownloadDuration.Milliseconds(),
		TTFBMS:        best.TTFB.Milliseconds(),
		UpdatedAt:     now,
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

func (s *Service) speedTesterForURL(rawURL string) SpeedTester {
	if s.cfg.SpeedTest.NewTester != nil {
		return s.cfg.SpeedTest.NewTester(rawURL)
	}
	return speedtest.NewTester(speedtest.Config{
		URL:           rawURL,
		DownloadBytes: s.cfg.SpeedTest.DownloadBytes,
		Timeout:       s.cfg.SpeedTest.Timeout,
		Concurrency:   s.cfg.SpeedTest.Concurrency,
	})
}

func temporarySpeedTestRecords(records []state.Record) []state.Record {
	out := make([]state.Record, 0, len(records))
	seen := map[string]struct{}{}
	for _, record := range records {
		if strings.EqualFold(record.NodeID, "fallback") || strings.HasPrefix(record.Name, "*.") || strings.HasPrefix(record.FQDN, "*.") {
			continue
		}
		if net.ParseIP(record.Value) == nil {
			continue
		}
		key := resultKey(provider.Candidate{NodeID: record.NodeID, IP: record.Value})
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, record)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].NodeID != out[j].NodeID {
			return out[i].NodeID < out[j].NodeID
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func temporarySpeedTestResult(item temporaryMeasuredRecord) TemporarySpeedTestResult {
	record := item.record
	return TemporarySpeedTestResult{
		NodeID:        record.NodeID,
		Name:          record.Name,
		FQDN:          record.FQDN,
		IP:            record.Value,
		LatencyMS:     record.LatencyMS,
		SpeedBPS:      record.SpeedBPS,
		DownloadBytes: record.DownloadBytes,
		DownloadMS:    record.DownloadMS,
		TTFBMS:        record.TTFBMS,
		Success:       item.success,
		Error:         item.error,
	}
}

func (s *Service) applyTemporarySpeedTestRecords(current []state.Record, measured []temporaryMeasuredRecord, now time.Time) []state.Record {
	measuredByNode := map[string][]temporaryMeasuredRecord{}
	for _, item := range measured {
		if !item.success {
			continue
		}
		nodeID := strings.ToLower(strings.TrimSpace(item.record.NodeID))
		measuredByNode[nodeID] = append(measuredByNode[nodeID], item)
	}
	for nodeID := range measuredByNode {
		sort.SliceStable(measuredByNode[nodeID], func(i, j int) bool {
			left := measuredByNode[nodeID][i].record
			right := measuredByNode[nodeID][j].record
			if left.SpeedBPS != right.SpeedBPS {
				return left.SpeedBPS > right.SpeedBPS
			}
			if left.TTFBMS != right.TTFBMS {
				return left.TTFBMS < right.TTFBMS
			}
			if left.LatencyMS != right.LatencyMS {
				return left.LatencyMS < right.LatencyMS
			}
			return left.Name < right.Name
		})
	}

	groups := map[string][]int{}
	slotPattern := managedPattern(s.cfg.ManagedPrefix, s.cfg.ManagedBaseSubdomain)
	for i, record := range current {
		if strings.EqualFold(record.NodeID, "fallback") || strings.HasPrefix(record.Name, "*.") || strings.HasPrefix(record.FQDN, "*.") {
			continue
		}
		if !slotPattern.MatchString(strings.ToLower(strings.TrimSpace(record.Name))) {
			continue
		}
		if net.ParseIP(record.Value) == nil {
			continue
		}
		nodeID := strings.ToLower(strings.TrimSpace(record.NodeID))
		if _, ok := measuredByNode[nodeID]; ok {
			groups[nodeID] = append(groups[nodeID], i)
		}
	}
	out := append([]state.Record(nil), current...)
	for nodeID, indexes := range groups {
		sort.SliceStable(indexes, func(i, j int) bool {
			return out[indexes[i]].Name < out[indexes[j]].Name
		})
		measuredRecords := measuredByNode[nodeID]
		for i, item := range measuredRecords {
			if i >= len(indexes) {
				break
			}
			slot := out[indexes[i]]
			measuredRecord := item.record
			slot.Type = measuredRecord.Type
			slot.Value = measuredRecord.Value
			slot.LatencyMS = measuredRecord.LatencyMS
			slot.SpeedBPS = measuredRecord.SpeedBPS
			slot.DownloadBytes = measuredRecord.DownloadBytes
			slot.DownloadMS = measuredRecord.DownloadMS
			slot.TTFBMS = measuredRecord.TTFBMS
			slot.UpdatedAt = now
			out[indexes[i]] = slot
		}
	}
	defaultNodeID := strings.ToLower(strings.TrimSpace(s.cfg.DefaultNodeID))
	defaultName := strings.ToLower(strings.Trim(strings.TrimSpace(s.cfg.ManagedBaseSubdomain), "."))
	if defaultNodeID != "" && defaultName != "" && len(measuredByNode[defaultNodeID]) > 0 {
		best := measuredByNode[defaultNodeID][0].record
		for i, record := range out {
			if strings.ToLower(strings.Trim(record.Name, ".")) != defaultName {
				continue
			}
			record.Type = best.Type
			record.Value = best.Value
			record.LatencyMS = best.LatencyMS
			record.SpeedBPS = best.SpeedBPS
			record.DownloadBytes = best.DownloadBytes
			record.DownloadMS = best.DownloadMS
			record.TTFBMS = best.TTFBMS
			record.UpdatedAt = now
			out[i] = record
			break
		}
	}
	return out
}

func (s *Service) purgeTemporarySpeedTestsLocked(now time.Time) {
	cutoff := now.Add(-30 * time.Minute)
	for id, cached := range s.tempTests {
		if cached.test.EndedAt.Before(cutoff) {
			delete(s.tempTests, id)
		}
	}
}

func randomTemporarySpeedTestID() string {
	var buf [18]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(buf[:])
}
