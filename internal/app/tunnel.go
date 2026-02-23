package app

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/internal/diag"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
	"github.com/goliatone/switchboard-hub/internal/tunnel/providers"
)

type TunnelProviderStatus struct {
	Name          string
	Enabled       bool
	Available     bool
	OAuthSuitable bool
	Notes         []string
	Err           string
}

var providerRegistryFactory = providers.Registry
var runCloudflaredTunnelLogin = defaultRunCloudflaredTunnelLogin
var stdioIsInteractive = defaultStdioIsInteractive

type TunnelInitOptions struct {
	Setup          bool
	NonInteractive bool
	OriginCert     string
	Mode           string
	AccountID      string
	ZoneID         string
	BaseDomain     string
	APITokenEnv    string
}

func TunnelProviders() []string {
	return providerRegistryFactory().Providers()
}

func TunnelInit(providerName string) error {
	return TunnelInitWithOptions(providerName, TunnelInitOptions{})
}

func TunnelInitWithOptions(providerName string, opts TunnelInitOptions) error {
	p, c, err := loadConfigWithPath()
	if err != nil {
		return err
	}
	providerName, err = resolveProviderName(c, providerName)
	if err != nil {
		return err
	}
	pr, err := providerRegistryFactory().Resolve(providerName)
	if err != nil {
		return actionableProviderResolveError(providerName, err)
	}

	if err := applyTunnelInitOptions(c, p, providerName, opts); err != nil {
		return err
	}
	runInit := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return pr.Init(ctx, tunnelProviderConfig(c, providerName))
	}
	if err := runInit(); err != nil {
		if shouldAttemptCloudflareSetup(c, providerName, opts, err) {
			if opts.NonInteractive {
				return fmt.Errorf("%w (setup skipped: --non-interactive enabled)", err)
			}
			if !stdioIsInteractive() {
				return fmt.Errorf("%w (setup skipped: interactive terminal required)", err)
			}
			if loginErr := runCloudflaredTunnelLogin(); loginErr != nil {
				return loginErr
			}
			if err := runInit(); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	enableProvider(c, providerName)
	if err := config.Save(p, c); err != nil {
		return err
	}
	return nil
}

func TunnelStatus(providerName string) ([]TunnelProviderStatus, error) {
	_, c, err := loadConfigWithPath()
	if err != nil {
		return nil, err
	}
	reg := providerRegistryFactory()
	names := reg.Providers()
	if strings.TrimSpace(providerName) != "" {
		names = []string{strings.ToLower(strings.TrimSpace(providerName))}
	}

	out := make([]TunnelProviderStatus, 0, len(names))
	for _, name := range names {
		pr, err := reg.Resolve(name)
		if err != nil {
			out = append(out, TunnelProviderStatus{
				Name:      name,
				Enabled:   providerEnabled(c, name),
				Available: false,
				Err:       err.Error(),
			})
			continue
		}
		caps := pr.Capabilities()
		st := TunnelProviderStatus{
			Name:          name,
			Enabled:       providerEnabled(c, name),
			OAuthSuitable: caps.OAuthSuitable,
			Notes:         caps.Notes,
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = pr.Init(ctx, tunnelProviderConfig(c, name))
		cancel()
		if err != nil {
			st.Available = false
			st.Err = err.Error()
		} else {
			st.Available = true
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func ExposeApp(name, providerName, publicHost string) error {
	p, c, err := loadConfigWithPath()
	if err != nil {
		return err
	}
	idx, appName, err := appIndexByName(c, name)
	if err != nil {
		return err
	}
	providerName, err = resolveProviderName(c, providerName)
	if err != nil {
		return err
	}
	host, err := resolveExposePublicHost(c, providerName, appName, publicHost)
	if err != nil {
		return err
	}

	pr, err := providerRegistryFactory().Resolve(providerName)
	if err != nil {
		return actionableProviderResolveError(providerName, err)
	}
	if err := initProviderWithConfig(pr, c, providerName, 20*time.Second); err != nil {
		return err
	}
	localURL := fmt.Sprintf("http://127.0.0.1:%d", c.Apps[idx].LocalPort)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	ep, err := pr.EnsureEndpoint(ctx, tunnel.EndpointRequest{
		Name:       appName,
		PublicHost: host,
		LocalURL:   localURL,
		Metadata: map[string]string{
			"app": appName,
		},
	})
	if err != nil {
		return err
	}

	c.Apps[idx].PublicEndpoint.Provider = ep.Provider
	c.Apps[idx].PublicEndpoint.Host = ep.Host
	c.Apps[idx].PublicEndpoint.EndpointID = ep.ID
	c.Apps[idx].PublicEndpoint.ActiveSessionID = ""
	c.Apps[idx].PublicEndpoint.ActiveSessionPID = 0
	c.Apps[idx].PublicEndpoint.ActiveSessionStarted = ""
	enableProvider(c, providerName)

	if err := config.Save(p, c); err != nil {
		return err
	}
	return nil
}

func AppUp(name string) error {
	p, c, err := loadConfigWithPath()
	if err != nil {
		return err
	}
	idx, _, err := appIndexByName(c, name)
	if err != nil {
		return err
	}
	a := c.Apps[idx]
	upsertLegacyRoute(c, a.LocalHost, a.LocalPort)
	if err := config.Save(p, c); err != nil {
		return err
	}
	if err := Apply(); err != nil {
		return err
	}

	if strings.TrimSpace(a.PublicEndpoint.Provider) == "" || strings.TrimSpace(a.PublicEndpoint.EndpointID) == "" {
		return nil
	}
	pr, err := providerRegistryFactory().Resolve(a.PublicEndpoint.Provider)
	if err != nil {
		return actionableProviderResolveError(a.PublicEndpoint.Provider, err)
	}
	if err := initProviderWithConfig(pr, c, a.PublicEndpoint.Provider, 20*time.Second); err != nil {
		return err
	}
	if strings.TrimSpace(a.PublicEndpoint.ActiveSessionID) != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		status, statusErr := pr.Status(ctx, a.PublicEndpoint.EndpointID)
		cancel()
		if statusErr == nil && status.Ready {
			diag.Debugf("app up: session already active for %s: %s", a.Name, status.Message)
			return nil
		}
		if statusErr != nil {
			diag.Debugf("app up: stale session for %s, status error: %v", a.Name, statusErr)
		} else {
			diag.Debugf("app up: stale session for %s, status not ready: %s", a.Name, status.Message)
		}
		c.Apps[idx].PublicEndpoint.ActiveSessionID = ""
		c.Apps[idx].PublicEndpoint.ActiveSessionPID = 0
		c.Apps[idx].PublicEndpoint.ActiveSessionStarted = ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	session, err := pr.Start(ctx, tunnel.StartRequest{
		Endpoint: tunnel.Endpoint{
			ID:       a.PublicEndpoint.EndpointID,
			Provider: a.PublicEndpoint.Provider,
			Host:     a.PublicEndpoint.Host,
			Name:     a.Name,
		},
		LocalURL: fmt.Sprintf("http://127.0.0.1:%d", a.LocalPort),
	})
	if err != nil {
		return err
	}

	c.Apps[idx].PublicEndpoint.ActiveSessionID = session.ID
	c.Apps[idx].PublicEndpoint.ActiveSessionPID = session.PID
	c.Apps[idx].PublicEndpoint.ActiveSessionStarted = session.StartedAt.UTC().Format(time.RFC3339)
	if err := config.Save(p, c); err != nil {
		return err
	}
	return nil
}

func AppDown(name string) error {
	p, c, err := loadConfigWithPath()
	if err != nil {
		return err
	}
	idx, _, err := appIndexByName(c, name)
	if err != nil {
		return err
	}
	a := c.Apps[idx]
	sessionID := strings.TrimSpace(a.PublicEndpoint.ActiveSessionID)
	if sessionID == "" {
		return nil
	}
	providerName := strings.TrimSpace(a.PublicEndpoint.Provider)
	if providerName == "" {
		providerName = strings.TrimSpace(c.Tunnel.DefaultProvider)
	}
	pr, err := providerRegistryFactory().Resolve(providerName)
	if err != nil {
		return actionableProviderResolveError(providerName, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := pr.Stop(ctx, sessionID); err != nil {
		if !isIdempotentStopError(err) {
			return err
		}
		diag.Debugf("app down: ignoring stop error for session %s: %v", sessionID, err)
	}
	c.Apps[idx].PublicEndpoint.ActiveSessionID = ""
	c.Apps[idx].PublicEndpoint.ActiveSessionPID = 0
	c.Apps[idx].PublicEndpoint.ActiveSessionStarted = ""
	if err := config.Save(p, c); err != nil {
		return err
	}
	return nil
}

func loadConfigWithPath() (string, *config.Config, error) {
	p, err := cfgPath()
	if err != nil {
		return "", nil, err
	}
	c, err := config.LoadOrCreateDefault(p)
	if err != nil {
		return "", nil, err
	}
	return p, c, nil
}

func resolveProviderName(c *config.Config, providerName string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(providerName))
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(c.Tunnel.DefaultProvider))
	}
	if name == "" {
		return "", fmt.Errorf("provider is required")
	}
	if _, err := providerRegistryFactory().Resolve(name); err != nil {
		return "", actionableProviderResolveError(name, err)
	}
	return name, nil
}

func providerEnabled(c *config.Config, providerName string) bool {
	if c == nil {
		return false
	}
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	if providerName == "" {
		return false
	}
	cfg, ok := c.Tunnel.Providers[providerName]
	return ok && cfg.Enabled
}

func enableProvider(c *config.Config, providerName string) {
	if c == nil {
		return
	}
	name := strings.ToLower(strings.TrimSpace(providerName))
	if name == "" {
		return
	}
	cfg := c.Tunnel.Providers[name]
	cfg.Enabled = true
	if cfg.Values == nil {
		cfg.Values = map[string]string{}
	}
	c.Tunnel.Providers[name] = cfg
	if strings.TrimSpace(c.Tunnel.DefaultProvider) == "" {
		c.Tunnel.DefaultProvider = name
	}
}

func tunnelProviderConfig(c *config.Config, providerName string) tunnel.ProviderConfig {
	out := tunnel.ProviderConfig{
		Values: map[string]string{},
	}
	if c == nil {
		return out
	}
	cfg, ok := c.Tunnel.Providers[strings.ToLower(strings.TrimSpace(providerName))]
	if !ok {
		return out
	}
	for k, v := range cfg.Values {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out.Values[k] = v
	}
	if cfg.AccountID != "" {
		out.Values["account_id"] = cfg.AccountID
	}
	if cfg.Zone != "" {
		out.Values["zone"] = cfg.Zone
		out.Values["zone_id"] = cfg.Zone
	}
	return out
}

func initProviderWithConfig(pr tunnel.Provider, c *config.Config, providerName string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return pr.Init(ctx, tunnelProviderConfig(c, providerName))
}

func applyTunnelInitOptions(c *config.Config, configPath, providerName string, opts TunnelInitOptions) error {
	if c == nil {
		return fmt.Errorf("config is nil")
	}
	name := strings.ToLower(strings.TrimSpace(providerName))
	if name != "cloudflare" {
		return nil
	}
	cfg := c.Tunnel.Providers[name]
	if cfg.Values == nil {
		cfg.Values = map[string]string{}
	}

	mode := strings.ToLower(strings.TrimSpace(opts.Mode))
	if mode != "" {
		if mode != "cli" && mode != "api" {
			return fmt.Errorf("invalid cloudflare mode %q (expected cli or api)", opts.Mode)
		}
		cfg.Values["mode"] = mode
	}

	if accountID := strings.TrimSpace(opts.AccountID); accountID != "" {
		cfg.AccountID = accountID
		cfg.Values["account_id"] = accountID
	}
	if zoneID := strings.TrimSpace(opts.ZoneID); zoneID != "" {
		cfg.Zone = zoneID
		cfg.Values["zone_id"] = zoneID
	}
	if baseDomain := strings.ToLower(strings.TrimSpace(opts.BaseDomain)); baseDomain != "" {
		if _, err := normalizePublicHost("switchd-bootstrap." + baseDomain); err != nil {
			return fmt.Errorf("invalid --base-domain %q: %w", opts.BaseDomain, err)
		}
		cfg.Values["base_domain"] = baseDomain
	}
	if tokenEnv := strings.TrimSpace(opts.APITokenEnv); tokenEnv != "" {
		cfg.Values["api_token_env"] = tokenEnv
	}

	rawPath := strings.TrimSpace(opts.OriginCert)
	if rawPath != "" {
		baseDir := filepath.Dir(configPath)
		resolved, err := resolvePathInput(rawPath, baseDir)
		if err != nil {
			return err
		}
		cfg.Values["origincert"] = resolved
	}

	if strings.TrimSpace(cfg.Values["mode"]) == "api" && strings.TrimSpace(cfg.Values["api_token_env"]) == "" {
		cfg.Values["api_token_env"] = "SWITCHD_CF_API_TOKEN"
	}
	c.Tunnel.Providers[name] = cfg
	return nil
}

func shouldAttemptCloudflareSetup(c *config.Config, providerName string, opts TunnelInitOptions, initErr error) bool {
	if strings.ToLower(strings.TrimSpace(providerName)) != "cloudflare" || !opts.Setup {
		return false
	}
	if providerModeFromConfig(c, providerName, opts.Mode) == "api" {
		return false
	}
	details, ok := tunnel.ActionableFromError(initErr)
	if !ok {
		return true
	}
	switch strings.TrimSpace(details.Code) {
	case "CF_ORIGIN_CERT_MISSING", "CF_AUTH_REQUIRED", "CF_AUTH_CHECK_FAILED":
		return true
	default:
		return false
	}
}

func providerModeFromConfig(c *config.Config, providerName string, modeOverride string) string {
	mode := strings.ToLower(strings.TrimSpace(modeOverride))
	if mode != "" {
		return mode
	}
	if c == nil {
		return "cli"
	}
	name := strings.ToLower(strings.TrimSpace(providerName))
	cfg, ok := c.Tunnel.Providers[name]
	if !ok {
		return "cli"
	}
	mode = strings.ToLower(strings.TrimSpace(cfg.Values["mode"]))
	if mode == "" {
		return "cli"
	}
	return mode
}

func resolveExposePublicHost(c *config.Config, providerName, appName, rawHost string) (string, error) {
	if strings.TrimSpace(rawHost) != "" {
		return normalizePublicHost(rawHost)
	}
	name := strings.ToLower(strings.TrimSpace(providerName))
	if name != "cloudflare" {
		return "", fmt.Errorf("--public-host is required for provider %q", providerName)
	}
	if c == nil {
		return "", fmt.Errorf("config is nil")
	}
	cfg := c.Tunnel.Providers[name]
	baseDomain := strings.ToLower(strings.TrimSpace(cfg.Values["base_domain"]))
	if baseDomain == "" {
		return "", fmt.Errorf("--public-host is required (or configure tunnels.providers.cloudflare.values.base_domain)")
	}
	return normalizePublicHost(appName + "." + baseDomain)
}

func defaultRunCloudflaredTunnelLogin() error {
	diag.LogCommand("cloudflared", "tunnel", "login")
	cmd := exec.Command("cloudflared", "tunnel", "login")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cloudflare setup failed (cloudflared tunnel login): %w", diag.SanitizeError(err))
	}
	return nil
}

func defaultStdioIsInteractive() bool {
	in, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	out, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (in.Mode()&os.ModeCharDevice) != 0 && (out.Mode()&os.ModeCharDevice) != 0
}

func appIndexByName(c *config.Config, name string) (int, string, error) {
	if c == nil {
		return -1, "", fmt.Errorf("config is nil")
	}
	appName, err := normalizeAppNameInput(name)
	if err != nil {
		return -1, "", err
	}
	idx := findAppByName(c, appName)
	if idx < 0 {
		return -1, "", fmt.Errorf("app not found: %s", appName)
	}
	return idx, appName, nil
}

func normalizePublicHost(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimSuffix(s, "/")
	if s == "" {
		return "", fmt.Errorf("public host is required")
	}
	if strings.Contains(s, "/") {
		return "", fmt.Errorf("public host must not include path: %q", raw)
	}
	if strings.Contains(s, ":") {
		return "", fmt.Errorf("public host must not include port: %q", raw)
	}
	if !strings.Contains(s, ".") {
		return "", fmt.Errorf("public host must be a fully qualified domain: %q", raw)
	}
	return s, nil
}

type AppTunnelHealth struct {
	AppName      string
	Provider     string
	EndpointHost string
	EndpointID   string
	SessionID    string
	SessionPID   int
	StartedAt    string
	Ready        bool
	Message      string
	Err          string
}

func AppTunnelHealthStatus() ([]AppTunnelHealth, error) {
	_, c, err := loadConfigWithPath()
	if err != nil {
		return nil, err
	}
	return appTunnelHealthStatusFromConfig(c)
}

func appTunnelHealthStatusFromConfig(c *config.Config) ([]AppTunnelHealth, error) {
	if c == nil {
		return nil, fmt.Errorf("config is nil")
	}
	out := make([]AppTunnelHealth, 0, len(c.Apps))
	for _, a := range c.Apps {
		h := AppTunnelHealth{
			AppName:      a.Name,
			Provider:     strings.TrimSpace(a.PublicEndpoint.Provider),
			EndpointHost: strings.TrimSpace(a.PublicEndpoint.Host),
			EndpointID:   strings.TrimSpace(a.PublicEndpoint.EndpointID),
			SessionID:    strings.TrimSpace(a.PublicEndpoint.ActiveSessionID),
			SessionPID:   a.PublicEndpoint.ActiveSessionPID,
			StartedAt:    strings.TrimSpace(a.PublicEndpoint.ActiveSessionStarted),
		}
		if h.Provider == "" || h.EndpointID == "" {
			h.Message = "no tunnel endpoint configured"
			out = append(out, h)
			continue
		}

		pr, err := providerRegistryFactory().Resolve(h.Provider)
		if err != nil {
			h.Err = actionableProviderResolveError(h.Provider, err).Error()
			out = append(out, h)
			continue
		}
		if err := initProviderWithConfig(pr, c, h.Provider, 8*time.Second); err != nil {
			h.Err = err.Error()
			out = append(out, h)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		st, err := pr.Status(ctx, h.EndpointID)
		cancel()
		if err != nil {
			h.Err = err.Error()
			if h.SessionPID > 0 && processAlive(h.SessionPID) {
				h.Ready = true
				h.Message = "session pid is running"
			}
			out = append(out, h)
			continue
		}
		h.Ready = st.Ready
		h.Message = st.Message
		if strings.TrimSpace(st.SessionID) != "" {
			h.SessionID = strings.TrimSpace(st.SessionID)
		}
		out = append(out, h)
	}
	return out, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

func isIdempotentStopError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "session not found") ||
		strings.Contains(msg, "not running") ||
		strings.Contains(msg, "no such process") ||
		strings.Contains(msg, "already stopped")
}

func actionableProviderResolveError(providerName string, err error) error {
	available := providerRegistryFactory().Providers()
	return fmt.Errorf("provider %q is unavailable: %v (available providers: %s)", providerName, err, strings.Join(available, ", "))
}

func sessionSummary(pid int, started string) string {
	parts := []string{}
	if pid > 0 {
		parts = append(parts, "pid="+strconv.Itoa(pid))
	}
	if strings.TrimSpace(started) != "" {
		parts = append(parts, "started="+strings.TrimSpace(started))
	}
	return strings.Join(parts, " ")
}
