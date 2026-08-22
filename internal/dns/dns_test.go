package dns

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedBlockRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dnsmasq.conf")
	if err := os.WriteFile(path, []byte("keep=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	block := buildManagedBlock("test", "127.0.0.2")
	if err := upsertManagedBlock(path, block); err != nil {
		t.Fatalf("upsertManagedBlock returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), managedBegin) || !strings.Contains(string(data), "address=/.test/127.0.0.1") {
		t.Fatalf("managed block missing from %q", data)
	}
	if err := removeManagedBlock(path); err != nil {
		t.Fatalf("removeManagedBlock returned error: %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), managedBegin) || !strings.Contains(string(data), "keep=true") {
		t.Fatalf("unexpected content after removal: %q", data)
	}
}

func TestManagedBlockHelpersReturnReadErrors(t *testing.T) {
	path := t.TempDir()
	if err := upsertManagedBlock(path, buildManagedBlock("test", "127.0.0.2")); err == nil {
		t.Fatal("upsertManagedBlock should return a directory read error")
	}
	if err := removeManagedBlock(path); err == nil {
		t.Fatal("removeManagedBlock should return a directory read error")
	}
}

func TestRemoveManagedBlockAllowsMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	if err := removeManagedBlock(path); err != nil {
		t.Fatalf("removeManagedBlock missing file: %v", err)
	}
}
