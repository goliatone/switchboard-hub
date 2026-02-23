package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goliatone/switchboard-hub/internal/diag"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

const providerName = "tailscale"

type Provider struct {
	run      tunnel.CommandRunner
	lookPath func(file string) (string, error)
	now      func() time.Time

	mu       sync.Mutex
	sessions map[string]string // sessionID -> endpointID
}

type Option func(*Provider)

func New(opts ...Option) *Provider {
	p := &Provider{
		run:      runCommand,
		lookPath: exec.LookPath,
		now:      time.Now,
		sessions: map[string]string{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	return p
}

func WithCommandRunner(run tunnel.CommandRunner) Option {
	return func(p *Provider) {
		if run != nil {
			p.run = run
		}
	}
}

func WithLookPath(fn func(file string) (string, error)) Option {
	return func(p *Provider) {
		if fn != nil {
			p.lookPath = fn
		}
	}
}

func WithClock(now func() time.Time) Option {
	return func(p *Provider) {
		if now != nil {
			p.now = now
		}
	}
}

func (p *Provider) Name() string {
	return providerName
}

func (p *Provider) Capabilities() tunnel.Capabilities {
	return tunnel.Capabilities{
		StableHostname:     true,
		HTTPForwarding:     true,
		HTTPSForwarding:    true,
		OAuthSuitable:      true,
		SupportsSSE:        true,
		SupportsWebSockets: true,
		Notes: []string{
			"Tailscale Funnel behavior and availability may vary by plan and host.",
			"Prefer Cloudflare for public stable OAuth callbacks when ts.net hostname control is limited.",
		},
	}
}

func (p *Provider) Init(ctx context.Context, _ tunnel.ProviderConfig) error {
	if _, err := p.lookPath("tailscale"); err != nil {
		return errors.New("tailscale not found; install Tailscale CLI and login")
	}
	out, err := p.runTailscale(ctx, "status", "--json")
	if err != nil {
		return fmt.Errorf("tailscale status check failed: %w", err)
	}
	status := tailscaleStatus{}
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		return fmt.Errorf("decode tailscale status json: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(status.BackendState), "NeedsLogin") {
		return errors.New("tailscale is not logged in (run `tailscale up`)")
	}
	return nil
}

func (p *Provider) EnsureEndpoint(ctx context.Context, req tunnel.EndpointRequest) (tunnel.Endpoint, error) {
	if err := p.Init(ctx, tunnel.ProviderConfig{}); err != nil {
		return tunnel.Endpoint{}, err
	}
	endpointID := strings.TrimSpace(req.PublicHost)
	if endpointID == "" {
		out, err := p.runTailscale(ctx, "status", "--json")
		if err != nil {
			return tunnel.Endpoint{}, fmt.Errorf("tailscale status check failed: %w", err)
		}
		status := tailscaleStatus{}
		if err := json.Unmarshal([]byte(out), &status); err != nil {
			return tunnel.Endpoint{}, fmt.Errorf("decode tailscale status json: %w", err)
		}
		endpointID = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(status.Self.DNSName, ".")))
	}
	if endpointID == "" {
		return tunnel.Endpoint{}, errors.New("tailscale endpoint host is required")
	}
	if !strings.HasSuffix(strings.ToLower(endpointID), ".ts.net") {
		return tunnel.Endpoint{}, fmt.Errorf("tailscale endpoint host %q must end with .ts.net", endpointID)
	}

	return tunnel.Endpoint{
		ID:       endpointID,
		Provider: providerName,
		Name:     tunnelNameFrom(req.Name),
		Host:     endpointID,
		Metadata: req.Metadata,
	}, nil
}

func (p *Provider) Start(ctx context.Context, req tunnel.StartRequest) (tunnel.Session, error) {
	if err := p.Init(ctx, tunnel.ProviderConfig{}); err != nil {
		return tunnel.Session{}, err
	}
	if strings.TrimSpace(req.Endpoint.ID) == "" {
		return tunnel.Session{}, errors.New("endpoint id is required")
	}
	localPort, err := extractLocalPort(req.LocalURL)
	if err != nil {
		return tunnel.Session{}, err
	}

	if _, err := p.runTailscale(ctx, "funnel", "--bg", strconv.Itoa(localPort)); err != nil {
		return tunnel.Session{}, fmt.Errorf("enable tailscale funnel on port %d: %w", localPort, err)
	}
	startedAt := p.now().UTC()
	sessionID := fmt.Sprintf("%s-%d", req.Endpoint.ID, localPort)

	p.mu.Lock()
	p.sessions[sessionID] = req.Endpoint.ID
	p.mu.Unlock()

	return tunnel.Session{
		ID:         sessionID,
		Provider:   providerName,
		EndpointID: req.Endpoint.ID,
		PID:        0,
		StartedAt:  startedAt,
		Metadata: map[string]string{
			"local_port": strconv.Itoa(localPort),
		},
	}, nil
}

func (p *Provider) Stop(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session id is required")
	}

	if _, err := p.runTailscale(ctx, "funnel", "reset"); err != nil {
		return fmt.Errorf("disable tailscale funnel: %w", err)
	}

	p.mu.Lock()
	delete(p.sessions, sessionID)
	p.mu.Unlock()
	return nil
}

func (p *Provider) RemoveEndpoint(_ context.Context, endpointID string) error {
	endpointID = strings.TrimSpace(endpointID)
	if endpointID == "" {
		return errors.New("endpoint id is required")
	}
	return nil
}

func (p *Provider) Status(ctx context.Context, endpointID string) (tunnel.EndpointStatus, error) {
	endpointID = strings.TrimSpace(endpointID)
	if endpointID == "" {
		return tunnel.EndpointStatus{}, errors.New("endpoint id is required")
	}
	if _, err := p.runTailscale(ctx, "funnel", "status", "--json"); err != nil {
		return tunnel.EndpointStatus{}, fmt.Errorf("tailscale funnel status failed: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	for sessionID, id := range p.sessions {
		if id == endpointID {
			return tunnel.EndpointStatus{
				Ready: true,
				Endpoint: tunnel.Endpoint{
					ID:       endpointID,
					Provider: providerName,
				},
				SessionID: sessionID,
				Message:   "tailscale funnel session tracked in current process",
			}, nil
		}
	}
	return tunnel.EndpointStatus{
		Ready: false,
		Endpoint: tunnel.Endpoint{
			ID:       endpointID,
			Provider: providerName,
		},
		Message: "tailscale funnel reachable; no local session tracked in this process",
	}, nil
}

func (p *Provider) runTailscale(ctx context.Context, args ...string) (string, error) {
	return p.run(ctx, "tailscale", args...)
}

type tailscaleStatus struct {
	BackendState string `json:"BackendState"`
	Self         struct {
		DNSName string `json:"DNSName"`
	} `json:"Self"`
}

func extractLocalPort(localURL string) (int, error) {
	u, err := url.Parse(strings.TrimSpace(localURL))
	if err != nil {
		return 0, fmt.Errorf("invalid local url %q: %w", localURL, err)
	}
	portRaw := u.Port()
	if portRaw == "" {
		switch strings.ToLower(u.Scheme) {
		case "http":
			portRaw = "80"
		case "https":
			portRaw = "443"
		default:
			return 0, fmt.Errorf("local url %q must include explicit port for scheme %q", localURL, u.Scheme)
		}
	}
	p, err := strconv.Atoi(portRaw)
	if err != nil || p <= 0 || p > 65535 {
		return 0, fmt.Errorf("invalid local port in url %q", localURL)
	}
	return p, nil
}

func tunnelNameFrom(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	if idx := strings.IndexByte(s, '.'); idx > 0 {
		s = s[:idx]
	}
	if s == "" {
		return "switchd-app"
	}
	return "switchd-" + s
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	diag.LogCommand(name, args...)
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		if s == "" {
			return "", diag.SanitizeError(err)
		}
		return "", fmt.Errorf("%v: %s", diag.SanitizeError(err), diag.Redact(s))
	}
	return diag.Redact(s), nil
}

var _ tunnel.Provider = (*Provider)(nil)
