// Package validation defines the explicit setup for the opt-in real-provider
// ingress harness. It performs no provider discovery or mutation on import.
package validation

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	envName  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

type Config struct {
	Provider           string            `json:"provider"`
	RunDir             string            `json:"run_dir"`
	HelperPath         string            `json:"helper_path"`
	ProbePath          string            `json:"probe_path"`
	ProbeNetwork       string            `json:"probe_network"`
	CredentialEnv      string            `json:"credential_env,omitempty"`
	HostA              string            `json:"host_a"`
	HostB              string            `json:"host_b"`
	PortA              int               `json:"port_a"`
	PortB              int               `json:"port_b"`
	DedicatedResources bool              `json:"dedicated_resources"`
	Values             map[string]string `json:"values"`
}

func (c Config) Validate() error {
	if !c.DedicatedResources {
		return errors.New("dedicated test resources must be explicitly selected")
	}
	if c.Provider != "cloudflare" && c.Provider != "tailscale" {
		return errors.New("select cloudflare or tailscale")
	}
	for _, p := range []string{c.RunDir, c.HelperPath, c.ProbePath, c.Values["coordination_dir"]} {
		if !filepath.IsAbs(p) {
			return errors.New("absolute run/helper/probe/coordination paths required")
		}
	}
	if strings.TrimSpace(c.ProbeNetwork) == "" {
		return errors.New("explicit external probe network required")
	}
	if !dnsHostname(c.HostA) || !dnsHostname(c.HostB) {
		return errors.New("host_a and host_b require lowercase fully qualified DNS names without a trailing dot")
	}
	if err := c.validateProvider(); err != nil {
		return err
	}

	if !executable(c.HelperPath) || !executable(c.ProbePath) {
		return errors.New("helper_path and probe_path must point to installed executables")
	}
	if c.Provider == "cloudflare" && !executable(c.Values["supervisor_path"]) {
		return errors.New("supervisor_path must point to an installed connector executable")
	}
	return nil
}

func dnsHostname(host string) bool {
	if len(host) > 253 || !strings.Contains(host, ".") || net.ParseIP(host) != nil {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if !dnsLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func funnelPort(p int) bool { return p == 443 || p == 8443 || p == 10000 }

func (c Config) validateProvider() error {
	allowed := map[string]bool{"coordination_dir": true}
	if c.Provider == "cloudflare" {
		for _, k := range []string{"ownership_mode", "authority_id", "account_id", "zone_id", "supervisor_path"} {
			allowed[k] = true
			if c.Values[k] == "" {
				return errors.New("complete single-writer Cloudflare settings required")
			}
		}
		if c.Values["ownership_mode"] != "single-writer" || c.HostA == c.HostB || c.PortA != 443 || c.PortB != 443 || !envName.MatchString(c.CredentialEnv) || !filepath.IsAbs(c.Values["supervisor_path"]) {
			return errors.New("cloudflare requires distinct dedicated hosts, port 443, supervisor and credential environment reference")
		}
	} else {
		if c.CredentialEnv != "" {
			return errors.New("tailscale configuration does not accept a credential environment reference")
		}
		allowed["local_api_socket"] = true
		allowed["local_api_transport"] = true
		if err := c.validateTailscale(); err != nil {
			return err
		}
	}
	for k := range c.Values {
		if !allowed[k] {
			return errors.New("unsupported provider setting; never put credential values in the harness file")
		}
	}
	return nil
}

func (c Config) validateTailscale() error {
	transport := c.Values["local_api_transport"]
	socket := c.Values["local_api_socket"]
	if (transport == "macos-gui" && socket != "") || (transport != "macos-gui" && transport != "unix" && transport != "") || (transport != "macos-gui" && !filepath.IsAbs(socket)) {
		return errors.New("tailscale requires explicit Unix socket or macos-gui transport without a socket")
	}
	if c.HostA != c.HostB || c.PortA == c.PortB || !funnelPort(c.PortA) || !funnelPort(c.PortB) {
		return errors.New("tailscale requires distinct supported ports on its node hostname")
	}
	return nil
}
