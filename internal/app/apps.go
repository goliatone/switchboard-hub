package app

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"

	"github.com/goliatone/switchboard-hub/internal/config"
)

type CreateAppOptions struct {
	DialHost string
}

func CreateApp(nameOrHost string, port int, opts *CreateAppOptions) error {
	return DefaultService().CreateApp(nameOrHost, port, opts)
}

func RemoveApp(name string) error {
	return DefaultService().RemoveApp(name)
}

func ListApps() ([]config.App, error) {
	return DefaultService().ListApps()
}

func upsertApp(c *config.Config, nameOrHost string, port int, opts *CreateAppOptions) (config.App, error) {
	if c == nil {
		return config.App{}, errors.New("config is nil")
	}
	if err := validateAppPort(port); err != nil {
		return config.App{}, err
	}
	host, err := normalizeAppHost(nameOrHost, c.TLD)
	if err != nil {
		return config.App{}, err
	}
	name, err := normalizeAppNameFromInput(nameOrHost, host, c.TLD)
	if err != nil {
		return config.App{}, err
	}
	normalizedDialHost, err := NormalizeDialHost(createAppDialHost(opts))
	if err != nil {
		return config.App{}, err
	}

	if i := findAppByName(c, name); i >= 0 {
		return config.App{}, fmt.Errorf("app name already exists: %s", name)
	}
	if i := findAppByHost(c, host); i >= 0 {
		return config.App{}, fmt.Errorf("app host already exists: %s", host)
	}

	app := config.App{
		Name:      name,
		LocalHost: host,
		LocalPort: port,
		DialHost:  normalizedDialHost,
		Metadata:  map[string]string{},
	}
	if normalizedDialHost == "" {
		if resolvedDialHost, ok := DetectReachableDialHost(port); ok {
			app.ResolvedDialHost = resolvedDialHost
		}
	}
	c.Apps = append(c.Apps, app)
	sort.Slice(c.Apps, func(i, j int) bool { return c.Apps[i].Name < c.Apps[j].Name })
	upsertLegacyRoute(c, host, port, ConfiguredDialHost(app))
	return app, nil
}

func syncAppFromRoute(c *config.Config, host string, port int, dialHost string) error {
	if c == nil {
		return errors.New("config is nil")
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return errors.New("route host is required")
	}
	if err := validateAppPort(port); err != nil {
		return err
	}
	normalizedDialHost, err := NormalizeDialHost(dialHost)
	if err != nil {
		return err
	}
	if i := findAppByHost(c, host); i >= 0 {
		c.Apps[i].LocalPort = port
		c.Apps[i].DialHost = normalizedDialHost
		if normalizedDialHost != "" {
			c.Apps[i].ResolvedDialHost = ""
		} else {
			c.Apps[i].ResolvedDialHost = resolveRouteDialHost(normalizedDialHost, port)
		}
		sort.Slice(c.Apps, func(i, j int) bool { return c.Apps[i].Name < c.Apps[j].Name })
		return nil
	}
	name, err := normalizeAppNameFromInput(host, host, c.TLD)
	if err != nil {
		return err
	}
	base := name
	for i := 2; ; i++ {
		if findAppByName(c, name) < 0 {
			break
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}

	c.Apps = append(c.Apps, config.App{
		Name:      name,
		LocalHost: host,
		LocalPort: port,
		DialHost:  normalizedDialHost,
		Metadata: map[string]string{
			"source": "legacy-route",
		},
	})
	last := len(c.Apps) - 1
	if normalizedDialHost == "" {
		c.Apps[last].ResolvedDialHost = resolveRouteDialHost(normalizedDialHost, port)
	}
	sort.Slice(c.Apps, func(i, j int) bool { return c.Apps[i].Name < c.Apps[j].Name })
	return nil
}

func findAppByName(c *config.Config, name string) int {
	if c == nil {
		return -1
	}
	name = strings.ToLower(strings.TrimSpace(name))
	for i, a := range c.Apps {
		if strings.EqualFold(strings.TrimSpace(a.Name), name) {
			return i
		}
	}
	return -1
}

func findAppByHost(c *config.Config, host string) int {
	if c == nil {
		return -1
	}
	host = strings.ToLower(strings.TrimSpace(host))
	for i, a := range c.Apps {
		if strings.EqualFold(strings.TrimSpace(a.LocalHost), host) {
			return i
		}
	}
	return -1
}

func validateAppPort(port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("invalid app port %d (expected 1..65535)", port)
	}
	return nil
}

var appNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9\-]*[a-z0-9])?$`)

func normalizeAppNameInput(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimSuffix(s, ".")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	if idx := strings.IndexByte(s, '.'); idx > 0 {
		s = s[:idx]
	}
	s = strings.ReplaceAll(s, "_", "-")
	if strings.Contains(s, " ") {
		return "", fmt.Errorf("invalid app name %q", raw)
	}
	if !appNamePattern.MatchString(s) {
		return "", fmt.Errorf("invalid app name %q (allowed: lowercase letters, numbers, hyphen)", raw)
	}
	return s, nil
}

func normalizeAppNameFromInput(nameOrHost, host, tld string) (string, error) {
	raw := strings.TrimSpace(nameOrHost)
	if strings.Contains(raw, ".") {
		suffix := "." + strings.ToLower(strings.TrimSpace(tld))
		h := strings.ToLower(strings.TrimSpace(host))
		if suffix != "." && strings.HasSuffix(h, suffix) {
			h = strings.TrimSuffix(h, suffix)
		}
		if idx := strings.IndexByte(h, '.'); idx > 0 {
			h = h[:idx]
		}
		return normalizeAppNameInput(h)
	}
	return normalizeAppNameInput(raw)
}

func normalizeAppHost(nameOrHost, tld string) (string, error) {
	host := normalizeHost(nameOrHost, tld)
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return "", errors.New("app host is required")
	}
	if strings.Contains(host, "/") {
		return "", fmt.Errorf("invalid app host %q", nameOrHost)
	}
	if strings.Contains(host, " ") {
		return "", fmt.Errorf("invalid app host %q", nameOrHost)
	}
	if strings.Contains(host, ":") {
		return "", fmt.Errorf("invalid app host %q (ports are not allowed)", nameOrHost)
	}
	return host, nil
}

func upsertLegacyRoute(c *config.Config, host string, port int, dialHost string) bool {
	if c == nil {
		return false
	}
	dial := DialAddress(dialHost, port)
	for i := range c.Routes {
		if strings.EqualFold(c.Routes[i].Host, host) {
			if c.Routes[i].Dial == dial {
				return false
			}
			c.Routes[i].Dial = dial
			sort.Slice(c.Routes, func(i, j int) bool { return c.Routes[i].Host < c.Routes[j].Host })
			return true
		}
	}
	c.Routes = append(c.Routes, config.Route{Host: host, Dial: dial})
	sort.Slice(c.Routes, func(i, j int) bool { return c.Routes[i].Host < c.Routes[j].Host })
	return true
}

func syncRoutesFromApps(c *config.Config) bool {
	if c == nil {
		return false
	}
	changed := false
	for _, a := range c.Apps {
		dialHost := ConfiguredDialHost(a)
		if upsertLegacyRoute(c, a.LocalHost, a.LocalPort, dialHost) {
			changed = true
		}
	}
	return changed
}

func dialHostFromRouteDial(dial string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(dial))
	if err != nil {
		return ""
	}
	host, err = NormalizeDialHost(host)
	if err != nil {
		return ""
	}
	return host
}

func createAppDialHost(opts *CreateAppOptions) string {
	if opts == nil {
		return ""
	}
	return opts.DialHost
}

func resolveRouteDialHost(explicitDialHost string, port int) string {
	if explicitDialHost != "" {
		return ""
	}
	if host, ok := DetectReachableDialHost(port); ok {
		return host
	}
	return ""
}

func removeLegacyRouteByHost(c *config.Config, host string) {
	if c == nil {
		return
	}
	out := make([]config.Route, 0, len(c.Routes))
	for _, r := range c.Routes {
		if strings.EqualFold(strings.TrimSpace(r.Host), strings.TrimSpace(host)) {
			continue
		}
		out = append(out, r)
	}
	c.Routes = out
}

func removeAppByHost(c *config.Config, host string) {
	if c == nil {
		return
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return
	}
	out := make([]config.App, 0, len(c.Apps))
	for _, a := range c.Apps {
		if strings.EqualFold(strings.TrimSpace(a.LocalHost), host) {
			continue
		}
		out = append(out, a)
	}
	c.Apps = out
}
