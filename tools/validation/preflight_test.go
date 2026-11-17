package validation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func preflightFixture(t *testing.T) Config {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return Config{
		Provider: "cloudflare", RunDir: filepath.Join(dir, "fresh"), HelperPath: exe,
		ProbePath: exe, ProbeNetwork: "fixture", CredentialEnv: "T08_FIXTURE_API_TOKEN",
		HostA: "a.example.test", HostB: "b.example.test", PortA: 443, PortB: 443,
		DedicatedResources: true,
		Values: map[string]string{
			"ownership_mode": "single-writer", "authority_id": "fixture",
			"account_id": "test-account", "zone_id": "test-zone",
			"coordination_dir": filepath.Join(dir, "coord"), "supervisor_path": exe,
		},
	}
}

func TestLocalPreflightDoesNotNeedCredentialsOrCreateState(t *testing.T) {
	c := preflightFixture(t)
	t.Setenv(c.CredentialEnv, "")
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := c.checkLocalSetup(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{c.RunDir, c.Values["coordination_dir"]} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("preflight created state", err)
		}
	}
	if err := c.checkCredentialPresence(); err == nil {
		t.Fatal("live credential check accepted a missing credential")
	}
	t.Setenv(c.CredentialEnv, "dummy-token")
	if err := c.checkCredentialPresence(); err != nil {
		t.Fatal(err)
	}
}

func TestLocalPreflightRefusesIncompleteSetup(t *testing.T) {
	c := preflightFixture(t)
	if err := os.Mkdir(c.RunDir, 0o700); err != nil {
		t.Fatal(err)
	}
	coord := c.Values["coordination_dir"]
	if err := os.Mkdir(coord, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(coord, 0o755); err != nil {
		t.Fatal(err)
	}
	err := c.checkLocalSetup()
	for _, want := range []string{"fresh path", "existing coordination_dir"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("missing local setup error %q in %v", want, err)
		}
	}

	c.RunDir = filepath.Join(c.Values["coordination_dir"], "next")
	if err = c.checkLocalSetup(); err == nil || !strings.Contains(err.Error(), "parent directory") {
		t.Fatalf("accepted a public run parent: %v", err)
	}
	c.Values["supervisor_path"] = filepath.Join(t.TempDir(), "missing")
	if err = c.Validate(); err == nil || !strings.Contains(err.Error(), "supervisor_path") {
		t.Fatalf("accepted a missing connector: %v", err)
	}
}

func TestReadConfigRejectsTrailingDataAndUnsafeFiles(t *testing.T) {
	c := preflightFixture(t)
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "setup.json")
	for _, tc := range []struct {
		name, data string
		mode       os.FileMode
		valid      bool
	}{
		{"valid", string(raw) + "\n  ", 0o600, true},
		{"multiple values", string(raw) + "\n{}", 0o600, false},
		{"invalid trailing bytes", string(raw) + "canary", 0o600, false},
		{"above size limit", string(raw) + strings.Repeat(" ", maxConfigBytes), 0o600, false},
		{"unexpected field", strings.Replace(string(raw), "{", `{"canary":true,`, 1), 0o600, false},
		{"public mode", string(raw), 0o644, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			_, err := readConfig(path)
			if (err == nil) != tc.valid || err != nil && strings.Contains(err.Error(), "canary") {
				t.Fatalf("unexpected or unsanitized parse result: %v", err)
			}
		})
	}
}

func TestPreflightDNSAndCredentialNames(t *testing.T) {
	c := preflightFixture(t)
	for _, bad := range []string{"a.example.test.", "A.example.test", "127.0.0.1", "a.example.test/path", "a.example.test#route"} {
		c.HostA = bad
		if err := c.Validate(); err == nil {
			t.Errorf("accepted invalid fixture hostname %q", bad)
		}
	}
	c.HostA = "a.example.test"
	c.CredentialEnv = "literal secret"
	if err := c.Validate(); err == nil || strings.Contains(err.Error(), c.CredentialEnv) {
		t.Fatalf("expected sanitized credential reference error: %v", err)
	}
}
