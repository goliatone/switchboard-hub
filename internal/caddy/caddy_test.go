package caddy

import (
	"encoding/json"
	"testing"

	"github.com/goliatone/switchboard-hub/internal/config"
)

func TestAdminListenAddress(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{in: "", want: "127.0.0.1:2019"},
		{in: "http://127.0.0.1:2019", want: "127.0.0.1:2019"},
		{in: "127.0.0.1:2020", want: "127.0.0.1:2020"},
		{in: "https://localhost:4443/admin", want: "localhost:4443"},
	}

	for _, tc := range cases {
		got := adminListenAddress(tc.in)
		if got != tc.want {
			t.Fatalf("adminListenAddress(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildJSON_HTTPOnly(t *testing.T) {
	c := &config.Config{
		Caddy: config.Caddy{
			Admin:  "http://127.0.0.1:2019",
			Listen: []string{":80"},
			TLS: config.CaddyTLS{
				Enabled: false,
				Mode:    "internal",
				Listen:  []string{":443"},
			},
		},
		Routes: []config.Route{
			{Host: "my-local-app.test", Dial: "127.0.0.1:3030"},
		},
	}

	raw, err := BuildJSON(c)
	if err != nil {
		t.Fatalf("BuildJSON returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal generated json: %v", err)
	}

	apps := got["apps"].(map[string]any)
	if _, ok := apps["tls"]; ok {
		t.Fatal("did not expect apps.tls when TLS is disabled")
	}

	servers := apps["http"].(map[string]any)["servers"].(map[string]any)
	if _, ok := servers["switchboard_hub_http"]; !ok {
		t.Fatal("expected switchboard_hub_http server")
	}
	if _, ok := servers["switchboard_hub_https"]; ok {
		t.Fatal("did not expect switchboard_hub_https server when TLS is disabled")
	}
}

func TestBuildJSON_TLSInternalIssuer(t *testing.T) {
	c := &config.Config{
		Caddy: config.Caddy{
			Admin:  "http://127.0.0.1:2019",
			Listen: []string{":80"},
			TLS: config.CaddyTLS{
				Enabled:  true,
				Mode:     "internal",
				Listen:   []string{":443"},
				CertFile: "",
				KeyFile:  "",
			},
		},
		Routes: []config.Route{
			{Host: "my-local-app.test", Dial: "127.0.0.1:3030"},
			{Host: "api.my-local-app.test", Dial: "127.0.0.1:4040"},
		},
	}

	raw, err := BuildJSON(c)
	if err != nil {
		t.Fatalf("BuildJSON returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal generated json: %v", err)
	}

	apps := got["apps"].(map[string]any)
	servers := apps["http"].(map[string]any)["servers"].(map[string]any)
	if _, ok := servers["switchboard_hub_https"]; !ok {
		t.Fatal("expected switchboard_hub_https server when TLS is enabled")
	}

	tlsApp, ok := apps["tls"].(map[string]any)
	if !ok {
		t.Fatal("expected apps.tls block when TLS is enabled with routes")
	}

	automation := tlsApp["automation"].(map[string]any)
	policies := automation["policies"].([]any)
	if len(policies) != 1 {
		t.Fatalf("expected one tls automation policy, got %d", len(policies))
	}

	policy := policies[0].(map[string]any)
	issuers := policy["issuers"].([]any)
	if len(issuers) != 1 {
		t.Fatalf("expected one issuer, got %d", len(issuers))
	}
	issuer := issuers[0].(map[string]any)
	if issuer["module"] != "internal" {
		t.Fatalf("expected internal issuer, got %v", issuer["module"])
	}
}

func TestBuildJSON_TLSFileCertificates(t *testing.T) {
	c := &config.Config{
		Caddy: config.Caddy{
			Admin:  "http://127.0.0.1:2019",
			Listen: []string{":80"},
			TLS: config.CaddyTLS{
				Enabled:  true,
				Mode:     "file",
				Listen:   []string{":443"},
				CertFile: "/tmp/dev-cert.pem",
				KeyFile:  "/tmp/dev-key.pem",
			},
		},
		Routes: []config.Route{
			{Host: "my-local-app.test", Dial: "127.0.0.1:3030"},
		},
	}

	raw, err := BuildJSON(c)
	if err != nil {
		t.Fatalf("BuildJSON returned error: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal generated json: %v", err)
	}

	apps := got["apps"].(map[string]any)
	tlsApp := apps["tls"].(map[string]any)
	certs := tlsApp["certificates"].(map[string]any)
	loadFiles := certs["load_files"].([]any)
	if len(loadFiles) != 1 {
		t.Fatalf("expected one loaded certificate, got %d", len(loadFiles))
	}
	first := loadFiles[0].(map[string]any)
	if first["certificate"] != "/tmp/dev-cert.pem" {
		t.Fatalf("unexpected certificate path: %v", first["certificate"])
	}
	if first["key"] != "/tmp/dev-key.pem" {
		t.Fatalf("unexpected key path: %v", first["key"])
	}
}

func TestBuildJSON_TLSFileModeRequiresPaths(t *testing.T) {
	c := &config.Config{
		Caddy: config.Caddy{
			Admin:  "http://127.0.0.1:2019",
			Listen: []string{":80"},
			TLS: config.CaddyTLS{
				Enabled: true,
				Mode:    "file",
				Listen:  []string{":443"},
			},
		},
	}

	if _, err := BuildJSON(c); err == nil {
		t.Fatal("expected BuildJSON to fail when file-mode TLS paths are missing")
	}
}
