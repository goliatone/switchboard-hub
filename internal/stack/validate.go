package stack

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

var hostnameLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

func (s *Stack) Validate() error {
	if s == nil {
		return fmt.Errorf("stack is nil")
	}
	if s.Version == 0 {
		return fmt.Errorf("version is required")
	}
	if s.Version != 1 {
		return fmt.Errorf("unsupported stack version %d (expected 1)", s.Version)
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if normalizeNameSegment(s.Name) == "" {
		return fmt.Errorf("invalid stack name %q", s.Name)
	}
	if len(s.Services) == 0 {
		return fmt.Errorf("services must contain at least one service")
	}

	seenNames := map[string]struct{}{}
	seenGenerated := map[string]struct{}{}
	seenLocalHosts := map[string]struct{}{}
	seenPublicHosts := map[string]struct{}{}
	for i, svc := range s.Services {
		if strings.TrimSpace(svc.Name) == "" {
			return fmt.Errorf("services[%d].name is required", i)
		}
		if normalizeNameSegment(svc.Name) == "" {
			return fmt.Errorf("services[%d].name is invalid: %q", i, svc.Name)
		}

		nameKey := normalizeLookupKey(svc.Name)
		if _, ok := seenNames[nameKey]; ok {
			return fmt.Errorf("duplicate service name %q", svc.Name)
		}
		seenNames[nameKey] = struct{}{}

		appName := GeneratedAppName(s.Name, svc.Name)
		if appName == "" {
			return fmt.Errorf("generated app name is invalid for service %q", svc.Name)
		}
		if len(appName) > 63 {
			return fmt.Errorf("generated app name %q is too long", appName)
		}
		if _, ok := seenGenerated[appName]; ok {
			return fmt.Errorf("generated app name collision for service %q (%s)", svc.Name, appName)
		}
		seenGenerated[appName] = struct{}{}

		if err := validatePort(svc.LocalPort); err != nil {
			return fmt.Errorf("services[%d].local_port: %w", i, err)
		}
		if strings.TrimSpace(svc.LocalHost) != "" {
			host, err := normalizeHostname(svc.LocalHost)
			if err != nil {
				return fmt.Errorf("services[%d].local_host: %w", i, err)
			}
			if _, ok := seenLocalHosts[host]; ok {
				return fmt.Errorf("duplicate local_host %q", host)
			}
			seenLocalHosts[host] = struct{}{}
		}
		if strings.TrimSpace(svc.PublicHost) != "" {
			host, err := normalizeHostname(svc.PublicHost)
			if err != nil {
				return fmt.Errorf("services[%d].public_host: %w", i, err)
			}
			if _, ok := seenPublicHosts[host]; ok {
				return fmt.Errorf("duplicate public_host %q", host)
			}
			seenPublicHosts[host] = struct{}{}
		}
	}

	for key := range s.Outputs {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("outputs contains an empty key")
		}
	}

	return nil
}

func validatePort(port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("invalid local_port %d (expected 1..65535)", port)
	}
	return nil
}

func normalizeHostname(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", fmt.Errorf("hostname is required")
	}
	s = strings.TrimSuffix(s, ".")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	if strings.Contains(s, "/") {
		return "", fmt.Errorf("hostname must not contain a path: %q", raw)
	}
	if strings.Contains(s, ":") {
		return "", fmt.Errorf("hostname must not contain a port: %q", raw)
	}
	if strings.Contains(s, " ") {
		return "", fmt.Errorf("hostname must not contain spaces: %q", raw)
	}

	host := strings.ToLower(s)
	if ip := net.ParseIP(host); ip != nil {
		return "", fmt.Errorf("hostname must not be an IP address: %q", raw)
	}
	if len(host) > 253 {
		return "", fmt.Errorf("hostname is too long: %q", raw)
	}
	labels := strings.SplitSeq(host, ".")
	for label := range labels {
		if label == "" {
			return "", fmt.Errorf("hostname contains an empty label: %q", raw)
		}
		if len(label) > 63 {
			return "", fmt.Errorf("hostname label too long: %q", raw)
		}
		if !hostnameLabelPattern.MatchString(label) {
			return "", fmt.Errorf("hostname label %q is invalid", label)
		}
	}
	return host, nil
}

func parentDomain(raw string) (string, error) {
	host, err := normalizeHostname(raw)
	if err != nil {
		return "", err
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("parent_domain requires at least two labels: %q", raw)
	}
	return strings.Join(labels[1:], "."), nil
}
