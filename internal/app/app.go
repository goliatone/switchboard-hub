package app

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goliatone/switchboard-hub/internal/caddy"
	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/internal/dns"
	"github.com/goliatone/switchboard-hub/internal/sys"
)

func cfgPath() (string, error) {
	override := strings.TrimSpace(os.Getenv("SWITCHD_CONFIG_PATH"))
	if override != "" {
		return filepath.Clean(override), nil
	}
	home, err := runUserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "switchboard-hub", "config.yaml"), nil
}

func runUserHomeDir() (string, error) {
	if os.Geteuid() != 0 {
		return os.UserHomeDir()
	}
	sudoUser := strings.TrimSpace(os.Getenv("SUDO_USER"))
	if sudoUser == "" || sudoUser == "root" {
		return os.UserHomeDir()
	}
	u, err := user.Lookup(sudoUser)
	if err != nil {
		return "", err
	}
	if u.HomeDir == "" {
		return "", fmt.Errorf("sudo user %q has no home directory", sudoUser)
	}
	return u.HomeDir, nil
}

func sudoOwner() (int, int, bool) {
	if os.Geteuid() != 0 {
		return 0, 0, false
	}
	uidStr := strings.TrimSpace(os.Getenv("SUDO_UID"))
	gidStr := strings.TrimSpace(os.Getenv("SUDO_GID"))
	if uidStr == "" || gidStr == "" {
		return 0, 0, false
	}
	uid, err := strconv.Atoi(uidStr)
	if err != nil {
		return 0, 0, false
	}
	gid, err := strconv.Atoi(gidStr)
	if err != nil {
		return 0, 0, false
	}
	return uid, gid, true
}

func fixSudoOwnership(path string) {
	uid, gid, ok := sudoOwner()
	if !ok {
		return
	}
	_ = os.Chown(path, uid, gid)
}

func ensureConfigDir() (string, error) {
	p, err := cfgPath()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	fixSudoOwnership(dir)
	return dir, nil
}

func Init(tld, dnsIP string, enableTLS bool, tlsMode string, tlsCertFile string, tlsKeyFile string) error {
	if os.Geteuid() != 0 {
		return errors.New("init must be run with sudo (needs /etc/resolver, dnsmasq.conf, and loopback alias)")
	}

	_, err := ensureConfigDir()
	if err != nil {
		return err
	}

	p, err := cfgPath()
	if err != nil {
		return err
	}
	created := false
	if _, statErr := os.Stat(p); statErr != nil {
		if !enableTLS && (strings.TrimSpace(tlsMode) != "" || strings.TrimSpace(tlsCertFile) != "" || strings.TrimSpace(tlsKeyFile) != "") {
			return errors.New("tls flags require --tls=true")
		}
		if normalizeTLSMode(tlsMode) == "internal" && (strings.TrimSpace(tlsCertFile) != "" || strings.TrimSpace(tlsKeyFile) != "") {
			return errors.New("tls-cert-file/tls-key-file can only be used with --tls-mode file")
		}
		c := config.Default(tld, dnsIP)
		baseDir := filepath.Dir(p)
		c.Caddy.TLS.Enabled = enableTLS
		c.Caddy.TLS.Mode = normalizeTLSMode(tlsMode)
		if c.Caddy.TLS.Mode == "file" {
			c.Caddy.TLS.CertFile, err = resolvePathInput(tlsCertFile, baseDir)
			if err != nil {
				return err
			}
			c.Caddy.TLS.KeyFile, err = resolvePathInput(tlsKeyFile, baseDir)
			if err != nil {
				return err
			}
		} else {
			c.Caddy.TLS.CertFile = ""
			c.Caddy.TLS.KeyFile = ""
		}
		if err := validateTLSConfig(c); err != nil {
			return err
		}
		if err := config.Save(p, c); err != nil {
			return err
		}
		fmt.Println("wrote:", p)
		created = true
	} else {
		if !enableTLS || strings.TrimSpace(tlsMode) != "" || strings.TrimSpace(tlsCertFile) != "" || strings.TrimSpace(tlsKeyFile) != "" {
			return errors.New("config already exists; init does not update TLS settings (use switchd tls mkcert or edit config.yaml)")
		}
		fixSudoOwnership(p)
		fmt.Println("exists:", p)
	}

	c, err := config.Load(p)
	if err != nil {
		return err
	}
	if err := dns.Apply(c); err != nil {
		return err
	}

	if created {
		if err := validateTLSConfig(c); err != nil {
			return err
		}
	}

	dir := filepath.Dir(p)
	bootstrap := filepath.Join(dir, "bootstrap.Caddyfile")
	if err := caddy.WriteBootstrapCaddyfile(bootstrap); err != nil {
		return err
	}
	fixSudoOwnership(bootstrap)
	fmt.Println("wrote:", bootstrap)

	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1) Restart dnsmasq: sudo brew services restart dnsmasq")
	fmt.Println("  2) Flush DNS cache: sudo dscacheutil -flushcache && sudo killall -HUP mDNSResponder")
	fmt.Println("  3) Start Caddy (in another terminal): sudo switchd caddy run")
	fmt.Println("  4) Add routes + apply: switchd add myapp --port 3030 && switchd apply")
	if c.Caddy.TLS.Enabled {
		switch normalizeTLSMode(c.Caddy.TLS.Mode) {
		case "internal":
			fmt.Println("  5) Trust Caddy local CA (once): sudo caddy trust")
		case "file":
			fmt.Println("  5) Ensure your configured cert is trusted by your browser/system")
		}
	}
	return nil
}

func Uninstall() error {
	if os.Geteuid() != 0 {
		return errors.New("uninstall must be run with sudo (needs /etc/resolver, dnsmasq.conf, and loopback alias)")
	}
	p, err := cfgPath()
	if err != nil {
		return err
	}
	c, err := config.LoadOrDefault(p)
	if err != nil {
		return err
	}
	if err := dns.Uninstall(c); err != nil {
		return err
	}

	fmt.Println()
	fmt.Println("Next steps:")
	fmt.Println("  1) Restart dnsmasq: sudo brew services restart dnsmasq")
	fmt.Println("  2) Flush DNS cache: sudo dscacheutil -flushcache && sudo killall -HUP mDNSResponder")
	return nil
}

func AddRoute(nameOrHost string, port int) error {
	p, err := cfgPath()
	if err != nil {
		return err
	}
	c, err := config.LoadOrCreateDefault(p)
	if err != nil {
		return err
	}

	host := normalizeHost(nameOrHost, c.TLD)
	dial := fmt.Sprintf("127.0.0.1:%d", port)

	found := false
	for i := range c.Routes {
		if strings.EqualFold(c.Routes[i].Host, host) {
			c.Routes[i].Dial = dial
			found = true
			break
		}
	}
	if !found {
		c.Routes = append(c.Routes, config.Route{Host: host, Dial: dial})
	}
	sort.Slice(c.Routes, func(i, j int) bool { return c.Routes[i].Host < c.Routes[j].Host })
	if err := syncAppFromRoute(c, host, port); err != nil {
		return err
	}

	if err := config.Save(p, c); err != nil {
		return err
	}
	return nil
}

func RemoveRoute(nameOrHost string) error {
	p, err := cfgPath()
	if err != nil {
		return err
	}
	c, err := config.LoadOrDefault(p)
	if err != nil {
		return err
	}
	host := normalizeHost(nameOrHost, c.TLD)

	out := make([]config.Route, 0, len(c.Routes))
	removed := false
	for _, r := range c.Routes {
		if strings.EqualFold(r.Host, host) {
			removed = true
			continue
		}
		out = append(out, r)
	}
	if !removed {
		return fmt.Errorf("route not found: %s", host)
	}
	c.Routes = out
	removeAppByHost(c, host)
	if err := config.Save(p, c); err != nil {
		return err
	}
	return nil
}

func ListRoutes() error {
	p, err := cfgPath()
	if err != nil {
		return err
	}
	c, err := config.LoadOrDefault(p)
	if err != nil {
		return err
	}
	if len(c.Routes) == 0 {
		fmt.Println("(no routes)")
		return nil
	}
	for _, r := range c.Routes {
		fmt.Printf("%-35s -> %s\n", r.Host, r.Dial)
	}
	return nil
}

func Apply() error {
	p, err := cfgPath()
	if err != nil {
		return err
	}
	c, err := config.LoadOrDefault(p)
	if err != nil {
		return err
	}
	if err := validateTLSConfig(c); err != nil {
		return fmt.Errorf("invalid TLS config: %w", err)
	}

	client := &http.Client{Timeout: 2 * time.Second}
	req, _ := http.NewRequest("GET", strings.TrimRight(c.Caddy.Admin, "/")+"/config/", nil)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("caddy admin not reachable at %s (is Caddy running?): %w", c.Caddy.Admin, err)
	}
	_ = resp.Body.Close()

	cfgJSON, err := caddy.BuildJSON(c)
	if err != nil {
		return err
	}

	if err := caddy.LoadConfig(c.Caddy.Admin, cfgJSON); err != nil {
		return err
	}

	dir, _ := ensureConfigDir()
	last := filepath.Join(dir, "last-applied.json")
	if err := os.WriteFile(last, cfgJSON, 0o644); err == nil {
		fixSudoOwnership(last)
		fmt.Println("wrote:", last)
	}

	return nil
}

func Open(nameOrHost string, scheme string) error {
	p, err := cfgPath()
	if err != nil {
		return err
	}
	c, err := config.LoadOrDefault(p)
	if err != nil {
		return err
	}

	host := normalizeHost(nameOrHost, c.TLD)

	s := strings.ToLower(strings.TrimSpace(scheme))
	if s == "" {
		if c.Caddy.TLS.Enabled {
			s = "https"
		} else {
			s = "http"
		}
	}
	if s != "http" && s != "https" {
		return fmt.Errorf("invalid scheme %q (expected http or https)", scheme)
	}

	url := fmt.Sprintf("%s://%s", s, host)
	if sys.Exists("open") {
		return sys.Run("open", url)
	}
	fmt.Println(url)
	return nil
}

func Status() error {
	p, err := cfgPath()
	if err != nil {
		return err
	}
	c, err := config.LoadOrDefault(p)
	if err != nil {
		return err
	}

	fmt.Println("Config:", p)
	fmt.Println("TLD:   ", c.TLD)
	fmt.Println("DNS IP:", c.DNS.IP)
	fmt.Println("Caddy:", c.Caddy.Admin)
	fmt.Printf("TLS:    enabled=%t mode=%s\n", c.Caddy.TLS.Enabled, normalizeTLSMode(c.Caddy.TLS.Mode))

	fmt.Println()
	fmt.Println("Checks:")

	if err := validateTLSConfig(c); err != nil {
		fmt.Println("- TLS:", "invalid:", err)
	} else {
		fmt.Println("- TLS:", "config ok")
	}
	for _, w := range tlsWarnings(c) {
		fmt.Println("- TLS:", "warning:", w)
	}

	if sys.Exists("dig") {
		out, err := sys.RunCapture("dig", "+short", "switchboard-hub-status."+c.TLD, "@"+c.DNS.IP)
		if err != nil {
			fmt.Println("- DNS:", "dig failed:", err)
		} else if strings.TrimSpace(out) == "" {
			fmt.Println("- DNS:", "no answer (dnsmasq might not be running/configured)")
		} else {
			fmt.Println("- DNS:", "ok (dig returned:", strings.TrimSpace(out)+")")
		}
	} else {
		fmt.Println("- DNS:", "dig not found (skipping)")
	}

	client := &http.Client{Timeout: 2 * time.Second}
	req, _ := http.NewRequest("GET", strings.TrimRight(c.Caddy.Admin, "/")+"/config/", nil)
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("- Caddy:", "admin unreachable:", err)
		fmt.Println("  Start: sudo switchd caddy run")
	} else {
		_ = resp.Body.Close()
		fmt.Println("- Caddy:", "admin reachable")
	}

	fmt.Println("- Apps:", fmt.Sprintf("%d configured", len(c.Apps)))
	if len(c.Apps) == 0 {
		fmt.Println("  (none)")
		return nil
	}
	for _, a := range c.Apps {
		fmt.Printf("  - %s: host=%s port=%d\n", a.Name, a.LocalHost, a.LocalPort)
	}

	health, hErr := appTunnelHealthStatusFromConfig(c)
	if hErr != nil {
		fmt.Println("- Tunnel health:", "error:", hErr)
		return nil
	}
	fmt.Println("- Tunnel health:")
	for _, h := range health {
		if strings.TrimSpace(h.Provider) == "" {
			fmt.Printf("  - %s: no tunnel configured\n", h.AppName)
			continue
		}
		base := fmt.Sprintf("  - %s: provider=%s host=%s", h.AppName, h.Provider, h.EndpointHost)
		if strings.TrimSpace(h.Err) != "" {
			fmt.Printf("%s status=error %s\n", base, h.Err)
			continue
		}
		status := "not-ready"
		if h.Ready {
			status = "ready"
		}
		sessionInfo := strings.TrimSpace(sessionSummary(h.SessionPID, h.StartedAt))
		if sessionInfo != "" {
			sessionInfo = " " + sessionInfo
		}
		fmt.Printf("%s status=%s %s%s\n", base, status, h.Message, sessionInfo)
	}

	return nil
}

func TLSMkcert(certFile, keyFile string, install bool) error {
	if !sys.Exists("mkcert") {
		return errors.New("mkcert not found (install with: brew install mkcert)")
	}

	p, err := cfgPath()
	if err != nil {
		return err
	}
	c, err := config.LoadOrCreateDefault(p)
	if err != nil {
		return err
	}

	baseDir := filepath.Dir(p)
	certOut := strings.TrimSpace(certFile)
	keyOut := strings.TrimSpace(keyFile)
	if certOut == "" {
		certOut = filepath.Join(baseDir, "tls-cert.pem")
	}
	if keyOut == "" {
		keyOut = filepath.Join(baseDir, "tls-key.pem")
	}
	certOut, err = resolvePathInput(certOut, baseDir)
	if err != nil {
		return err
	}
	keyOut, err = resolvePathInput(keyOut, baseDir)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(certOut), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(keyOut), 0o755); err != nil {
		return err
	}
	fixSudoOwnership(filepath.Dir(certOut))
	fixSudoOwnership(filepath.Dir(keyOut))

	hosts := mkcertHosts(c)
	args := make([]string, 0, len(hosts)+5)
	if install {
		args = append(args, "-install")
	}
	args = append(args, "-cert-file", certOut, "-key-file", keyOut)
	args = append(args, hosts...)

	if err := sys.Run("mkcert", args...); err != nil {
		return err
	}
	fixSudoOwnership(certOut)
	fixSudoOwnership(keyOut)

	c.Caddy.TLS.Enabled = true
	c.Caddy.TLS.Mode = "file"
	c.Caddy.TLS.CertFile = certOut
	c.Caddy.TLS.KeyFile = keyOut
	if err := validateTLSConfig(c); err != nil {
		return err
	}
	if err := config.Save(p, c); err != nil {
		return err
	}

	fmt.Println("saved:", p)
	fmt.Println("generated cert:", certOut)
	fmt.Println("generated key: ", keyOut)
	fmt.Println("Next: switchd apply")
	return nil
}

func CaddyRun() error {
	if os.Geteuid() != 0 {
		return errors.New("caddy run must be run with sudo to bind :80/:443")
	}

	p, err := cfgPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	bootstrap := filepath.Join(dir, "bootstrap.Caddyfile")

	if _, err := os.Stat(bootstrap); err != nil {
		_ = os.MkdirAll(dir, 0o755)
		if err := caddy.WriteBootstrapCaddyfile(bootstrap); err != nil {
			return err
		}
		fixSudoOwnership(dir)
		fixSudoOwnership(bootstrap)
		fmt.Println("wrote:", bootstrap)
	} else {
		fixSudoOwnership(bootstrap)
	}

	return sys.Run("caddy", "run", "--config", bootstrap, "--adapter", "caddyfile")
}

func normalizeHost(nameOrHost, tld string) string {
	s := strings.TrimSpace(nameOrHost)
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimSuffix(s, "/")

	if strings.Contains(s, ".") {
		return s
	}
	return s + "." + tld
}

func normalizeTLSMode(mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "" {
		return "internal"
	}
	return m
}

func validateTLSConfig(c *config.Config) error {
	if !c.Caddy.TLS.Enabled {
		return nil
	}

	switch normalizeTLSMode(c.Caddy.TLS.Mode) {
	case "internal":
		return nil
	case "file":
		certFile := strings.TrimSpace(c.Caddy.TLS.CertFile)
		keyFile := strings.TrimSpace(c.Caddy.TLS.KeyFile)
		if certFile == "" || keyFile == "" {
			return errors.New("tls mode file requires caddy.tls.cert_file and caddy.tls.key_file")
		}
		if !filepath.IsAbs(certFile) {
			return fmt.Errorf("tls cert file must be an absolute path: %q", certFile)
		}
		if !filepath.IsAbs(keyFile) {
			return fmt.Errorf("tls key file must be an absolute path: %q", keyFile)
		}
		if _, err := os.Stat(certFile); err != nil {
			return fmt.Errorf("tls cert file not accessible %q: %w", certFile, err)
		}
		if _, err := os.Stat(keyFile); err != nil {
			return fmt.Errorf("tls key file not accessible %q: %w", keyFile, err)
		}
		certPEM, err := os.ReadFile(certFile)
		if err != nil {
			return fmt.Errorf("read tls cert file %q: %w", certFile, err)
		}
		keyPEM, err := os.ReadFile(keyFile)
		if err != nil {
			return fmt.Errorf("read tls key file %q: %w", keyFile, err)
		}
		if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
			return fmt.Errorf("tls cert/key are not a valid pair: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported tls mode %q (expected internal or file)", c.Caddy.TLS.Mode)
	}
}

func resolvePathInput(raw, baseDir string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	if strings.HasPrefix(s, "~/") {
		home, err := runUserHomeDir()
		if err != nil {
			return "", err
		}
		s = filepath.Join(home, strings.TrimPrefix(s, "~/"))
	}
	if !filepath.IsAbs(s) {
		s = filepath.Join(baseDir, s)
	}
	return filepath.Clean(s), nil
}

func mkcertHosts(c *config.Config) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(c.Routes)+1)
	add := func(host string) {
		h := strings.ToLower(strings.TrimSpace(host))
		if h == "" {
			return
		}
		if _, ok := seen[h]; ok {
			return
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}

	add("*." + c.TLD)
	for _, r := range c.Routes {
		add(r.Host)
	}
	sort.Strings(out)
	return out
}

func tlsWarnings(c *config.Config) []string {
	out := []string{}
	mode := normalizeTLSMode(c.Caddy.TLS.Mode)
	cert := strings.TrimSpace(c.Caddy.TLS.CertFile)
	key := strings.TrimSpace(c.Caddy.TLS.KeyFile)

	if !c.Caddy.TLS.Enabled && (mode != "internal" || cert != "" || key != "") {
		out = append(out, "TLS is disabled; tls.mode/cert_file/key_file are ignored")
	}
	if c.Caddy.TLS.Enabled && mode == "internal" && (cert != "" || key != "") {
		out = append(out, "tls.cert_file and tls.key_file are ignored in internal mode")
	}
	if c.Caddy.TLS.Enabled && mode == "internal" && len(c.Routes) == 0 {
		out = append(out, "no routes configured yet; internal certs will be issued after hosts are added")
	}
	return out
}
