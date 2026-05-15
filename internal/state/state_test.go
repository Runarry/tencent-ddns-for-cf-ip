package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore(path)
	now := time.Now().UTC()
	input := State{
		Records: []Record{{
			RecordID:  10,
			Name:      "cf-ctcc-01",
			FQDN:      "cf-ctcc-01.example.com",
			Type:      "A",
			Value:     "172.64.1.1",
			NodeID:    "ctcc",
			LatencyMS: 100,
			UpdatedAt: now,
		}},
	}
	if err := store.Save(input); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 1 || got.Records[0].Name != "cf-ctcc-01" {
		t.Fatalf("unexpected state: %#v", got)
	}
}

func TestStoreRejectsCorruptJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path).Load(); err == nil {
		t.Fatal("expected corrupt json error")
	}
}
