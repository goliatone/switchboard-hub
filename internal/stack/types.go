package stack

import (
	"fmt"
	"strconv"
	"strings"
)

type Stack struct {
	Version  int               `yaml:"version" json:"version"`
	Name     string            `yaml:"name" json:"name"`
	Defaults Defaults          `yaml:"defaults,omitempty" json:"defaults,omitempty"`
	Services []Service         `yaml:"services" json:"services"`
	Outputs  map[string]string `yaml:"outputs,omitempty" json:"outputs,omitempty"`

	sourcePath string
}

type Defaults struct {
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	Expose   *bool  `yaml:"expose,omitempty" json:"expose,omitempty"`
	Up       *bool  `yaml:"up,omitempty" json:"up,omitempty"`
}

type Service struct {
	Name       string `yaml:"name" json:"name"`
	LocalHost  string `yaml:"local_host,omitempty" json:"local_host,omitempty"`
	LocalPort  int    `yaml:"local_port" json:"local_port"`
	PublicHost string `yaml:"public_host,omitempty" json:"public_host,omitempty"`
	Provider   string `yaml:"provider,omitempty" json:"provider,omitempty"`
	Expose     *bool  `yaml:"expose,omitempty" json:"expose,omitempty"`
	Up         *bool  `yaml:"up,omitempty" json:"up,omitempty"`
}

type ResolvedStack struct {
	Stack    Stack
	TLD      string
	Services []ResolvedService

	servicesByName map[string]ResolvedService
}

type ResolvedService struct {
	Name               string
	GeneratedAppName   string
	LocalHost          string
	LocalPort          int
	PublicHost         string
	Provider           string
	Expose             bool
	Up                 bool
	OriginalLocalHost  string
	OriginalPublicHost string
}

func GeneratedAppName(stackName, serviceName string) string {
	stackPart := normalizeNameSegment(stackName)
	servicePart := normalizeNameSegment(serviceName)
	if stackPart == "" || servicePart == "" {
		return ""
	}
	return stackPart + "-" + servicePart
}

func (s *ResolvedStack) ServiceValue(name, field string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("stack is nil")
	}
	svc, ok := s.lookupService(name)
	if !ok {
		return "", fmt.Errorf("service %q not found in stack %q", strings.TrimSpace(name), s.Stack.Name)
	}

	switch normalizeLookupKey(field) {
	case "name":
		return svc.Name, nil
	case "public_host":
		if strings.TrimSpace(svc.PublicHost) == "" {
			return "", fmt.Errorf("service %q does not define public_host", svc.Name)
		}
		return svc.PublicHost, nil
	case "local_host":
		return svc.LocalHost, nil
	case "local_port":
		return strconv.Itoa(svc.LocalPort), nil
	case "app_name", "generated_app_name", "generated_name":
		return svc.GeneratedAppName, nil
	default:
		return "", fmt.Errorf("unsupported service field %q", strings.TrimSpace(field))
	}
}

func (s *ResolvedStack) lookupService(name string) (ResolvedService, bool) {
	if s == nil {
		return ResolvedService{}, false
	}
	key := normalizeLookupKey(name)
	if key == "" {
		return ResolvedService{}, false
	}
	svc, ok := s.servicesByName[key]
	return svc, ok
}

func normalizeLookupKey(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func normalizeNameSegment(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(s))
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			lastDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}

	out := strings.Trim(b.String(), "-")
	out = strings.TrimSpace(out)
	out = strings.TrimSuffix(out, ".")
	if out == "" {
		return ""
	}
	if len(out) > 63 {
		return ""
	}
	return out
}

func boolOrDefault(v *bool, defaultValue bool) bool {
	if v == nil {
		return defaultValue
	}
	return *v
}

func cloneStrings(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
