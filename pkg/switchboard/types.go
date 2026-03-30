package switchboard

import (
	"context"
	"time"

	"github.com/goliatone/switchboard-hub/internal/app"
	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

type Config struct {
	TLD    string  `json:"tld" yaml:"tld"`
	DNS    DNS     `json:"dns" yaml:"dns"`
	Caddy  Caddy   `json:"caddy" yaml:"caddy"`
	Routes []Route `json:"routes" yaml:"routes"`
	Tunnel Tunnels `json:"tunnels,omitempty" yaml:"tunnels,omitempty"`
	Apps   []App   `json:"apps,omitempty" yaml:"apps,omitempty"`
}

type DNS struct {
	IP string `json:"ip" yaml:"ip"`
}

type Caddy struct {
	Admin  string   `json:"admin" yaml:"admin"`
	Listen []string `json:"listen" yaml:"listen"`
	TLS    CaddyTLS `json:"tls" yaml:"tls"`
}

type CaddyTLS struct {
	Enabled  bool     `json:"enabled" yaml:"enabled"`
	Mode     string   `json:"mode" yaml:"mode"`
	Listen   []string `json:"listen" yaml:"listen"`
	CertFile string   `json:"cert_file,omitempty" yaml:"cert_file,omitempty"`
	KeyFile  string   `json:"key_file,omitempty" yaml:"key_file,omitempty"`
}

type Route struct {
	Host string `json:"host" yaml:"host"`
	Dial string `json:"dial" yaml:"dial"`
}

type Tunnels struct {
	DefaultProvider string                          `json:"default_provider,omitempty" yaml:"default_provider,omitempty"`
	Providers       map[string]TunnelProviderConfig `json:"providers,omitempty" yaml:"providers,omitempty"`
}

type TunnelProviderConfig struct {
	Enabled   bool              `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	AccountID string            `json:"account_id,omitempty" yaml:"account_id,omitempty"`
	Zone      string            `json:"zone,omitempty" yaml:"zone,omitempty"`
	Values    map[string]string `json:"values,omitempty" yaml:"values,omitempty"`
}

type App struct {
	Name           string            `json:"name" yaml:"name"`
	LocalHost      string            `json:"local_host" yaml:"local_host"`
	LocalPort      int               `json:"local_port" yaml:"local_port"`
	DialHost       string            `json:"dial_host,omitempty" yaml:"dial_host,omitempty"`
	PublicEndpoint AppPublicEndpoint `json:"public_endpoint,omitempty" yaml:"public_endpoint,omitempty"`
	OAuth          AppOAuth          `json:"oauth,omitempty" yaml:"oauth,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

type CreateAppOptions struct {
	DialHost string `json:"dial_host,omitempty" yaml:"dial_host,omitempty"`
}

type AppTunnelHealth struct {
	AppName      string `json:"app_name"`
	Provider     string `json:"provider"`
	EndpointHost string `json:"endpoint_host"`
	EndpointID   string `json:"endpoint_id"`
	SessionID    string `json:"session_id"`
	SessionPID   int    `json:"session_pid"`
	StartedAt    string `json:"started_at"`
	Ready        bool   `json:"ready"`
	Message      string `json:"message"`
	Err          string `json:"error,omitempty"`
}

type AppPublicEndpoint struct {
	Provider             string `json:"provider,omitempty" yaml:"provider,omitempty"`
	Host                 string `json:"host,omitempty" yaml:"host,omitempty"`
	EndpointID           string `json:"endpoint_id,omitempty" yaml:"endpoint_id,omitempty"`
	ActiveSessionID      string `json:"active_session_id,omitempty" yaml:"active_session_id,omitempty"`
	ActiveSessionPID     int    `json:"active_session_pid,omitempty" yaml:"active_session_pid,omitempty"`
	ActiveSessionStarted string `json:"active_session_started_at,omitempty" yaml:"active_session_started_at,omitempty"`
}

type AppOAuth struct {
	Google AppGoogleOAuth `json:"google,omitempty" yaml:"google,omitempty"`
}

type AppGoogleOAuth struct {
	Enabled      bool   `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	CallbackPath string `json:"callback_path,omitempty" yaml:"callback_path,omitempty"`
	RedirectURI  string `json:"redirect_uri,omitempty" yaml:"redirect_uri,omitempty"`
}

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

type TunnelProviderStatus struct {
	Name          string   `json:"name"`
	Enabled       bool     `json:"enabled"`
	Available     bool     `json:"available"`
	OAuthSuitable bool     `json:"oauth_suitable"`
	Notes         []string `json:"notes,omitempty"`
	Err           string   `json:"error,omitempty"`
}

type Provider interface {
	Name() string
	Capabilities() Capabilities
	Init(ctx context.Context, cfg ProviderConfig) error
	EnsureEndpoint(ctx context.Context, req EndpointRequest) (Endpoint, error)
	Start(ctx context.Context, req StartRequest) (Session, error)
	Stop(ctx context.Context, sessionID string) error
	RemoveEndpoint(ctx context.Context, endpointID string) error
	Status(ctx context.Context, endpointID string) (EndpointStatus, error)
}

type ProviderRegistry interface {
	Providers() []string
	Resolve(name string) (Provider, error)
}

type ProviderConfig struct {
	Values map[string]string
}

type EndpointRequest struct {
	Name       string
	PublicHost string
	LocalURL   string
	Metadata   map[string]string
}

type Endpoint struct {
	ID       string
	Provider string
	Name     string
	Host     string
	Metadata map[string]string
}

type StartRequest struct {
	Endpoint   Endpoint
	LocalURL   string
	SessionEnv map[string]string
}

type Session struct {
	ID         string
	Provider   string
	EndpointID string
	PID        int
	StartedAt  time.Time
	Metadata   map[string]string
}

type EndpointStatus struct {
	Ready     bool
	Endpoint  Endpoint
	Message   string
	SessionID string
}

type Capabilities struct {
	StableHostname     bool
	HTTPForwarding     bool
	HTTPSForwarding    bool
	OAuthSuitable      bool
	SupportsSSE        bool
	SupportsWebSockets bool
	Notes              []string
}

type ConfigStore interface {
	Load(path string) (Config, error)
	LoadOrDefault(path string) (Config, error)
	LoadOrCreateDefault(path string) (Config, error)
	Save(path string, cfg Config) error
}

func fromInternalConfig(c *config.Config) Config {
	if c == nil {
		return Config{}
	}
	out := Config{
		TLD: c.TLD,
		DNS: DNS{IP: c.DNS.IP},
		Caddy: Caddy{
			Admin:  c.Caddy.Admin,
			Listen: append([]string(nil), c.Caddy.Listen...),
			TLS: CaddyTLS{
				Enabled:  c.Caddy.TLS.Enabled,
				Mode:     c.Caddy.TLS.Mode,
				Listen:   append([]string(nil), c.Caddy.TLS.Listen...),
				CertFile: c.Caddy.TLS.CertFile,
				KeyFile:  c.Caddy.TLS.KeyFile,
			},
		},
		Routes: make([]Route, 0, len(c.Routes)),
		Tunnel: Tunnels{
			DefaultProvider: c.Tunnel.DefaultProvider,
			Providers:       map[string]TunnelProviderConfig{},
		},
		Apps: make([]App, 0, len(c.Apps)),
	}
	for _, r := range c.Routes {
		out.Routes = append(out.Routes, Route{Host: r.Host, Dial: r.Dial})
	}
	for name, cfg := range c.Tunnel.Providers {
		values := map[string]string{}
		for k, v := range cfg.Values {
			values[k] = v
		}
		out.Tunnel.Providers[name] = TunnelProviderConfig{
			Enabled:   cfg.Enabled,
			AccountID: cfg.AccountID,
			Zone:      cfg.Zone,
			Values:    values,
		}
	}
	for _, a := range c.Apps {
		metadata := map[string]string{}
		for k, v := range a.Metadata {
			metadata[k] = v
		}
		out.Apps = append(out.Apps, App{
			Name:      a.Name,
			LocalHost: a.LocalHost,
			LocalPort: a.LocalPort,
			DialHost:  a.DialHost,
			PublicEndpoint: AppPublicEndpoint{
				Provider:             a.PublicEndpoint.Provider,
				Host:                 a.PublicEndpoint.Host,
				EndpointID:           a.PublicEndpoint.EndpointID,
				ActiveSessionID:      a.PublicEndpoint.ActiveSessionID,
				ActiveSessionPID:     a.PublicEndpoint.ActiveSessionPID,
				ActiveSessionStarted: a.PublicEndpoint.ActiveSessionStarted,
			},
			OAuth: AppOAuth{
				Google: AppGoogleOAuth{
					Enabled:      a.OAuth.Google.Enabled,
					CallbackPath: a.OAuth.Google.CallbackPath,
					RedirectURI:  a.OAuth.Google.RedirectURI,
				},
			},
			Metadata: metadata,
		})
	}
	return out
}

func toInternalConfig(c Config) *config.Config {
	out := &config.Config{
		TLD: c.TLD,
		DNS: config.DNS{IP: c.DNS.IP},
		Caddy: config.Caddy{
			Admin:  c.Caddy.Admin,
			Listen: append([]string(nil), c.Caddy.Listen...),
			TLS: config.CaddyTLS{
				Enabled:  c.Caddy.TLS.Enabled,
				Mode:     c.Caddy.TLS.Mode,
				Listen:   append([]string(nil), c.Caddy.TLS.Listen...),
				CertFile: c.Caddy.TLS.CertFile,
				KeyFile:  c.Caddy.TLS.KeyFile,
			},
		},
		Routes: make([]config.Route, 0, len(c.Routes)),
		Tunnel: config.Tunnels{
			DefaultProvider: c.Tunnel.DefaultProvider,
			Providers:       map[string]config.TunnelProviderCfg{},
		},
		Apps: make([]config.App, 0, len(c.Apps)),
	}
	for _, r := range c.Routes {
		out.Routes = append(out.Routes, config.Route{Host: r.Host, Dial: r.Dial})
	}
	for name, cfg := range c.Tunnel.Providers {
		values := map[string]string{}
		for k, v := range cfg.Values {
			values[k] = v
		}
		out.Tunnel.Providers[name] = config.TunnelProviderCfg{
			Enabled:   cfg.Enabled,
			AccountID: cfg.AccountID,
			Zone:      cfg.Zone,
			Values:    values,
		}
	}
	for _, a := range c.Apps {
		metadata := map[string]string{}
		for k, v := range a.Metadata {
			metadata[k] = v
		}
		out.Apps = append(out.Apps, config.App{
			Name:      a.Name,
			LocalHost: a.LocalHost,
			LocalPort: a.LocalPort,
			DialHost:  a.DialHost,
			PublicEndpoint: config.AppPublicEndpoint{
				Provider:             a.PublicEndpoint.Provider,
				Host:                 a.PublicEndpoint.Host,
				EndpointID:           a.PublicEndpoint.EndpointID,
				ActiveSessionID:      a.PublicEndpoint.ActiveSessionID,
				ActiveSessionPID:     a.PublicEndpoint.ActiveSessionPID,
				ActiveSessionStarted: a.PublicEndpoint.ActiveSessionStarted,
			},
			OAuth: config.AppOAuth{
				Google: config.AppGoogleOAuth{
					Enabled:      a.OAuth.Google.Enabled,
					CallbackPath: a.OAuth.Google.CallbackPath,
					RedirectURI:  a.OAuth.Google.RedirectURI,
				},
			},
			Metadata: metadata,
		})
	}
	return out
}

func toInternalTunnelInitOptions(opts TunnelInitOptions) app.TunnelInitOptions {
	return app.TunnelInitOptions{
		Setup:          opts.Setup,
		NonInteractive: opts.NonInteractive,
		OriginCert:     opts.OriginCert,
		Mode:           opts.Mode,
		AccountID:      opts.AccountID,
		ZoneID:         opts.ZoneID,
		BaseDomain:     opts.BaseDomain,
		APITokenEnv:    opts.APITokenEnv,
	}
}

func fromInternalProviderStatus(st []app.TunnelProviderStatus) []TunnelProviderStatus {
	out := make([]TunnelProviderStatus, 0, len(st))
	for _, item := range st {
		out = append(out, TunnelProviderStatus{
			Name:          item.Name,
			Enabled:       item.Enabled,
			Available:     item.Available,
			OAuthSuitable: item.OAuthSuitable,
			Notes:         append([]string(nil), item.Notes...),
			Err:           item.Err,
		})
	}
	return out
}

func fromInternalAppTunnelHealth(st []app.AppTunnelHealth) []AppTunnelHealth {
	out := make([]AppTunnelHealth, 0, len(st))
	for _, item := range st {
		out = append(out, AppTunnelHealth{
			AppName:      item.AppName,
			Provider:     item.Provider,
			EndpointHost: item.EndpointHost,
			EndpointID:   item.EndpointID,
			SessionID:    item.SessionID,
			SessionPID:   item.SessionPID,
			StartedAt:    item.StartedAt,
			Ready:        item.Ready,
			Message:      item.Message,
			Err:          item.Err,
		})
	}
	return out
}

func toInternalProviderConfig(cfg ProviderConfig) tunnel.ProviderConfig {
	values := map[string]string{}
	for k, v := range cfg.Values {
		values[k] = v
	}
	return tunnel.ProviderConfig{Values: values}
}

func fromInternalCapabilities(c tunnel.Capabilities) Capabilities {
	return Capabilities{
		StableHostname:     c.StableHostname,
		HTTPForwarding:     c.HTTPForwarding,
		HTTPSForwarding:    c.HTTPSForwarding,
		OAuthSuitable:      c.OAuthSuitable,
		SupportsSSE:        c.SupportsSSE,
		SupportsWebSockets: c.SupportsWebSockets,
		Notes:              append([]string(nil), c.Notes...),
	}
}

func toInternalEndpointRequest(req EndpointRequest) tunnel.EndpointRequest {
	metadata := map[string]string{}
	for k, v := range req.Metadata {
		metadata[k] = v
	}
	return tunnel.EndpointRequest{
		Name:       req.Name,
		PublicHost: req.PublicHost,
		LocalURL:   req.LocalURL,
		Metadata:   metadata,
	}
}

func fromInternalEndpoint(ep tunnel.Endpoint) Endpoint {
	metadata := map[string]string{}
	for k, v := range ep.Metadata {
		metadata[k] = v
	}
	return Endpoint{
		ID:       ep.ID,
		Provider: ep.Provider,
		Name:     ep.Name,
		Host:     ep.Host,
		Metadata: metadata,
	}
}

func toInternalStartRequest(req StartRequest) tunnel.StartRequest {
	sessionEnv := map[string]string{}
	for k, v := range req.SessionEnv {
		sessionEnv[k] = v
	}
	return tunnel.StartRequest{
		Endpoint: tunnel.Endpoint{
			ID:       req.Endpoint.ID,
			Provider: req.Endpoint.Provider,
			Name:     req.Endpoint.Name,
			Host:     req.Endpoint.Host,
			Metadata: req.Endpoint.Metadata,
		},
		LocalURL:   req.LocalURL,
		SessionEnv: sessionEnv,
	}
}

func fromInternalSession(s tunnel.Session) Session {
	metadata := map[string]string{}
	for k, v := range s.Metadata {
		metadata[k] = v
	}
	return Session{
		ID:         s.ID,
		Provider:   s.Provider,
		EndpointID: s.EndpointID,
		PID:        s.PID,
		StartedAt:  s.StartedAt,
		Metadata:   metadata,
	}
}

func fromInternalEndpointStatus(st tunnel.EndpointStatus) EndpointStatus {
	return EndpointStatus{
		Ready:     st.Ready,
		Endpoint:  fromInternalEndpoint(st.Endpoint),
		Message:   st.Message,
		SessionID: st.SessionID,
	}
}
