package cluster

import (
	"testing"
)

func TestRoutingTableEncodeDecode(t *testing.T) {
	rt := &RoutingTable{
		Version: 42,
		Entries: []RoutingEntry{
			{ID: "sp-1", Addr: "http://sp-1:9000", Weight: 2, Healthy: true},
			{ID: "sp-2", Addr: "http://sp-2:9000", Weight: 1, Healthy: false},
		},
	}

	encoded, err := rt.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if encoded == "" {
		t.Fatal("encoded string should not be empty")
	}

	got, err := DecodeRoutingTable(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if got.Version != rt.Version {
		t.Errorf("Version = %d, want %d", got.Version, rt.Version)
	}
	if len(got.Entries) != len(rt.Entries) {
		t.Fatalf("Entries len = %d, want %d", len(got.Entries), len(rt.Entries))
	}
	for i, want := range rt.Entries {
		got := got.Entries[i]
		if got.ID != want.ID || got.Addr != want.Addr || got.Weight != want.Weight || got.Healthy != want.Healthy {
			t.Errorf("Entries[%d] = %+v, want %+v", i, got, want)
		}
	}
}

func TestDecodeInvalidBase64(t *testing.T) {
	_, err := DecodeRoutingTable("!!!not-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64")
	}
}

func TestDecodeInvalidJSON(t *testing.T) {
	import_ := "aW52YWxpZC1qc29u" // base64("invalid-json")
	_, err := DecodeRoutingTable(import_)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestSaveLoadRoutingTable(t *testing.T) {
	dir := t.TempDir()

	rt := &RoutingTable{
		Version: 99,
		Entries: []RoutingEntry{
			{ID: "node-a", Addr: "http://a:9000", Weight: 3, Healthy: true},
		},
	}

	if err := rt.SaveToDir(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := LoadFromDir(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil routing table")
	}
	if loaded.Version != 99 {
		t.Errorf("Version = %d, want 99", loaded.Version)
	}
	if len(loaded.Entries) != 1 || loaded.Entries[0].ID != "node-a" {
		t.Errorf("Entries = %+v, want [{node-a ...}]", loaded.Entries)
	}
}

func TestLoadNonExistentFile(t *testing.T) {
	rt, err := LoadFromDir(t.TempDir())
	if err != nil {
		t.Fatalf("Load non-existent: %v", err)
	}
	if rt != nil {
		t.Error("expected nil for non-existent cache")
	}
}
