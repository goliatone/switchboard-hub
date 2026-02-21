package tailscale

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

func TestInitFailsWhenBinaryMissing(t *testing.T) {
	p := New(
		WithLookPath(func(string) (string, error) { return "", errors.New("not found") }),
	)
	if err := p.Init(context.Background(), tunnel.ProviderConfig{}); err == nil {
		t.Fatal("expected binary missing error")
	}
}

func TestInitFailsWhenNeedsLogin(t *testing.T) {
	p := New(
		WithLookPath(func(string) (string, error) { return "/usr/local/bin/tailscale", nil }),
		WithCommandRunner(func(context.Context, string, ...string) (string, error) {
			return `{"BackendState":"NeedsLogin"}`, nil
		}),
	)
	if err := p.Init(context.Background(), tunnel.ProviderConfig{}); err == nil {
		t.Fatal("expected needs-login error")
	}
}

func TestEnsureEndpointFromPublicHost(t *testing.T) {
	p := New(
		WithLookPath(func(string) (string, error) { return "/usr/local/bin/tailscale", nil }),
		WithCommandRunner(func(_ context.Context, _ string, args ...string) (string, error) {
			if strings.HasPrefix(strings.Join(args, " "), "status --json") {
				return `{"BackendState":"Running","Self":{"DNSName":"machine.ts.net."}}`, nil
			}
			return "", nil
		}),
	)
	ep, err := p.EnsureEndpoint(context.Background(), tunnel.EndpointRequest{
		Name:       "esign",
		PublicHost: "my-mac.ts.net",
	})
	if err != nil {
		t.Fatalf("EnsureEndpoint returned error: %v", err)
	}
	if ep.ID != "my-mac.ts.net" {
		t.Fatalf("unexpected endpoint id: %q", ep.ID)
	}
}

func TestStartStopAndStatus(t *testing.T) {
	run := func(_ context.Context, _ string, args ...string) (string, error) {
		cmd := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(cmd, "status --json"):
			return `{"BackendState":"Running","Self":{"DNSName":"machine.ts.net."}}`, nil
		case strings.HasPrefix(cmd, "funnel --bg 3000"):
			return "ok", nil
		case strings.HasPrefix(cmd, "funnel status --json"):
			return `{"ok":true}`, nil
		case strings.HasPrefix(cmd, "funnel reset"):
			return "ok", nil
		default:
			return "", nil
		}
	}

	p := New(
		WithLookPath(func(string) (string, error) { return "/usr/local/bin/tailscale", nil }),
		WithCommandRunner(run),
		WithClock(func() time.Time { return time.Unix(1710000000, 0) }),
	)
	sess, err := p.Start(context.Background(), tunnel.StartRequest{
		Endpoint: tunnel.Endpoint{ID: "my-mac.ts.net"},
		LocalURL: "http://127.0.0.1:3000",
	})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	st, err := p.Status(context.Background(), "my-mac.ts.net")
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if !st.Ready {
		t.Fatalf("expected ready status, got %#v", st)
	}
	if err := p.Stop(context.Background(), sess.ID); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
}

func TestExtractLocalPort(t *testing.T) {
	p, err := extractLocalPort("http://127.0.0.1:3000")
	if err != nil {
		t.Fatalf("extractLocalPort returned error: %v", err)
	}
	if p != 3000 {
		t.Fatalf("unexpected port: %d", p)
	}
}
