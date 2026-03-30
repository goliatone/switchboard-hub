package switchboard

import (
	"context"
	"strings"
	"time"

	"github.com/goliatone/switchboard-hub/internal/app"
	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
	"github.com/goliatone/switchboard-hub/internal/tunnel/providers"
)

type Options struct {
	ConfigPath       string
	Store            ConfigStore
	ProviderRegistry ProviderRegistry
	ApplyFunc        func(path string, cfg Config) error
	Now              func() time.Time
}

type Client struct {
	service *app.Service
}

func New(opts Options) *Client {
	return &Client{
		service: app.NewService(app.ServiceOptions{
			ConfigPath:       strings.TrimSpace(opts.ConfigPath),
			Store:            newInternalStoreAdapter(opts.Store),
			ProviderRegistry: newInternalRegistryAdapter(opts.ProviderRegistry),
			ApplyConfig:      newInternalApplyAdapter(opts.ApplyFunc),
			Now:              opts.Now,
		}),
	}
}

func Default() *Client {
	return New(Options{})
}

func (c *Client) ConfigPath() (string, error) {
	return c.service.ConfigPath()
}

func (c *Client) LoadConfig() (Config, error) {
	_, cfg, err := c.service.LoadConfig()
	if err != nil {
		return Config{}, err
	}
	return fromInternalConfig(cfg), nil
}

func (c *Client) LoadOrDefaultConfig() (Config, error) {
	_, cfg, err := c.service.LoadOrDefaultConfig()
	if err != nil {
		return Config{}, err
	}
	return fromInternalConfig(cfg), nil
}

func (c *Client) LoadOrCreateDefaultConfig() (Config, error) {
	_, cfg, err := c.service.LoadOrCreateDefaultConfig()
	if err != nil {
		return Config{}, err
	}
	return fromInternalConfig(cfg), nil
}

func (c *Client) SaveConfig(cfg Config) error {
	return c.service.SaveConfig(toInternalConfig(cfg))
}

func (c *Client) CreateApp(nameOrHost string, port int, dialHost string) error {
	return c.service.CreateApp(nameOrHost, port, dialHost)
}

func (c *Client) RemoveApp(name string) error {
	return c.service.RemoveApp(name)
}

func (c *Client) ListApps() ([]App, error) {
	apps, err := c.service.ListApps()
	if err != nil {
		return nil, err
	}
	out := make([]App, 0, len(apps))
	for _, a := range apps {
		cfg := &config.Config{Apps: []config.App{a}}
		out = append(out, fromInternalConfig(cfg).Apps[0])
	}
	return out, nil
}

func (c *Client) ExposeApp(name, providerName, publicHost string) error {
	return c.service.ExposeApp(name, providerName, publicHost)
}

func (c *Client) AppUp(name string) error {
	return c.service.AppUp(name)
}

func (c *Client) AppDown(name string) error {
	return c.service.AppDown(name)
}

func (c *Client) TunnelProviders() []string {
	return c.service.TunnelProviders()
}

func (c *Client) TunnelInit(providerName string, opts TunnelInitOptions) error {
	return c.service.TunnelInitWithOptions(providerName, toInternalTunnelInitOptions(opts))
}

func (c *Client) TunnelStatus(providerName string) ([]TunnelProviderStatus, error) {
	statuses, err := c.service.TunnelStatus(providerName)
	if err != nil {
		return nil, err
	}
	return fromInternalProviderStatus(statuses), nil
}

func (c *Client) AppTunnelHealthStatus() ([]AppTunnelHealth, error) {
	statuses, err := c.service.AppTunnelHealthStatus()
	if err != nil {
		return nil, err
	}
	return fromInternalAppTunnelHealth(statuses), nil
}

type internalStoreAdapter struct {
	store ConfigStore
}

func newInternalStoreAdapter(store ConfigStore) app.ConfigStore {
	if store == nil {
		return nil
	}
	return internalStoreAdapter{store: store}
}

func (a internalStoreAdapter) Load(path string) (*config.Config, error) {
	cfg, err := a.store.Load(path)
	if err != nil {
		return nil, err
	}
	return toInternalConfig(cfg), nil
}

func (a internalStoreAdapter) LoadOrDefault(path string) (*config.Config, error) {
	cfg, err := a.store.LoadOrDefault(path)
	if err != nil {
		return nil, err
	}
	return toInternalConfig(cfg), nil
}

func (a internalStoreAdapter) LoadOrCreateDefault(path string) (*config.Config, error) {
	cfg, err := a.store.LoadOrCreateDefault(path)
	if err != nil {
		return nil, err
	}
	return toInternalConfig(cfg), nil
}

func (a internalStoreAdapter) Save(path string, cfg *config.Config) error {
	return a.store.Save(path, fromInternalConfig(cfg))
}

type internalRegistryAdapter struct {
	registry ProviderRegistry
}

func newInternalRegistryAdapter(registry ProviderRegistry) app.ProviderRegistry {
	if registry == nil {
		return providers.Registry()
	}
	return internalRegistryAdapter{registry: registry}
}

func (a internalRegistryAdapter) Providers() []string {
	return a.registry.Providers()
}

func (a internalRegistryAdapter) Resolve(name string) (tunnel.Provider, error) {
	provider, err := a.registry.Resolve(name)
	if err != nil {
		return nil, err
	}
	return publicProviderAdapter{provider: provider}, nil
}

type publicProviderAdapter struct {
	provider Provider
}

func (a publicProviderAdapter) Name() string {
	return a.provider.Name()
}

func (a publicProviderAdapter) Capabilities() tunnel.Capabilities {
	caps := a.provider.Capabilities()
	return tunnel.Capabilities{
		StableHostname:     caps.StableHostname,
		HTTPForwarding:     caps.HTTPForwarding,
		HTTPSForwarding:    caps.HTTPSForwarding,
		OAuthSuitable:      caps.OAuthSuitable,
		SupportsSSE:        caps.SupportsSSE,
		SupportsWebSockets: caps.SupportsWebSockets,
		Notes:              append([]string(nil), caps.Notes...),
	}
}

func (a publicProviderAdapter) Init(ctx context.Context, cfg tunnel.ProviderConfig) error {
	return a.provider.Init(ctx, ProviderConfig{Values: cfg.Values})
}

func (a publicProviderAdapter) EnsureEndpoint(ctx context.Context, req tunnel.EndpointRequest) (tunnel.Endpoint, error) {
	ep, err := a.provider.EnsureEndpoint(ctx, EndpointRequest{
		Name:       req.Name,
		PublicHost: req.PublicHost,
		LocalURL:   req.LocalURL,
		Metadata:   req.Metadata,
	})
	if err != nil {
		return tunnel.Endpoint{}, err
	}
	return tunnel.Endpoint{
		ID:       ep.ID,
		Provider: ep.Provider,
		Name:     ep.Name,
		Host:     ep.Host,
		Metadata: ep.Metadata,
	}, nil
}

func (a publicProviderAdapter) Start(ctx context.Context, req tunnel.StartRequest) (tunnel.Session, error) {
	session, err := a.provider.Start(ctx, StartRequest{
		Endpoint: Endpoint{
			ID:       req.Endpoint.ID,
			Provider: req.Endpoint.Provider,
			Name:     req.Endpoint.Name,
			Host:     req.Endpoint.Host,
			Metadata: req.Endpoint.Metadata,
		},
		LocalURL:   req.LocalURL,
		SessionEnv: req.SessionEnv,
	})
	if err != nil {
		return tunnel.Session{}, err
	}
	return tunnel.Session{
		ID:         session.ID,
		Provider:   session.Provider,
		EndpointID: session.EndpointID,
		PID:        session.PID,
		StartedAt:  session.StartedAt,
		Metadata:   session.Metadata,
	}, nil
}

func (a publicProviderAdapter) Stop(ctx context.Context, sessionID string) error {
	return a.provider.Stop(ctx, sessionID)
}

func (a publicProviderAdapter) RemoveEndpoint(ctx context.Context, endpointID string) error {
	return a.provider.RemoveEndpoint(ctx, endpointID)
}

func (a publicProviderAdapter) Status(ctx context.Context, endpointID string) (tunnel.EndpointStatus, error) {
	st, err := a.provider.Status(ctx, endpointID)
	if err != nil {
		return tunnel.EndpointStatus{}, err
	}
	return tunnel.EndpointStatus{
		Ready:     st.Ready,
		Endpoint:  tunnel.Endpoint{ID: st.Endpoint.ID, Provider: st.Endpoint.Provider, Name: st.Endpoint.Name, Host: st.Endpoint.Host, Metadata: st.Endpoint.Metadata},
		Message:   st.Message,
		SessionID: st.SessionID,
	}, nil
}

func newInternalApplyAdapter(fn func(path string, cfg Config) error) func(path string, cfg *config.Config) error {
	if fn == nil {
		return nil
	}
	return func(path string, cfg *config.Config) error {
		return fn(path, fromInternalConfig(cfg))
	}
}
