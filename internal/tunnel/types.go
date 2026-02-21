package tunnel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

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
	StableHostname  bool
	HTTPForwarding  bool
	HTTPSForwarding bool
	OAuthSuitable   bool

	SupportsSSE        bool
	SupportsWebSockets bool

	Notes []string
}

func (c Capabilities) Validate() error {
	if !c.HTTPForwarding && !c.HTTPSForwarding {
		return errors.New("provider must support HTTP or HTTPS forwarding")
	}
	if c.OAuthSuitable && !c.StableHostname {
		return errors.New("oauth-suitable provider must support stable hostnames")
	}
	return nil
}

func (c Capabilities) ValidateOAuthUse(host string) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return errors.New("public host is required for oauth flow")
	}
	if !c.OAuthSuitable {
		return errors.New("provider is not marked oauth suitable")
	}
	if !c.StableHostname {
		return errors.New("oauth flow requires stable callback hostname")
	}
	if !c.HTTPSForwarding {
		return fmt.Errorf("oauth flow requires https-capable provider for host %q", host)
	}
	return nil
}

type CommandRunner func(ctx context.Context, name string, args ...string) (string, error)
