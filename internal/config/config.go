package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	TLD    string  `yaml:"tld" json:"tld"`
	DNS    DNS     `yaml:"dns" json:"dns"`
	Caddy  Caddy   `yaml:"caddy" json:"caddy"`
	Routes []Route `yaml:"routes" json:"routes"`
	Tunnel Tunnels `yaml:"tunnels,omitempty" json:"tunnels,omitempty"`
	Apps   []App   `yaml:"apps,omitempty" json:"apps,omitempty"`
}

type DNS struct {
	IP string `yaml:"ip" json:"ip"`
}

type Caddy struct {
	Admin  string   `yaml:"admin" json:"admin"`
	Listen []string `yaml:"listen" json:"listen"`
	TLS    CaddyTLS `yaml:"tls" json:"tls"`
}

type CaddyTLS struct {
	Enabled  bool     `yaml:"enabled" json:"enabled"`
	Mode     string   `yaml:"mode" json:"mode"`
	Listen   []string `yaml:"listen" json:"listen"`
	CertFile string   `yaml:"cert_file,omitempty" json:"cert_file,omitempty"`
	KeyFile  string   `yaml:"key_file,omitempty" json:"key_file,omitempty"`
}

type Route struct {
	Host string `yaml:"host" json:"host"`
	Dial string `yaml:"dial" json:"dial"`
}

type Tunnels struct {
	DefaultProvider string                       `yaml:"default_provider,omitempty" json:"default_provider,omitempty"`
	Providers       map[string]TunnelProviderCfg `yaml:"providers,omitempty" json:"providers,omitempty"`
}

type TunnelProviderCfg struct {
	Enabled   bool              `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	AccountID string            `yaml:"account_id,omitempty" json:"account_id,omitempty"`
	Zone      string            `yaml:"zone,omitempty" json:"zone,omitempty"`
	Values    map[string]string `yaml:"values,omitempty" json:"values,omitempty"`
}

type App struct {
	Name             string            `yaml:"name" json:"name"`
	LocalHost        string            `yaml:"local_host" json:"local_host"`
	LocalPort        int               `yaml:"local_port" json:"local_port"`
	DialHost         string            `yaml:"dial_host,omitempty" json:"dial_host,omitempty"`
	ResolvedDialHost string            `yaml:"resolved_dial_host,omitempty" json:"resolved_dial_host,omitempty"`
	PublicEndpoint   AppPublicEndpoint `yaml:"public_endpoint,omitempty" json:"public_endpoint,omitempty"`
	OAuth            AppOAuth          `yaml:"oauth,omitempty" json:"oauth,omitempty"`
	Metadata         map[string]string `yaml:"metadata,omitempty" json:"metadata,omitempty"`
}

type AppPublicEndpoint struct {
	Provider             string `yaml:"provider,omitempty" json:"provider,omitempty"`
	Host                 string `yaml:"host,omitempty" json:"host,omitempty"`
	EndpointID           string `yaml:"endpoint_id,omitempty" json:"endpoint_id,omitempty"`
	ActiveSessionID      string `yaml:"active_session_id,omitempty" json:"active_session_id,omitempty"`
	ActiveSessionPID     int    `yaml:"active_session_pid,omitempty" json:"active_session_pid,omitempty"`
	ActiveSessionStarted string `yaml:"active_session_started_at,omitempty" json:"active_session_started_at,omitempty"`
}

type AppOAuth struct {
	Google AppGoogleOAuth `yaml:"google,omitempty" json:"google,omitempty"`
}

type AppGoogleOAuth struct {
	Enabled      bool   `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	CallbackPath string `yaml:"callback_path,omitempty" json:"callback_path,omitempty"`
	RedirectURI  string `yaml:"redirect_uri,omitempty" json:"redirect_uri,omitempty"`
}

func Default(tld, dnsIP string) *Config {
	return &Config{
		TLD: tld,
		DNS: DNS{IP: dnsIP},
		Caddy: Caddy{
			Admin:  "http://127.0.0.1:2019",
			Listen: []string{":80"},
			TLS: CaddyTLS{
				Enabled:  true,
				Mode:     "internal",
				Listen:   []string{":443"},
				CertFile: "",
				KeyFile:  "",
			},
		},
		Routes: []Route{},
		Tunnel: defaultTunnels(),
		Apps:   []App{},
	}
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.TLD == "" {
		c.TLD = "test"
	}
	if c.DNS.IP == "" {
		c.DNS.IP = "10.0.0.1"
	}
	if c.Caddy.Admin == "" {
		c.Caddy.Admin = "http://127.0.0.1:2019"
	}
	if len(c.Caddy.Listen) == 0 {
		c.Caddy.Listen = []string{":80"}
	}
	if len(c.Caddy.TLS.Listen) == 0 {
		c.Caddy.TLS.Listen = []string{":443"}
	}
	if strings.TrimSpace(c.Caddy.TLS.Mode) == "" {
		c.Caddy.TLS.Mode = "internal"
	} else {
		c.Caddy.TLS.Mode = strings.ToLower(strings.TrimSpace(c.Caddy.TLS.Mode))
	}
	c.Caddy.TLS.CertFile = strings.TrimSpace(c.Caddy.TLS.CertFile)
	c.Caddy.TLS.KeyFile = strings.TrimSpace(c.Caddy.TLS.KeyFile)
	c.Tunnel = normalizeTunnels(c.Tunnel)
	c.Apps = normalizeApps(c.Apps)
	migrateRoutesToApps(&c)
	return &c, nil
}

func LoadOrDefault(path string) (*Config, error) {
	if _, err := os.Stat(path); err == nil {
		return Load(path)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return Default("test", "10.0.0.1"), nil
}

func LoadOrCreateDefault(path string) (*Config, error) {
	c, err := LoadOrDefault(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		return c, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := Save(path, c); err != nil {
		return nil, err
	}
	return c, nil
}

func Save(path string, c *Config) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	fixSudoOwnership(dir)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		if os.IsPermission(err) && os.Geteuid() != 0 {
			return fmt.Errorf("write config %s: %w (hint: sudo chown -R \"$USER\":staff %s)", path, err, dir)
		}
		return fmt.Errorf("write config %s: %w", path, err)
	}
	fixSudoOwnership(path)
	return nil
}

func fixSudoOwnership(path string) {
	if os.Geteuid() != 0 {
		return
	}
	uidStr := strings.TrimSpace(os.Getenv("SUDO_UID"))
	gidStr := strings.TrimSpace(os.Getenv("SUDO_GID"))
	if uidStr == "" || gidStr == "" {
		uidStr = strings.TrimSpace(os.Getenv("SWITCHD_CONFIG_OWNER_UID"))
		gidStr = strings.TrimSpace(os.Getenv("SWITCHD_CONFIG_OWNER_GID"))
	}
	if uidStr == "" || gidStr == "" {
		return
	}
	uid, err := strconv.Atoi(uidStr)
	if err != nil {
		return
	}
	gid, err := strconv.Atoi(gidStr)
	if err != nil {
		return
	}
	_ = os.Chown(path, uid, gid)
}

func defaultTunnels() Tunnels {
	return Tunnels{
		DefaultProvider: "cloudflare",
		Providers: map[string]TunnelProviderCfg{
			"cloudflare": {
				Enabled: false,
				Zone:    "",
				Values:  map[string]string{},
			},
			"tailscale": {
				Enabled: false,
				Values:  map[string]string{},
			},
		},
	}
}

func normalizeTunnels(t Tunnels) Tunnels {
	out := defaultTunnels()

	if strings.TrimSpace(t.DefaultProvider) != "" {
		out.DefaultProvider = strings.ToLower(strings.TrimSpace(t.DefaultProvider))
	}
	for name, cfg := range t.Providers {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		normalized := cfg
		if normalized.Values == nil {
			normalized.Values = map[string]string{}
		}
		normalized.Zone = strings.TrimSpace(normalized.Zone)
		normalized.AccountID = strings.TrimSpace(normalized.AccountID)
		out.Providers[key] = normalized
	}
	return out
}

func normalizeApps(apps []App) []App {
	if len(apps) == 0 {
		return []App{}
	}
	out := make([]App, 0, len(apps))
	for _, a := range apps {
		n := App{
			Name:             normalizeAppName(a.Name),
			LocalHost:        normalizeHost(a.LocalHost),
			LocalPort:        a.LocalPort,
			DialHost:         normalizeDialHost(a.DialHost),
			ResolvedDialHost: normalizeDialHost(a.ResolvedDialHost),
			PublicEndpoint: AppPublicEndpoint{
				Provider:             strings.ToLower(strings.TrimSpace(a.PublicEndpoint.Provider)),
				Host:                 normalizeHost(a.PublicEndpoint.Host),
				EndpointID:           strings.TrimSpace(a.PublicEndpoint.EndpointID),
				ActiveSessionID:      strings.TrimSpace(a.PublicEndpoint.ActiveSessionID),
				ActiveSessionPID:     a.PublicEndpoint.ActiveSessionPID,
				ActiveSessionStarted: strings.TrimSpace(a.PublicEndpoint.ActiveSessionStarted),
			},
			OAuth: AppOAuth{
				Google: AppGoogleOAuth{
					Enabled:      a.OAuth.Google.Enabled,
					CallbackPath: strings.TrimSpace(a.OAuth.Google.CallbackPath),
					RedirectURI:  strings.TrimSpace(a.OAuth.Google.RedirectURI),
				},
			},
			Metadata: map[string]string{},
		}
		for k, v := range a.Metadata {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			n.Metadata[k] = v
		}
		if n.DialHost != "" {
			n.ResolvedDialHost = ""
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func migrateRoutesToApps(c *Config) {
	if c == nil || len(c.Routes) == 0 {
		return
	}
	usedNames := map[string]struct{}{}
	usedHosts := map[string]struct{}{}
	for _, a := range c.Apps {
		if n := normalizeAppName(a.Name); n != "" {
			usedNames[n] = struct{}{}
		}
		if h := normalizeHost(a.LocalHost); h != "" {
			usedHosts[h] = struct{}{}
		}
	}

	for _, r := range c.Routes {
		host := normalizeHost(r.Host)
		if host == "" {
			continue
		}
		dialHost, port, ok := parseDialTarget(r.Dial)
		if !ok {
			continue
		}
		if _, exists := usedHosts[host]; exists {
			continue
		}
		name := deriveAppName(host, c.TLD)
		if name == "" {
			continue
		}
		base := name
		for i := 2; ; i++ {
			if _, taken := usedNames[name]; !taken {
				break
			}
			name = fmt.Sprintf("%s-%d", base, i)
		}
		c.Apps = append(c.Apps, App{
			Name:             name,
			LocalHost:        host,
			LocalPort:        port,
			ResolvedDialHost: dialHost,
			Metadata:         map[string]string{"source": "migrated-route"},
		})
		usedNames[name] = struct{}{}
		usedHosts[host] = struct{}{}
	}

	sort.Slice(c.Apps, func(i, j int) bool { return c.Apps[i].Name < c.Apps[j].Name })
}

func parseDialTarget(dial string) (string, int, bool) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(dial))
	if err != nil {
		return "", 0, false
	}
	p, err := strconv.Atoi(port)
	if err != nil || p <= 0 || p > 65535 {
		return "", 0, false
	}
	return normalizeDialHost(host), p, true
}

func parseDialPort(dial string) (int, bool) {
	_, port, ok := parseDialTarget(dial)
	return port, ok
}

func deriveAppName(host, tld string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	suffix := "." + strings.ToLower(strings.TrimSpace(tld))
	if suffix != "." && strings.HasSuffix(h, suffix) {
		h = strings.TrimSuffix(h, suffix)
	}
	if idx := strings.IndexByte(h, '.'); idx > 0 {
		h = h[:idx]
	}
	return normalizeAppName(h)
}

var appNameCleaner = regexp.MustCompile(`[^a-z0-9\-]+`)

func normalizeAppName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.ReplaceAll(n, "_", "-")
	n = appNameCleaner.ReplaceAllString(n, "-")
	n = strings.Trim(n, "-")
	return n
}

func normalizeHost(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.TrimPrefix(h, "http://")
	h = strings.TrimPrefix(h, "https://")
	h = strings.TrimSuffix(h, "/")
	return h
}

func normalizeDialHost(host string) string {
	h := normalizeHost(host)
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	return h
}
