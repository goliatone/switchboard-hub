package app

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/goliatone/switchboard-hub/internal/config"
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

func TunnelProviders() []string {
	return providers.Registry().Providers()
}

func TunnelInit(providerName string) error {
	p, c, err := loadConfigWithPath()
	if err != nil {
		return err
	}
	providerName, err = resolveProviderName(c, providerName)
	if err != nil {
		return err
	}
	pr, err := providers.Registry().Resolve(providerName)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := pr.Init(ctx, tunnelProviderConfig(c, providerName)); err != nil {
		return err
	}

	enableProvider(c, providerName)
	if err := config.Save(p, c); err != nil {
		return err
	}
	fmt.Println("saved:", p)
	return nil
}

func TunnelStatus(providerName string) ([]TunnelProviderStatus, error) {
	_, c, err := loadConfigWithPath()
	if err != nil {
		return nil, err
	}
	reg := providers.Registry()
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
	host, err := normalizePublicHost(publicHost)
	if err != nil {
		return err
	}

	pr, err := providers.Registry().Resolve(providerName)
	if err != nil {
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
	enableProvider(c, providerName)

	if err := config.Save(p, c); err != nil {
		return err
	}
	fmt.Println("saved:", p)
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
	if strings.TrimSpace(a.PublicEndpoint.ActiveSessionID) != "" {
		return nil
	}

	pr, err := providers.Registry().Resolve(a.PublicEndpoint.Provider)
	if err != nil {
		return err
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
	if err := config.Save(p, c); err != nil {
		return err
	}
	fmt.Println("saved:", p)
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
	pr, err := providers.Registry().Resolve(providerName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := pr.Stop(ctx, sessionID); err != nil {
		return err
	}
	c.Apps[idx].PublicEndpoint.ActiveSessionID = ""
	if err := config.Save(p, c); err != nil {
		return err
	}
	fmt.Println("saved:", p)
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
	}
	return out
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
