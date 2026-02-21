package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

const (
	providerName = "cloudflare"
)

type process interface {
	PID() int
	Kill() error
}

type processStarter func(name string, args ...string) (process, error)

type Provider struct {
	run      tunnel.CommandRunner
	lookPath func(file string) (string, error)
	start    processStarter
	now      func() time.Time

	mu       sync.Mutex
	sessions map[string]sessionState
}

type sessionState struct {
	endpointID string
	process    process
	startedAt  time.Time
}

type Option func(*Provider)

func New(opts ...Option) *Provider {
	p := &Provider{
		run:      runCommand,
		lookPath: exec.LookPath,
		start:    startCommand,
		now:      time.Now,
		sessions: map[string]sessionState{},
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

func WithProcessStarter(start processStarter) Option {
	return func(p *Provider) {
		if start != nil {
			p.start = start
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
		SupportsSSE:        false,
		SupportsWebSockets: true,
		Notes: []string{
			"Cloudflare named tunnels require cloudflared login and DNS zone access.",
			"Cloudflare free quick tunnels are not suitable for stable OAuth callbacks.",
		},
	}
}

func (p *Provider) Init(ctx context.Context, _ tunnel.ProviderConfig) error {
	if _, err := p.lookPath("cloudflared"); err != nil {
		return errors.New("cloudflared not found; install with `brew install cloudflared`")
	}
	if _, err := p.runCloudflared(ctx, "tunnel", "list", "--output", "json"); err != nil {
		return fmt.Errorf("cloudflare auth check failed (run `cloudflared tunnel login`): %w", err)
	}
	return nil
}

func (p *Provider) EnsureEndpoint(ctx context.Context, req tunnel.EndpointRequest) (tunnel.Endpoint, error) {
	if err := p.Init(ctx, tunnel.ProviderConfig{}); err != nil {
		return tunnel.Endpoint{}, err
	}
	if strings.TrimSpace(req.Name) == "" {
		return tunnel.Endpoint{}, errors.New("endpoint name is required")
	}
	publicHost := strings.ToLower(strings.TrimSpace(req.PublicHost))
	if publicHost == "" {
		return tunnel.Endpoint{}, errors.New("public host is required")
	}
	if err := p.Capabilities().ValidateOAuthUse(publicHost); err != nil {
		return tunnel.Endpoint{}, err
	}

	tunnelName := tunnelNameFrom(req.Name)
	tunnelID, err := p.findTunnelIDByName(ctx, tunnelName)
	if err != nil {
		return tunnel.Endpoint{}, err
	}
	if tunnelID == "" {
		if _, err := p.runCloudflared(ctx, "tunnel", "create", tunnelName); err != nil {
			return tunnel.Endpoint{}, fmt.Errorf("create cloudflare tunnel %q: %w", tunnelName, err)
		}
		tunnelID, err = p.findTunnelIDByName(ctx, tunnelName)
		if err != nil {
			return tunnel.Endpoint{}, err
		}
		if tunnelID == "" {
			return tunnel.Endpoint{}, fmt.Errorf("created tunnel %q but failed to resolve its ID", tunnelName)
		}
	}

	if _, err := p.runCloudflared(ctx, "tunnel", "route", "dns", tunnelID, publicHost); err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "already exists") || strings.Contains(msg, "cname") {
			return tunnel.Endpoint{}, fmt.Errorf("cloudflare dns route for %q already exists: %w", publicHost, err)
		}
		return tunnel.Endpoint{}, fmt.Errorf("create cloudflare dns route %q: %w", publicHost, err)
	}

	return tunnel.Endpoint{
		ID:       tunnelID,
		Provider: providerName,
		Name:     tunnelName,
		Host:     publicHost,
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
	if strings.TrimSpace(req.LocalURL) == "" {
		return tunnel.Session{}, errors.New("local url is required")
	}

	proc, err := p.start("cloudflared", "tunnel", "--url", req.LocalURL, "run", req.Endpoint.ID)
	if err != nil {
		return tunnel.Session{}, fmt.Errorf("start cloudflare tunnel %q: %w", req.Endpoint.ID, err)
	}

	startedAt := p.now().UTC()
	sessionID := fmt.Sprintf("%s-%d", req.Endpoint.ID, proc.PID())
	state := sessionState{
		endpointID: req.Endpoint.ID,
		process:    proc,
		startedAt:  startedAt,
	}

	p.mu.Lock()
	p.sessions[sessionID] = state
	p.mu.Unlock()

	return tunnel.Session{
		ID:         sessionID,
		Provider:   providerName,
		EndpointID: req.Endpoint.ID,
		PID:        proc.PID(),
		StartedAt:  startedAt,
		Metadata: map[string]string{
			"local_url": req.LocalURL,
		},
	}, nil
}

func (p *Provider) Stop(_ context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session id is required")
	}

	p.mu.Lock()
	state, ok := p.sessions[sessionID]
	if ok {
		delete(p.sessions, sessionID)
	}
	p.mu.Unlock()
	if !ok {
		pid, err := pidFromSessionID(sessionID)
		if err != nil {
			return fmt.Errorf("session not found: %s", sessionID)
		}
		proc, err := os.FindProcess(pid)
		if err != nil {
			return fmt.Errorf("find process for session %s: %w", sessionID, err)
		}
		if err := proc.Kill(); err != nil {
			return fmt.Errorf("stop cloudflare session %s: %w", sessionID, err)
		}
		return nil
	}

	if err := state.process.Kill(); err != nil {
		return fmt.Errorf("stop cloudflare session %s: %w", sessionID, err)
	}
	return nil
}

func (p *Provider) RemoveEndpoint(ctx context.Context, endpointID string) error {
	if err := p.Init(ctx, tunnel.ProviderConfig{}); err != nil {
		return err
	}
	endpointID = strings.TrimSpace(endpointID)
	if endpointID == "" {
		return errors.New("endpoint id is required")
	}
	if _, err := p.runCloudflared(ctx, "tunnel", "delete", endpointID); err != nil {
		return fmt.Errorf("delete cloudflare tunnel %q: %w", endpointID, err)
	}
	return nil
}

func (p *Provider) Status(ctx context.Context, endpointID string) (tunnel.EndpointStatus, error) {
	endpointID = strings.TrimSpace(endpointID)
	if endpointID == "" {
		return tunnel.EndpointStatus{}, errors.New("endpoint id is required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for sessionID, state := range p.sessions {
		if state.endpointID == endpointID {
			return tunnel.EndpointStatus{
				Ready: true,
				Endpoint: tunnel.Endpoint{
					ID:       endpointID,
					Provider: providerName,
				},
				SessionID: sessionID,
				Message:   "cloudflare tunnel process is running",
			}, nil
		}
	}
	if _, err := p.runCloudflared(ctx, "tunnel", "info", endpointID); err != nil {
		return tunnel.EndpointStatus{}, fmt.Errorf("cloudflare tunnel info for %q failed: %w", endpointID, err)
	}
	return tunnel.EndpointStatus{
		Ready: false,
		Endpoint: tunnel.Endpoint{
			ID:       endpointID,
			Provider: providerName,
		},
		Message: "cloudflare tunnel exists but no local runtime session is tracked",
	}, nil
}

func (p *Provider) runCloudflared(ctx context.Context, args ...string) (string, error) {
	return p.run(ctx, "cloudflared", args...)
}

func (p *Provider) findTunnelIDByName(ctx context.Context, name string) (string, error) {
	out, err := p.runCloudflared(ctx, "tunnel", "list", "--output", "json")
	if err != nil {
		return "", fmt.Errorf("list cloudflare tunnels: %w", err)
	}
	tunnels, err := parseTunnelList(out)
	if err != nil {
		return "", err
	}
	for _, t := range tunnels {
		if strings.EqualFold(t.Name, name) {
			return t.ID, nil
		}
	}
	return "", nil
}

type listEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func parseTunnelList(raw string) ([]listEntry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var entries []listEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("decode cloudflare tunnel list: %w", err)
	}
	return entries, nil
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

type osProcess struct {
	pid  int
	kill func() error
}

func (p *osProcess) PID() int { return p.pid }
func (p *osProcess) Kill() error {
	if p.kill == nil {
		return nil
	}
	return p.kill()
}

func startCommand(name string, args ...string) (process, error) {
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &osProcess{
		pid: cmd.Process.Pid,
		kill: func() error {
			if cmd.Process == nil {
				return nil
			}
			return cmd.Process.Kill()
		},
	}, nil
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		if s == "" {
			return "", err
		}
		return "", fmt.Errorf("%v: %s", err, s)
	}
	return s, nil
}

func pidFromSessionID(sessionID string) (int, error) {
	idx := strings.LastIndex(sessionID, "-")
	if idx < 0 || idx+1 >= len(sessionID) {
		return 0, errors.New("missing pid")
	}
	pid, err := strconv.Atoi(sessionID[idx+1:])
	if err != nil || pid <= 0 {
		return 0, errors.New("invalid pid")
	}
	return pid, nil
}

var _ tunnel.Provider = (*Provider)(nil)
