package syncsvc

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/sleep/tencent-ddns-for-cf-ip/internal/dnspod"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/ping"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/provider"
	"github.com/sleep/tencent-ddns-for-cf-ip/internal/state"
)

func TestPlanChangesOnlyTouchesManagedRecords(t *testing.T) {
	desired := map[string]state.Record{
		"cf-ctcc-01": {Name: "cf-ctcc-01", Type: "A", Value: "172.64.1.1"},
		"cf-ctcc-02": {Name: "cf-ctcc-02", Type: "A", Value: "172.64.1.2"},
	}
	existing := []dnspod.Record{
		{ID: 1, Name: "cf-ctcc-01", Type: "A", Value: "172.64.9.9"},
		{ID: 2, Name: "cf-ctcc-03", Type: "A", Value: "172.64.1.3"},
		{ID: 3, Name: "www", Type: "A", Value: "1.1.1.1"},
		{ID: 4, Name: "cf-custom", Type: "A", Value: "2.2.2.2"},
	}
	plan := PlanChanges(existing, desired, "cf", "", nil)
	if len(plan.ToModify) != 1 || plan.ToModify[0].ID != 1 {
		t.Fatalf("unexpected modify plan: %#v", plan.ToModify)
	}
	if len(plan.ToCreate) != 1 || plan.ToCreate[0].Name != "cf-ctcc-02" {
		t.Fatalf("unexpected create plan: %#v", plan.ToCreate)
	}
	if len(plan.ToDelete) != 1 || plan.ToDelete[0].ID != 2 {
		t.Fatalf("unexpected delete plan: %#v", plan.ToDelete)
	}
}

func TestServiceRunSyncsRecords(t *testing.T) {
	dns := &memoryDNS{
		records: []dnspod.Record{
			{ID: 10, Name: "cf-ctcc-01", Type: "A", Value: "172.64.9.9"},
			{ID: 11, Name: "cf-ctcc-02", Type: "A", Value: "172.64.9.8"},
		},
		nextID: 100,
	}
	store := &memoryStore{state: state.Empty()}
	service := NewService(Config{
		NodeIDs:           []string{"ctcc"},
		ManagedPrefix:     "cf",
		DefaultNodeID:     "ctcc",
		MaxRecordsPerNode: 1,
		Domain:            "example.com",
		RecordLine:        "默认",
		TTL:               600,
	}, providerStub{}, pingerStub{}, dns, store, state.Empty(), slog.Default())

	summary, err := service.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Updated != 1 || summary.Deleted != 1 || summary.Created != 0 {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if len(store.state.Records) != 1 || store.state.Records[0].Value != "172.64.1.1" {
		t.Fatalf("unexpected saved records: %#v", store.state.Records)
	}
	if dns.records[0].Value != "172.64.1.1" {
		t.Fatalf("record was not modified: %#v", dns.records)
	}
}

func TestDesiredRecordsIncludesBaseDefaultAndWildcardFallback(t *testing.T) {
	now := time.Now().UTC()
	selected := map[string][]ping.Result{
		"ctcc": {
			{Candidate: provider.Candidate{NodeID: "ctcc", IP: "172.64.1.1"}, Latency: 100 * time.Millisecond, Alive: true},
			{Candidate: provider.Candidate{NodeID: "ctcc", IP: "172.64.1.2"}, Latency: 120 * time.Millisecond, Alive: true},
		},
	}
	desired := desiredRecords(selected, Config{
		ManagedPrefix:        "cf",
		ManagedBaseSubdomain: "cdn.q",
		NodeLabels:           map[string]string{"ctcc": "cctcc"},
		DefaultNodeID:        "ctcc",
		Domain:               "example.com",
		Fallback: FallbackConfig{
			Enabled:           true,
			WildcardSubdomain: "*.cdn.q",
			Target:            "cdn.q.example.com",
			Type:              "CNAME",
		},
	}, now)

	if got := desired["cf-cctcc-01.cdn.q"]; got.Value != "172.64.1.1" || got.FQDN != "cf-cctcc-01.cdn.q.example.com" {
		t.Fatalf("unexpected labeled record: %#v", got)
	}
	if got := desired["cdn.q"]; got.Value != "172.64.1.1" || got.NodeID != "ctcc" {
		t.Fatalf("unexpected default record: %#v", got)
	}
	if got := desired["*.cdn.q"]; got.Type != "CNAME" || got.Value != "cdn.q.example.com" {
		t.Fatalf("unexpected fallback record: %#v", got)
	}
}

type providerStub struct{}

func (providerStub) Fetch(context.Context, []string) (map[string][]provider.Candidate, error) {
	return map[string][]provider.Candidate{
		"ctcc": {
			{NodeID: "ctcc", IP: "172.64.1.1"},
			{NodeID: "ctcc", IP: "172.64.1.2"},
		},
	}, nil
}

type pingerStub struct{}

func (pingerStub) Check(context.Context, []provider.Candidate) []ping.Result {
	return []ping.Result{
		{Candidate: provider.Candidate{NodeID: "ctcc", IP: "172.64.1.1"}, Latency: 100 * time.Millisecond, Alive: true},
		{Candidate: provider.Candidate{NodeID: "ctcc", IP: "172.64.1.2"}, Latency: 900 * time.Millisecond, Alive: false},
	}
}

type memoryDNS struct {
	records []dnspod.Record
	nextID  uint64
}

func (m *memoryDNS) ListRecords(context.Context) ([]dnspod.Record, error) {
	return append([]dnspod.Record(nil), m.records...), nil
}

func (m *memoryDNS) CreateRecord(_ context.Context, record dnspod.Record) (uint64, error) {
	m.nextID++
	record.ID = m.nextID
	m.records = append(m.records, record)
	return record.ID, nil
}

func (m *memoryDNS) ModifyRecord(_ context.Context, record dnspod.Record) error {
	for i := range m.records {
		if m.records[i].ID == record.ID {
			m.records[i] = record
			return nil
		}
	}
	return nil
}

func (m *memoryDNS) DeleteRecord(_ context.Context, id uint64) error {
	out := m.records[:0]
	for _, record := range m.records {
		if record.ID != id {
			out = append(out, record)
		}
	}
	m.records = out
	return nil
}

type memoryStore struct {
	state state.State
}

func (m *memoryStore) Load() (state.State, error) { return m.state, nil }
func (m *memoryStore) Save(st state.State) error {
	m.state = st
	return nil
}
