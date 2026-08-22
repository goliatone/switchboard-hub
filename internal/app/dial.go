package app

import (
	"context"
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
	labels := strings.SplitSeq(host, ".")
	for label := range labels {
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
	return ResolveDialHostContext(context.Background(), a)
}

func ResolveDialHostContext(ctx context.Context, a config.App) string {
	if host := normalizeDialHost(a.DialHost); host != "" {
		return host
	}
	if host, ok := DetectReachableDialHostContext(ctx, a.LocalPort); ok {
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
	return LocalURLForAppContext(context.Background(), a, resolve)
}

func LocalURLForAppContext(ctx context.Context, a config.App, resolve bool) string {
	host := ConfiguredDialHost(a)
	if resolve {
		host = ResolveDialHostContext(ctx, a)
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
	return DetectReachableDialHostContext(context.Background(), port)
}

func DetectReachableDialHostContext(ctx context.Context, port int) (string, bool) {
	for _, candidate := range []string{defaultAppDialHost, "::1"} {
		if dialHostReachable(ctx, candidate, port) {
			return candidate, true
		}
	}
	return "", false
}

func refreshResolvedDialHost(a *config.App) bool {
	return refreshResolvedDialHostContext(context.Background(), a)
}

func refreshResolvedDialHostContext(ctx context.Context, a *config.App) bool {
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
	host, ok := DetectReachableDialHostContext(ctx, a.LocalPort)
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

func dialHostReachable(ctx context.Context, host string, port int) bool {
	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	conn, err := dialer.DialContext(ctx, "tcp", DialAddress(host, port))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
