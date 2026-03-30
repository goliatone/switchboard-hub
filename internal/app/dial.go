package app

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/goliatone/switchboard-hub/internal/config"
)

const defaultAppDialHost = "127.0.0.1"

var dialHostLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

func NormalizeDialHost(raw string) (string, error) {
	host := normalizeDialHost(raw)
	if host == "" {
		return "", nil
	}
	if strings.Contains(host, "/") {
		return "", fmt.Errorf("invalid dial host %q", raw)
	}
	if strings.Contains(host, " ") {
		return "", fmt.Errorf("invalid dial host %q", raw)
	}
	if ip := net.ParseIP(host); ip != nil {
		return host, nil
	}
	if strings.Contains(host, ":") {
		return "", fmt.Errorf("invalid dial host %q (ports are not allowed)", raw)
	}
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if label == "" || !dialHostLabelPattern.MatchString(label) {
			return "", fmt.Errorf("invalid dial host %q", raw)
		}
	}
	return host, nil
}

func ConfiguredDialHost(a config.App) string {
	host := normalizeDialHost(a.DialHost)
	if host != "" {
		return host
	}
	host = normalizeDialHost(a.ResolvedDialHost)
	if host != "" {
		return host
	}
	return defaultAppDialHost
}

func ResolveDialHost(a config.App) string {
	if host := normalizeDialHost(a.DialHost); host != "" {
		return host
	}
	if host, ok := DetectReachableDialHost(a.LocalPort); ok {
		return host
	}
	if host := normalizeDialHost(a.ResolvedDialHost); host != "" {
		return host
	}
	return defaultAppDialHost
}

func DialAddress(host string, port int) string {
	return net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(port))
}

func LocalURLForApp(a config.App, resolve bool) string {
	host := ConfiguredDialHost(a)
	if resolve {
		host = ResolveDialHost(a)
	}
	return "http://" + DialAddress(host, a.LocalPort)
}

func normalizeDialHost(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	return s
}

func DetectReachableDialHost(port int) (string, bool) {
	for _, candidate := range []string{defaultAppDialHost, "::1"} {
		if dialHostReachable(candidate, port) {
			return candidate, true
		}
	}
	return "", false
}

func refreshResolvedDialHost(a *config.App) bool {
	if a == nil {
		return false
	}
	if normalizeDialHost(a.DialHost) != "" {
		if a.ResolvedDialHost != "" {
			a.ResolvedDialHost = ""
			return true
		}
		return false
	}
	host, ok := DetectReachableDialHost(a.LocalPort)
	if !ok {
		return false
	}
	host = normalizeDialHost(host)
	if normalizeDialHost(a.ResolvedDialHost) == host {
		return false
	}
	a.ResolvedDialHost = host
	return true
}

func dialHostReachable(host string, port int) bool {
	conn, err := net.DialTimeout("tcp", DialAddress(host, port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
