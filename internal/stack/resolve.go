package stack

import (
	"fmt"
	"sort"
	"strings"
)

func (s *Stack) Resolve(tld string) (*ResolvedStack, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}

	normalizedTLD := strings.TrimSpace(tld)
	if normalizedTLD != "" {
		var err error
		normalizedTLD, err = normalizeHostname(normalizedTLD)
		if err != nil {
			return nil, fmt.Errorf("invalid tld %q: %w", tld, err)
		}
	}

	needsDefaultHost := false
	for _, svc := range s.Services {
		if strings.TrimSpace(svc.LocalHost) == "" {
			needsDefaultHost = true
			break
		}
	}
	if needsDefaultHost && normalizedTLD == "" {
		return nil, fmt.Errorf("tld is required to derive default local_host values")
	}

	resolved := &ResolvedStack{
		Stack: Stack{
			Version:  s.Version,
			Name:     s.Name,
			Defaults: s.Defaults,
			Outputs:  cloneStrings(s.Outputs),
		},
		TLD:            normalizedTLD,
		Services:       make([]ResolvedService, 0, len(s.Services)),
		servicesByName: map[string]ResolvedService{},
	}
	seenLocalHosts := map[string]string{}
	seenPublicHosts := map[string]string{}
	resolved.Stack.Services = make([]Service, len(s.Services))
	copy(resolved.Stack.Services, s.Services)
	resolved.Stack.sourcePath = s.sourcePath

	for _, svc := range s.Services {
		rs := ResolvedService{
			Name:             svc.Name,
			LocalPort:        svc.LocalPort,
			Provider:         strings.TrimSpace(svc.Provider),
			Expose:           boolOrDefault(svc.Expose, boolOrDefault(s.Defaults.Expose, false)),
			Up:               boolOrDefault(svc.Up, boolOrDefault(s.Defaults.Up, false)),
			PublicHost:       strings.TrimSpace(svc.PublicHost),
			GeneratedAppName: GeneratedAppName(s.Name, svc.Name),
		}
		rs.OriginalLocalHost = strings.TrimSpace(svc.LocalHost)
		rs.OriginalPublicHost = strings.TrimSpace(svc.PublicHost)

		if rs.GeneratedAppName == "" {
			return nil, fmt.Errorf("generated app name is invalid for service %q", svc.Name)
		}

		if strings.TrimSpace(svc.LocalHost) != "" {
			host, err := normalizeHostname(svc.LocalHost)
			if err != nil {
				return nil, fmt.Errorf("service %q local_host: %w", svc.Name, err)
			}
			rs.LocalHost = host
		} else {
			host, err := normalizeHostname(rs.GeneratedAppName + "." + normalizedTLD)
			if err != nil {
				return nil, fmt.Errorf("service %q local_host: %w", svc.Name, err)
			}
			rs.LocalHost = host
		}

		if strings.TrimSpace(svc.PublicHost) != "" {
			host, err := normalizeHostname(svc.PublicHost)
			if err != nil {
				return nil, fmt.Errorf("service %q public_host: %w", svc.Name, err)
			}
			rs.PublicHost = host
		}
		if rs.Provider == "" {
			rs.Provider = strings.TrimSpace(s.Defaults.Provider)
		}
		if previous, ok := seenLocalHosts[normalizeLookupKey(rs.LocalHost)]; ok {
			return nil, fmt.Errorf("duplicate local_host %q for services %q and %q", rs.LocalHost, previous, svc.Name)
		}
		seenLocalHosts[normalizeLookupKey(rs.LocalHost)] = svc.Name
		if rs.PublicHost != "" {
			if previous, ok := seenPublicHosts[normalizeLookupKey(rs.PublicHost)]; ok {
				return nil, fmt.Errorf("duplicate public_host %q for services %q and %q", rs.PublicHost, previous, svc.Name)
			}
			seenPublicHosts[normalizeLookupKey(rs.PublicHost)] = svc.Name
		}

		key := normalizeLookupKey(svc.Name)
		resolved.Services = append(resolved.Services, rs)
		resolved.servicesByName[key] = rs
	}

	return resolved, nil
}

func (r *ResolvedStack) RenderEnvLines() ([]string, error) {
	outputs, err := r.RenderOutputs()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(outputs))
	for k := range outputs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+outputs[k])
	}
	return lines, nil
}
