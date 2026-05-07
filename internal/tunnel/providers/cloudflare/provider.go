package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/goliatone/switchboard-hub/internal/diag"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

const (
	providerName = "cloudflare"

	errCodeCloudflaredMissing = "CF_CLOUDFLARED_MISSING"
	errCodeOriginCertMissing  = "CF_ORIGIN_CERT_MISSING"
	errCodeAuthRequired       = "CF_AUTH_REQUIRED"
	errCodeAuthCheckFailed    = "CF_AUTH_CHECK_FAILED"
	errCodeAPIConfigInvalid   = "CF_API_CONFIG_INVALID"
	errCodeAPITokenMissing    = "CF_API_TOKEN_MISSING"
	errCodeAPIAuthFailed      = "CF_API_AUTH_FAILED"
	errCodeAPIRequestFailed   = "CF_API_REQUEST_FAILED"
	errCodeAPIDNSConflict     = "CF_API_DNS_CONFLICT"
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

	mu             sync.Mutex
	sessions       map[string]sessionState
	originCertPath string
	runtimeConfig  providerRuntimeConfig
}

type sessionState struct {
	endpointID string
	process    process
	startedAt  time.Time
}

type initError struct {
	code      string
	what      string
	why       string
	checks    []string
	nextSteps []string
	cause     error
}

func (e *initError) Error() string {
	if e == nil {
		return ""
	}
	s := strings.TrimSpace(e.what)
	if s == "" {
		s = "cloudflare tunnel init failed"
	}
	if strings.TrimSpace(e.code) != "" {
		s += " [" + strings.TrimSpace(e.code) + "]"
	}
	if e.cause != nil {
		s += ": " + e.cause.Error()
	}
	return s
}

func (e *initError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *initError) Actionable() tunnel.ActionableDetails {
	if e == nil {
		return tunnel.ActionableDetails{}
	}
	return tunnel.ActionableDetails{
		Code:      strings.TrimSpace(e.code),
		What:      strings.TrimSpace(e.what),
		Why:       strings.TrimSpace(e.why),
		Checks:    append([]string{}, e.checks...),
		NextSteps: append([]string{}, e.nextSteps...),
	}
}

type Option func(*Provider)

func New(opts ...Option) *Provider {
	p := &Provider{
		run:      runCommand,
		lookPath: exec.LookPath,
		start:    startCommand,
		now:      time.Now,
		sessions: map[string]sessionState{},
		runtimeConfig: providerRuntimeConfig{
			Mode:        providerModeCLI,
			APITokenEnv: defaultCloudflareAPITokenEnv,
			APIBaseURL:  defaultCloudflareAPIBaseURL,
		},
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
			"Cloudflare named tunnels support both CLI login mode and API token mode.",
			"Cloudflare free quick tunnels are not suitable for stable OAuth callbacks.",
		},
	}
}

func (p *Provider) Init(ctx context.Context, cfg tunnel.ProviderConfig) error {
	runtimeCfg, err := resolveProviderRuntimeConfig(cfg)
	if err != nil {
		return err
	}
	p.setRuntimeConfig(runtimeCfg)

	if _, err := p.lookPath("cloudflared"); err != nil {
		return &initError{
			code: errCodeCloudflaredMissing,
			what: "cloudflared CLI is not installed or not available on PATH",
			why:  "switchd uses cloudflared for Cloudflare named tunnel lifecycle commands",
			checks: []string{
				"looked up `cloudflared` on PATH",
			},
			nextSteps: []string{
				"Install cloudflared: brew install cloudflared",
				"Re-run: switchd tunnel init --provider cloudflare",
			},
			cause: diag.SanitizeError(err),
		}
	}
	if runtimeCfg.Mode == providerModeAPI {
		if err := p.initAPIMode(ctx, runtimeCfg); err != nil {
			return err
		}
		p.setOriginCertPath("")
		return nil
	}
	originCertPath, checks, err := resolveOriginCertPath(cfg)
	if err != nil {
		if ie, ok := err.(*initError); ok {
			ie.checks = append(append([]string{}, checks...), ie.checks...)
			return ie
		}
		return err
	}
	p.setOriginCertPath(originCertPath)
	if _, err := p.runCloudflared(ctx, "tunnel", "list", "--output", "json"); err != nil {
		return classifyCloudflaredAuthError(err, checks)
	}
	return nil
}

func (p *Provider) EnsureEndpoint(ctx context.Context, req tunnel.EndpointRequest) (tunnel.Endpoint, error) {
	if p.mode() == providerModeAPI {
		return p.ensureEndpointAPIMode(ctx, req)
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
	if p.mode() == providerModeAPI {
		return p.startAPIMode(ctx, req)
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
			if isProcessGoneError(err) {
				return nil
			}
			return fmt.Errorf("stop cloudflare session %s: %w", sessionID, err)
		}
		return nil
	}

	if err := state.process.Kill(); err != nil {
		if isProcessGoneError(err) {
			return nil
		}
		return fmt.Errorf("stop cloudflare session %s: %w", sessionID, err)
	}
	return nil
}

func (p *Provider) RemoveEndpoint(ctx context.Context, endpointID string) error {
	if p.mode() == providerModeAPI {
		return p.removeEndpointAPIMode(ctx, endpointID)
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
	for sessionID, state := range p.sessions {
		if state.endpointID == endpointID {
			p.mu.Unlock()
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
	p.mu.Unlock()
	if p.mode() == providerModeAPI {
		return p.statusAPIMode(ctx, endpointID)
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
	if p.mode() == providerModeCLI {
		if cert := strings.TrimSpace(p.getOriginCertPath()); cert != "" {
			merged := make([]string, 0, len(args)+2)
			merged = append(merged, "--origincert", cert)
			merged = append(merged, args...)
			args = merged
		}
	}
	return p.run(ctx, "cloudflared", args...)
}

func (p *Provider) mode() string {
	mode := strings.TrimSpace(strings.ToLower(p.getRuntimeConfig().Mode))
	if mode == "" {
		return providerModeCLI
	}
	return mode
}

func (p *Provider) setRuntimeConfig(cfg providerRuntimeConfig) {
	p.mu.Lock()
	p.runtimeConfig = cfg
	p.mu.Unlock()
}

func (p *Provider) getRuntimeConfig() providerRuntimeConfig {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.runtimeConfig
}

func (p *Provider) setOriginCertPath(path string) {
	p.mu.Lock()
	p.originCertPath = strings.TrimSpace(path)
	p.mu.Unlock()
}

func (p *Provider) getOriginCertPath() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.originCertPath
}

func classifyCloudflaredAuthError(err error, checks []string) error {
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(msg, "origincert") || strings.Contains(msg, "origin cert") {
		return &initError{
			code: errCodeOriginCertMissing,
			what: "cloudflare origin certificate could not be located",
			why:  "cloudflared requires an origin certificate (cert.pem) to manage named tunnels",
			checks: append(append([]string{}, checks...),
				"executed: cloudflared tunnel list --output json",
			),
			nextSteps: []string{
				"Authenticate once: cloudflared tunnel login",
				"Or provide cert path: switchd tunnel init --provider cloudflare --origincert ~/.cloudflared/cert.pem",
			},
			cause: diag.SanitizeError(err),
		}
	}
	if strings.Contains(msg, "unauthorized") || strings.Contains(msg, "not logged in") || strings.Contains(msg, "permission denied") {
		return &initError{
			code: errCodeAuthRequired,
			what: "cloudflare authentication check failed",
			why:  "the current Cloudflare credentials are missing or invalid for tunnel operations",
			checks: append(append([]string{}, checks...),
				"executed: cloudflared tunnel list --output json",
			),
			nextSteps: []string{
				"Authenticate once: cloudflared tunnel login",
				"Verify access: cloudflared tunnel list --output json",
				"Re-run: switchd tunnel init --provider cloudflare",
			},
			cause: diag.SanitizeError(err),
		}
	}
	return &initError{
		code: errCodeAuthCheckFailed,
		what: "cloudflare auth preflight failed",
		why:  "cloudflared could not complete tunnel list during provider initialization",
		checks: append(append([]string{}, checks...),
			"executed: cloudflared tunnel list --output json",
		),
		nextSteps: []string{
			"Run diagnostic command: cloudflared tunnel list --output json",
			"Re-authenticate: cloudflared tunnel login",
			"Retry: switchd tunnel init --provider cloudflare",
		},
		cause: diag.SanitizeError(err),
	}
}

func resolveOriginCertPath(cfg tunnel.ProviderConfig) (string, []string, error) {
	checks := []string{}
	candidates := []string{}
	addCandidate := func(raw string) {
		s := strings.TrimSpace(raw)
		if s == "" {
			return
		}
		if slices.Contains(candidates, s) {
			return
		}
		candidates = append(candidates, s)
	}

	if env := strings.TrimSpace(os.Getenv("TUNNEL_ORIGIN_CERT")); env != "" {
		addCandidate(env)
		checks = append(checks, "env TUNNEL_ORIGIN_CERT is set")
	} else {
		checks = append(checks, "env TUNNEL_ORIGIN_CERT is not set")
	}
	if v := providerConfigValue(cfg, "origincert", "origin_cert"); v != "" {
		addCandidate(v)
		checks = append(checks, "provider config includes origin cert path")
	} else {
		checks = append(checks, "provider config does not include origin cert path")
	}

	for _, def := range defaultOriginCertCandidates() {
		addCandidate(def)
	}

	checkedPaths := []string{}
	for _, raw := range candidates {
		path := expandUserPath(raw)
		path = filepath.Clean(path)
		checkedPaths = append(checkedPaths, path)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.IsDir() {
			continue
		}
		return path, append(checks, "resolved origin cert: "+path), nil
	}

	return "", append(checks, "checked paths: "+strings.Join(checkedPaths, ", ")), &initError{
		code: errCodeOriginCertMissing,
		what: "cloudflare origin certificate could not be located",
		why:  "cloudflared requires cert.pem to manage named tunnels",
		nextSteps: []string{
			"Authenticate once: cloudflared tunnel login",
			"Or set TUNNEL_ORIGIN_CERT=/absolute/path/to/cert.pem",
			"Or use: switchd tunnel init --provider cloudflare --origincert /absolute/path/to/cert.pem",
		},
	}
}

func providerConfigValue(cfg tunnel.ProviderConfig, keys ...string) string {
	if len(cfg.Values) == 0 {
		return ""
	}
	for _, key := range keys {
		k := strings.TrimSpace(key)
		if k == "" {
			continue
		}
		if v := strings.TrimSpace(cfg.Values[k]); v != "" {
			return v
		}
	}
	return ""
}

func defaultOriginCertCandidates() []string {
	base := []string{
		"~/.cloudflared/cert.pem",
		"~/.cloudflare-warp/cert.pem",
		"~/cloudflare-warp/cert.pem",
		"/etc/cloudflared/cert.pem",
		"/usr/local/etc/cloudflared/cert.pem",
	}
	out := make([]string, 0, len(base))
	for _, p := range base {
		out = append(out, expandUserPath(p))
	}
	return out
}

func expandUserPath(in string) string {
	s := strings.TrimSpace(in)
	if !strings.HasPrefix(s, "~/") {
		return s
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return s
	}
	return filepath.Join(home, strings.TrimPrefix(s, "~/"))
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

func isProcessGoneError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "process already finished") ||
		strings.Contains(msg, "no such process")
}

var _ tunnel.Provider = (*Provider)(nil)
