package validation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidationRequiresExplicitDedicatedSetup(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := Config{Provider: "cloudflare", RunDir: filepath.Join(t.TempDir(), "fresh"), HelperPath: exe, ProbePath: exe, ProbeNetwork: "external-observer", CredentialEnv: "DUMMY_CF_TOKEN", HostA: "a.example.test", HostB: "b.example.test", PortA: 443, PortB: 443, DedicatedResources: true, Values: map[string]string{"ownership_mode": "single-writer", "authority_id": "test", "account_id": "account", "zone_id": "zone", "coordination_dir": t.TempDir(), "supervisor_path": exe}}
	if err = c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.DedicatedResources = false
	if c.Validate() == nil {
		t.Fatal("accepted unowned resources")
	}
	c.DedicatedResources = true
	c.Values["api_token"] = "dummy"
	if c.Validate() == nil {
		t.Fatal("accepted credential value")
	}
	delete(c.Values, "api_token")
	c.HostB = c.HostA
	if c.Validate() == nil {
		t.Fatal("accepted host collision")
	}
}

func TestValidationSelectsExplicitTailscaleTransport(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := Config{Provider: "tailscale", RunDir: filepath.Join(t.TempDir(), "run"), HelperPath: exe, ProbePath: exe, ProbeNetwork: "external", HostA: "host.ts.net", HostB: "host.ts.net", PortA: 8443, PortB: 10000, DedicatedResources: true, Values: map[string]string{"coordination_dir": t.TempDir(), "local_api_transport": "macos-gui"}}
	if err = c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Values["local_api_socket"] = "/tmp/another-daemon.sock"
	if c.Validate() == nil {
		t.Fatal("accepted ambiguous GUI and Unix selection")
	}
	c.Values["local_api_transport"] = "unix"
	if err = c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Values["local_api_transport"] = "auto"
	if c.Validate() == nil {
		t.Fatal("accepted implicit transport selection")
	}
}
