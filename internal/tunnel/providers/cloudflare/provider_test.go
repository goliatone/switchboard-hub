package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

type fakeProcess struct {
	pid    int
	killed bool
}

func (p *fakeProcess) PID() int { return p.pid }
func (p *fakeProcess) Kill() error {
	p.killed = true
	return nil
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
	p := New(
		WithLookPath(func(string) (string, error) { return "/opt/homebrew/bin/cloudflared", nil }),
		WithCommandRunner(func(context.Context, string, ...string) (string, error) {
			return "", errors.New("permission denied")
		}),
	)
	if err := p.Init(context.Background(), tunnel.ProviderConfig{}); err == nil {
		t.Fatal("expected auth check failure")
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
