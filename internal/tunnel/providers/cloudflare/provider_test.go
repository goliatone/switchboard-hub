package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

type fakeProcess struct {
	pid     int
	killed  bool
	killErr error
}

func (p *fakeProcess) PID() int { return p.pid }
func (p *fakeProcess) Kill() error {
	p.killed = true
	return p.killErr
}

func TestInitFailsWhenBinaryMissing(t *testing.T) {
	p := New(
		WithLookPath(func(string) (string, error) { return "", errors.New("not found") }),
	)
	if err := p.Init(context.Background(), tunnel.ProviderConfig{}); err == nil {
		t.Fatal("expected binary missing error")
	}
}

func TestInitFailsWhenAuthCheckFails(t *testing.T) {
	tmp := t.TempDir()
	certPath := tmp + "/cert.pem"
	if err := os.WriteFile(certPath, []byte("dummy"), 0o644); err != nil {
		t.Fatalf("write temp cert: %v", err)
	}
	p := New(
		WithLookPath(func(string) (string, error) { return "/opt/homebrew/bin/cloudflared", nil }),
		WithCommandRunner(func(context.Context, string, ...string) (string, error) {
			return "", errors.New("permission denied")
		}),
	)
	if err := p.Init(context.Background(), tunnel.ProviderConfig{
		Values: map[string]string{"origincert": certPath},
	}); err == nil {
		t.Fatal("expected auth check failure")
	}
}

func TestInitFailsWhenOriginCertMissing(t *testing.T) {
	t.Setenv("TUNNEL_ORIGIN_CERT", "")
	p := New(
		WithLookPath(func(string) (string, error) { return "/opt/homebrew/bin/cloudflared", nil }),
		WithCommandRunner(func(context.Context, string, ...string) (string, error) {
			return "[]", nil
		}),
	)
	err := p.Init(context.Background(), tunnel.ProviderConfig{})
	if err == nil {
		t.Fatal("expected origin cert failure")
	}
	details, ok := tunnel.ActionableFromError(err)
	if !ok {
		t.Fatalf("expected actionable error, got: %v", err)
	}
	if details.Code != errCodeOriginCertMissing {
		t.Fatalf("unexpected error code: got=%q want=%q", details.Code, errCodeOriginCertMissing)
	}
}

func TestInitIncludesOriginCertInCloudflaredArgs(t *testing.T) {
	tmp := t.TempDir()
	certPath := tmp + "/cert.pem"
	if err := os.WriteFile(certPath, []byte("dummy"), 0o644); err != nil {
		t.Fatalf("write temp cert: %v", err)
	}
	seen := []string{}
	p := New(
		WithLookPath(func(string) (string, error) { return "/opt/homebrew/bin/cloudflared", nil }),
		WithCommandRunner(func(_ context.Context, _ string, args ...string) (string, error) {
			seen = append(seen, strings.Join(args, " "))
			return "[]", nil
		}),
	)
	if err := p.Init(context.Background(), tunnel.ProviderConfig{
		Values: map[string]string{"origincert": certPath},
	}); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if len(seen) == 0 {
		t.Fatal("expected at least one cloudflared command")
	}
	if !strings.Contains(seen[0], "--origincert "+certPath) {
		t.Fatalf("expected --origincert in command, got: %q", seen[0])
	}
}

func TestInitAPIModeFailsWhenTokenMissing(t *testing.T) {
	t.Setenv("SWITCHD_CF_API_TOKEN", "")
	p := New(
		WithLookPath(func(string) (string, error) { return "/opt/homebrew/bin/cloudflared", nil }),
	)
	err := p.Init(context.Background(), tunnel.ProviderConfig{
		Values: map[string]string{
			"mode":       "api",
			"account_id": "acct-1",
			"zone_id":    "zone-1",
		},
	})
	if err == nil {
		t.Fatal("expected token missing error")
	}
	details, ok := tunnel.ActionableFromError(err)
	if !ok {
		t.Fatalf("expected actionable error, got: %v", err)
	}
	if details.Code != errCodeAPITokenMissing {
		t.Fatalf("unexpected code: got=%q want=%q", details.Code, errCodeAPITokenMissing)
	}
}

func TestEnsureEndpointAPIModeCreatesTunnelAndDNS(t *testing.T) {
	t.Setenv("SWITCHD_CF_API_TOKEN", "token-1")
	type reqLog struct {
		method string
		path   string
	}
	logs := []reqLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logs = append(logs, reqLog{method: r.Method, path: r.URL.Path})
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel":
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/accounts/acct-1/cfd_tunnel":
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"id":"tun-1","name":"switchd-esign"}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tun-1/configurations":
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"ok":true}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/zones/zone-1/dns_records":
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/zones/zone-1/dns_records":
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"id":"dns-1"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1003,"message":"not found"}],"result":null}`))
		}
	}))
	defer srv.Close()

	p := New(
		WithLookPath(func(string) (string, error) { return "/opt/homebrew/bin/cloudflared", nil }),
	)
	cfg := tunnel.ProviderConfig{
		Values: map[string]string{
			"mode":         "api",
			"account_id":   "acct-1",
			"zone_id":      "zone-1",
			"api_base_url": srv.URL,
		},
	}
	if err := p.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	ep, err := p.EnsureEndpoint(context.Background(), tunnel.EndpointRequest{
		Name:       "esign",
		PublicHost: "esign.tnl.example.com",
		LocalURL:   "http://127.0.0.1:3000",
	})
	if err != nil {
		t.Fatalf("EnsureEndpoint returned error: %v", err)
	}
	if ep.ID != "tun-1" {
		t.Fatalf("unexpected tunnel id: %q", ep.ID)
	}
	if ep.Host != "esign.tnl.example.com" {
		t.Fatalf("unexpected host: %q", ep.Host)
	}
	if len(logs) < 4 {
		t.Fatalf("expected api calls, got=%d", len(logs))
	}
}

func TestStartAPIModeRunsCloudflaredWithToken(t *testing.T) {
	t.Setenv("SWITCHD_CF_API_TOKEN", "token-1")
	fp := &fakeProcess{pid: 5151}
	startArgs := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel":
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":[{"id":"tun-1","name":"switchd-esign"}]}`))
		case r.Method == http.MethodPut && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tun-1/configurations":
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"ok":true}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/accounts/acct-1/cfd_tunnel/tun-1/token":
			_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":"runtime-token"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1003,"message":"not found"}],"result":null}`))
		}
	}))
	defer srv.Close()

	p := New(
		WithLookPath(func(string) (string, error) { return "/opt/homebrew/bin/cloudflared", nil }),
		WithProcessStarter(func(_ string, args ...string) (process, error) {
			startArgs = append(startArgs, strings.Join(args, " "))
			return fp, nil
		}),
	)
	cfg := tunnel.ProviderConfig{
		Values: map[string]string{
			"mode":         "api",
			"account_id":   "acct-1",
			"zone_id":      "zone-1",
			"api_base_url": srv.URL,
		},
	}
	if err := p.Init(context.Background(), cfg); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	session, err := p.Start(context.Background(), tunnel.StartRequest{
		Endpoint: tunnel.Endpoint{
			ID:       "tun-1",
			Provider: "cloudflare",
			Host:     "esign.tnl.example.com",
		},
		LocalURL: "http://127.0.0.1:3000",
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if session.PID != 5151 {
		t.Fatalf("unexpected pid: %d", session.PID)
	}
	if len(startArgs) == 0 || !strings.Contains(startArgs[0], "run --token runtime-token") {
		t.Fatalf("expected tokenized run args, got=%v", startArgs)
	}
}

func TestEnsureEndpointCreatesTunnelAndRoute(t *testing.T) {
	calls := []string{}
	run := func(_ context.Context, _ string, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch {
		case strings.HasPrefix(strings.Join(args, " "), "tunnel list"):
			if len(calls) <= 2 {
				return "[]", nil
			}
			return `[{"id":"abc123","name":"switchd-esign"}]`, nil
		case strings.HasPrefix(strings.Join(args, " "), "tunnel create switchd-esign"):
			return "created", nil
		case strings.HasPrefix(strings.Join(args, " "), "tunnel route dns abc123 esign-oauth.dev.example.com"):
			return "ok", nil
		default:
			return "", fmt.Errorf("unexpected call: %v", args)
		}
	}

	p := New(
		WithLookPath(func(string) (string, error) { return "/opt/homebrew/bin/cloudflared", nil }),
		WithCommandRunner(run),
	)

	endpoint, err := p.EnsureEndpoint(context.Background(), tunnel.EndpointRequest{
		Name:       "esign",
		PublicHost: "esign-oauth.dev.example.com",
	})
	if err != nil {
		t.Fatalf("EnsureEndpoint returned error: %v", err)
	}
	if endpoint.ID != "abc123" {
		t.Fatalf("unexpected endpoint ID: %q", endpoint.ID)
	}
	if endpoint.Name != "switchd-esign" {
		t.Fatalf("unexpected endpoint name: %q", endpoint.Name)
	}
}

func TestStartStopAndStatus(t *testing.T) {
	fp := &fakeProcess{pid: 4242}
	run := func(_ context.Context, _ string, args ...string) (string, error) {
		cmd := strings.Join(args, " ")
		if strings.HasPrefix(cmd, "tunnel list") {
			return "[]", nil
		}
		if strings.HasPrefix(cmd, "tunnel info endpoint-1") {
			return "info", nil
		}
		return "", nil
	}

	p := New(
		WithLookPath(func(string) (string, error) { return "/opt/homebrew/bin/cloudflared", nil }),
		WithCommandRunner(run),
		WithProcessStarter(func(string, ...string) (process, error) { return fp, nil }),
		WithClock(func() time.Time { return time.Unix(1710000000, 0).UTC() }),
	)
	session, err := p.Start(context.Background(), tunnel.StartRequest{
		Endpoint: tunnel.Endpoint{ID: "endpoint-1"},
		LocalURL: "http://127.0.0.1:3000",
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	status, err := p.Status(context.Background(), "endpoint-1")
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if !status.Ready {
		t.Fatalf("expected ready status, got %#v", status)
	}

	if err := p.Stop(context.Background(), session.ID); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if !fp.killed {
		t.Fatal("expected process kill")
	}
}

func TestStopTreatsAlreadyFinishedProcessAsStopped(t *testing.T) {
	fp := &fakeProcess{pid: 4242, killErr: os.ErrProcessDone}
	p := New(
		WithProcessStarter(func(string, ...string) (process, error) { return fp, nil }),
	)
	session, err := p.Start(context.Background(), tunnel.StartRequest{
		Endpoint: tunnel.Endpoint{ID: "endpoint-1"},
		LocalURL: "http://127.0.0.1:3000",
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	if err := p.Stop(context.Background(), session.ID); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if !fp.killed {
		t.Fatal("expected process kill attempt")
	}
}

func TestStopReturnsUnexpectedKillError(t *testing.T) {
	fp := &fakeProcess{pid: 4242, killErr: errors.New("permission denied")}
	p := New(
		WithProcessStarter(func(string, ...string) (process, error) { return fp, nil }),
	)
	session, err := p.Start(context.Background(), tunnel.StartRequest{
		Endpoint: tunnel.Endpoint{ID: "endpoint-1"},
		LocalURL: "http://127.0.0.1:3000",
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	err = p.Stop(context.Background(), session.ID)
	if err == nil {
		t.Fatal("expected Stop error")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !fp.killed {
		t.Fatal("expected process kill attempt")
	}
}

func TestParseTunnelList(t *testing.T) {
	items, err := parseTunnelList(`[{"id":"id-1","name":"switchd-esign"}]`)
	if err != nil {
		t.Fatalf("parseTunnelList returned error: %v", err)
	}
	if len(items) != 1 || items[0].ID != "id-1" {
		t.Fatalf("unexpected parse result: %#v", items)
	}
}

func TestPIDFromSessionID(t *testing.T) {
	pid, err := pidFromSessionID("endpoint-1-4242")
	if err != nil {
		t.Fatalf("pidFromSessionID returned error: %v", err)
	}
	if pid != 4242 {
		t.Fatalf("unexpected pid: %d", pid)
	}
}
