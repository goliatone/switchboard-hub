package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/goliatone/switchboard-hub/internal/caddy"
	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

type ConfigStore interface {
	Load(path string) (*config.Config, error)
	LoadOrDefault(path string) (*config.Config, error)
	LoadOrCreateDefault(path string) (*config.Config, error)
	Save(path string, cfg *config.Config) error
}

type ProviderRegistry interface {
	Providers() []string
	Resolve(name string) (tunnel.Provider, error)
}

type ServiceOptions struct {
	ConfigPath               string
	Store                    ConfigStore
	ProviderRegistry         ProviderRegistry
	ApplyConfig              func(path string, cfg *config.Config) error
	Now                      func() time.Time
	RunCloudflareTunnelLogin func() error
	StdioIsInteractive       func() bool
}

type Service struct {
	configPath               string
	store                    ConfigStore
	providerRegistry         ProviderRegistry
	applyConfig              func(path string, cfg *config.Config) error
	now                      func() time.Time
	runCloudflareTunnelLogin func() error
	stdioIsInteractive       func() bool
}

type defaultConfigStore struct{}

func (defaultConfigStore) Load(path string) (*config.Config, error) {
	return config.Load(path)
}

func (defaultConfigStore) LoadOrDefault(path string) (*config.Config, error) {
	return config.LoadOrDefault(path)
}

func (defaultConfigStore) LoadOrCreateDefault(path string) (*config.Config, error) {
	return config.LoadOrCreateDefault(path)
}

func (defaultConfigStore) Save(path string, cfg *config.Config) error {
	return config.Save(path, cfg)
}

func NewService(opts ServiceOptions) *Service {
	svc := &Service{
		configPath:               strings.TrimSpace(opts.ConfigPath),
		store:                    opts.Store,
		applyConfig:              opts.ApplyConfig,
		now:                      opts.Now,
		runCloudflareTunnelLogin: opts.RunCloudflareTunnelLogin,
		stdioIsInteractive:       opts.StdioIsInteractive,
	}
	if svc.store == nil {
		svc.store = defaultConfigStore{}
	}
	if opts.ProviderRegistry != nil {
		svc.providerRegistry = opts.ProviderRegistry
	} else {
		svc.providerRegistry = providerRegistryFactory()
	}
	if svc.applyConfig == nil {
		svc.applyConfig = applyConfig
	}
	if svc.now == nil {
		svc.now = time.Now
	}
	if svc.runCloudflareTunnelLogin == nil {
		svc.runCloudflareTunnelLogin = runCloudflaredTunnelLogin
	}
	if svc.stdioIsInteractive == nil {
		svc.stdioIsInteractive = stdioIsInteractive
	}
	return svc
}

func DefaultService() *Service {
	return NewService(ServiceOptions{})
}

func (s *Service) ConfigPath() (string, error) {
	if s != nil && strings.TrimSpace(s.configPath) != "" {
		return filepath.Clean(strings.TrimSpace(s.configPath)), nil
	}
	override := strings.TrimSpace(os.Getenv("SWITCHD_CONFIG_PATH"))
	if override != "" {
		return filepath.Clean(override), nil
	}
	home, err := runUserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "switchboard-hub", "config.yaml"), nil
}

func (s *Service) LoadConfig() (string, *config.Config, error) {
	p, err := s.ConfigPath()
	if err != nil {
		return "", nil, err
	}
	c, err := s.store.Load(p)
	if err != nil {
		return "", nil, err
	}
	return p, c, nil
}

func (s *Service) LoadOrDefaultConfig() (string, *config.Config, error) {
	p, err := s.ConfigPath()
	if err != nil {
		return "", nil, err
	}
	c, err := s.store.LoadOrDefault(p)
	if err != nil {
		return "", nil, err
	}
	return p, c, nil
}

func (s *Service) LoadOrCreateDefaultConfig() (string, *config.Config, error) {
	p, err := s.ConfigPath()
	if err != nil {
		return "", nil, err
	}
	c, err := s.store.LoadOrCreateDefault(p)
	if err != nil {
		return "", nil, err
	}
	return p, c, nil
}

func (s *Service) SaveConfig(cfg *config.Config) error {
	p, err := s.ConfigPath()
	if err != nil {
		return err
	}
	return s.store.Save(p, cfg)
}

func (s *Service) SaveConfigAt(path string, cfg *config.Config) error {
	return s.store.Save(path, cfg)
}

func (s *Service) TunnelProviders() []string {
	if s == nil || s.providerRegistry == nil {
		return nil
	}
	return s.providerRegistry.Providers()
}

func (s *Service) CreateApp(nameOrHost string, port int) error {
	p, c, err := s.LoadOrCreateDefaultConfig()
	if err != nil {
		return err
	}
	if _, err := upsertApp(c, nameOrHost, port); err != nil {
		return err
	}
	return s.store.Save(p, c)
}

func (s *Service) RemoveApp(name string) error {
	p, c, err := s.LoadOrDefaultConfig()
	if err != nil {
		return err
	}
	name, err = normalizeAppNameInput(name)
	if err != nil {
		return err
	}
	idx := findAppByName(c, name)
	if idx < 0 {
		return fmt.Errorf("app not found: %s", name)
	}
	host := c.Apps[idx].LocalHost
	c.Apps = append(c.Apps[:idx], c.Apps[idx+1:]...)
	removeLegacyRouteByHost(c, host)
	return s.store.Save(p, c)
}

func (s *Service) ListApps() ([]config.App, error) {
	_, c, err := s.LoadOrDefaultConfig()
	if err != nil {
		return nil, err
	}
	out := make([]config.App, len(c.Apps))
	copy(out, c.Apps)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Service) Apply() error {
	p, c, err := s.LoadOrDefaultConfig()
	if err != nil {
		return err
	}
	return s.applyConfig(p, c)
}

func (s *Service) ApplyConfig(path string, cfg *config.Config) error {
	return s.applyConfig(path, cfg)
}

func (s *Service) TunnelInit(providerName string) error {
	return s.TunnelInitWithOptions(providerName, TunnelInitOptions{})
}

func (s *Service) TunnelInitWithOptions(providerName string, opts TunnelInitOptions) error {
	p, c, err := s.LoadOrCreateDefaultConfig()
	if err != nil {
		return err
	}
	providerName, err = s.resolveProviderName(c, providerName)
	if err != nil {
		return err
	}
	pr, err := s.providerRegistry.Resolve(providerName)
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
			if !s.stdioIsInteractive() {
				return fmt.Errorf("%w (setup skipped: interactive terminal required)", err)
			}
			if loginErr := s.runCloudflareTunnelLogin(); loginErr != nil {
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
	return s.store.Save(p, c)
}

func (s *Service) TunnelStatus(providerName string) ([]TunnelProviderStatus, error) {
	_, c, err := s.LoadOrCreateDefaultConfig()
	if err != nil {
		return nil, err
	}
	names := s.providerRegistry.Providers()
	if strings.TrimSpace(providerName) != "" {
		names = []string{strings.ToLower(strings.TrimSpace(providerName))}
	}

	out := make([]TunnelProviderStatus, 0, len(names))
	for _, name := range names {
		pr, err := s.providerRegistry.Resolve(name)
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

func (s *Service) ExposeApp(name, providerName, publicHost string) error {
	p, c, err := s.LoadOrCreateDefaultConfig()
	if err != nil {
		return err
	}
	if _, err := s.EnsurePublicEndpoint(c, name, providerName, publicHost); err != nil {
		return err
	}
	return s.store.Save(p, c)
}

func (s *Service) AppUp(name string) error {
	p, c, err := s.LoadOrCreateDefaultConfig()
	if err != nil {
		return err
	}
	idx, _, err := appIndexByName(c, name)
	if err != nil {
		return err
	}
	a := c.Apps[idx]
	upsertLegacyRoute(c, a.LocalHost, a.LocalPort)
	if err := s.store.Save(p, c); err != nil {
		return err
	}
	if err := s.applyConfig(p, c); err != nil {
		return err
	}
	if _, err := s.EnsureAppRuntime(c, name); err != nil {
		return err
	}
	return s.store.Save(p, c)
}

func (s *Service) AppDown(name string) error {
	p, c, err := s.LoadOrCreateDefaultConfig()
	if err != nil {
		return err
	}
	if _, err := s.StopAppRuntime(c, name); err != nil {
		return err
	}
	return s.store.Save(p, c)
}

func (s *Service) EnsurePublicEndpoint(c *config.Config, name, providerName, publicHost string) (config.App, error) {
	if c == nil {
		return config.App{}, fmt.Errorf("config is nil")
	}
	idx, appName, err := appIndexByName(c, name)
	if err != nil {
		return config.App{}, err
	}
	providerName, err = s.resolveProviderName(c, providerName)
	if err != nil {
		return config.App{}, err
	}
	host, err := resolveExposePublicHost(c, providerName, appName, publicHost)
	if err != nil {
		return config.App{}, err
	}

	pr, err := s.providerRegistry.Resolve(providerName)
	if err != nil {
		return config.App{}, actionableProviderResolveError(providerName, err)
	}
	if err := initProviderWithConfig(pr, c, providerName, 20*time.Second); err != nil {
		return config.App{}, err
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
		return config.App{}, err
	}

	c.Apps[idx].PublicEndpoint.Provider = ep.Provider
	c.Apps[idx].PublicEndpoint.Host = ep.Host
	c.Apps[idx].PublicEndpoint.EndpointID = ep.ID
	c.Apps[idx].PublicEndpoint.ActiveSessionID = ""
	c.Apps[idx].PublicEndpoint.ActiveSessionPID = 0
	c.Apps[idx].PublicEndpoint.ActiveSessionStarted = ""
	enableProvider(c, providerName)
	return c.Apps[idx], nil
}

func (s *Service) EnsureAppRuntime(c *config.Config, name string) (config.App, error) {
	if c == nil {
		return config.App{}, fmt.Errorf("config is nil")
	}
	idx, _, err := appIndexByName(c, name)
	if err != nil {
		return config.App{}, err
	}
	a := c.Apps[idx]
	if strings.TrimSpace(a.PublicEndpoint.Provider) == "" || strings.TrimSpace(a.PublicEndpoint.EndpointID) == "" {
		return a, nil
	}
	pr, err := s.providerRegistry.Resolve(a.PublicEndpoint.Provider)
	if err != nil {
		return config.App{}, actionableProviderResolveError(a.PublicEndpoint.Provider, err)
	}
	if err := initProviderWithConfig(pr, c, a.PublicEndpoint.Provider, 20*time.Second); err != nil {
		return config.App{}, err
	}
	if strings.TrimSpace(a.PublicEndpoint.ActiveSessionID) != "" {
		if a.PublicEndpoint.ActiveSessionPID > 0 && processAlive(a.PublicEndpoint.ActiveSessionPID) {
			return c.Apps[idx], nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		status, statusErr := pr.Status(ctx, a.PublicEndpoint.EndpointID)
		cancel()
		if statusErr == nil && status.Ready {
			if strings.TrimSpace(status.SessionID) != "" {
				c.Apps[idx].PublicEndpoint.ActiveSessionID = strings.TrimSpace(status.SessionID)
			}
			return c.Apps[idx], nil
		}
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		stopErr := pr.Stop(ctx, a.PublicEndpoint.ActiveSessionID)
		cancel()
		if stopErr != nil && !isIdempotentStopError(stopErr) {
			return config.App{}, stopErr
		}
		c.Apps[idx].PublicEndpoint.ActiveSessionID = ""
		c.Apps[idx].PublicEndpoint.ActiveSessionPID = 0
		c.Apps[idx].PublicEndpoint.ActiveSessionStarted = ""
		a = c.Apps[idx]
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
		return config.App{}, err
	}

	c.Apps[idx].PublicEndpoint.ActiveSessionID = session.ID
	c.Apps[idx].PublicEndpoint.ActiveSessionPID = session.PID
	c.Apps[idx].PublicEndpoint.ActiveSessionStarted = session.StartedAt.UTC().Format(time.RFC3339)
	return c.Apps[idx], nil
}

func (s *Service) StopAppRuntime(c *config.Config, name string) (config.App, error) {
	if c == nil {
		return config.App{}, fmt.Errorf("config is nil")
	}
	idx, _, err := appIndexByName(c, name)
	if err != nil {
		return config.App{}, err
	}
	a := c.Apps[idx]
	sessionID := strings.TrimSpace(a.PublicEndpoint.ActiveSessionID)
	if sessionID == "" {
		return a, nil
	}
	providerName := strings.TrimSpace(a.PublicEndpoint.Provider)
	if providerName == "" {
		providerName = strings.TrimSpace(c.Tunnel.DefaultProvider)
	}
	pr, err := s.providerRegistry.Resolve(providerName)
	if err != nil {
		return config.App{}, actionableProviderResolveError(providerName, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := pr.Stop(ctx, sessionID); err != nil {
		if !isIdempotentStopError(err) {
			return config.App{}, err
		}
	}
	c.Apps[idx].PublicEndpoint.ActiveSessionID = ""
	c.Apps[idx].PublicEndpoint.ActiveSessionPID = 0
	c.Apps[idx].PublicEndpoint.ActiveSessionStarted = ""
	return c.Apps[idx], nil
}

func (s *Service) resolveProviderName(c *config.Config, providerName string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(providerName))
	if name == "" {
		name = strings.ToLower(strings.TrimSpace(c.Tunnel.DefaultProvider))
	}
	if name == "" {
		return "", fmt.Errorf("provider is required")
	}
	if _, err := s.providerRegistry.Resolve(name); err != nil {
		return "", actionableProviderResolveError(name, err)
	}
	return name, nil
}

func applyConfig(path string, c *config.Config) error {
	if err := validateTLSConfig(c); err != nil {
		return fmt.Errorf("invalid TLS config: %w", err)
	}

	client := &http.Client{Timeout: 2 * time.Second}
	req, _ := http.NewRequest("GET", strings.TrimRight(c.Caddy.Admin, "/")+"/config/", nil)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("caddy admin not reachable at %s (is Caddy running?): %w", c.Caddy.Admin, err)
	}
	_ = resp.Body.Close()

	cfgJSON, err := caddy.BuildJSON(c)
	if err != nil {
		return err
	}

	if err := caddy.LoadConfig(c.Caddy.Admin, cfgJSON); err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	fixSudoOwnership(dir)
	last := filepath.Join(dir, "last-applied.json")
	if err := os.WriteFile(last, cfgJSON, 0o644); err == nil {
		fixSudoOwnership(last)
		fmt.Println("wrote:", last)
	}

	return nil
}
