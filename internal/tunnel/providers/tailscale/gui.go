package tailscale

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

// Credential discovery is a version-tested debug interface, not a stable API.
// Expand this pair only with transport, security and installation evidence.
const guiHelperVersion = "1.104.7"
const guiBackendVersion = "1.104.4"
const localAPIHost = "local-tailscaled.sock"

var guiCredentialPattern = regexp.MustCompile(`^curl -u:([0-9a-f]{20}) http://localhost:([0-9]{1,5})/localapi/v0/status\n?$`)

type guiCredentials struct {
	port  string
	token []byte
}

// boundedOutput never keeps more than the fixed discovery protocol limit.
type boundedOutput struct{ data []byte }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > 4096-len(b.data) {
		return 0, errors.New("helper response exceeds limit")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func guiHelperOutput(ctx context.Context, helper string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, helper, args...) // #nosec G204 -- explicit trusted absolute helper; fixed internal arguments, never shell text.
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + os.Getenv("HOME")}
	cmd.WaitDelay = 100 * time.Millisecond
	out := &boundedOutput{}
	cmd.Stdout, cmd.Stderr = out, io.Discard
	if err := cmd.Run(); err != nil {
		clear(out.data)
		return nil, errors.New("tailscale GUI credential helper failed")
	}
	return out.data, nil
}
func discoverGUI(ctx context.Context, helper string) (guiCredentials, error) {
	if !filepath.IsAbs(helper) {
		return guiCredentials{}, errors.New("configure an absolute trusted Tailscale GUI helper")
	}
	helper, err := filepath.EvalSymlinks(helper)
	if err != nil {
		return guiCredentials{}, errors.New("tailscale GUI helper unavailable")
	}
	info, err := os.Stat(helper)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return guiCredentials{}, errors.New("tailscale GUI helper must be a regular executable without group or world write access")
	}
	version, err := guiHelperOutput(ctx, helper, "version")
	if err != nil {
		return guiCredentials{}, err
	}
	defer clear(version)
	first, _, _ := bytes.Cut(version, []byte{'\n'})
	if string(first) != guiHelperVersion {
		return guiCredentials{}, errors.New("unsupported Tailscale GUI helper version; use the documented tested version")
	}
	raw, err := guiHelperOutput(ctx, helper, "debug", "local-creds")
	if err != nil {
		return guiCredentials{}, err
	}
	defer clear(raw)
	return parseGUICredentials(raw)
}
func parseGUICredentials(raw []byte) (guiCredentials, error) {
	matches := guiCredentialPattern.FindSubmatch(raw)
	if matches == nil {
		return guiCredentials{}, errors.New("unsupported Tailscale GUI credential response; no transport fallback")
	}
	port, err := strconv.Atoi(string(matches[2]))
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != string(matches[2]) {
		return guiCredentials{}, errors.New("invalid Tailscale GUI endpoint")
	}
	return guiCredentials{port: string(matches[2]), token: bytes.Clone(matches[1])}, nil
}

// guiTransport pins one discovery result for its lifetime. It never rediscovers
// or retries uncertain writes, and never sends local authentication to a proxy,
// redirect target, DNS-derived host, or caller-specified remote endpoint.
type guiTransport struct {
	base        *http.Transport
	credentials guiCredentials
}

func newGUIClient(c guiCredentials) *http.Client {
	address := net.JoinHostPort("127.0.0.1", c.port)
	transport := &guiTransport{credentials: c, base: &http.Transport{
		ResponseHeaderTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp4", address)
		},
	}}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (t *guiTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !guiRequestAllowed(r) {
		return nil, errors.New("unsupported Tailscale GUI LocalAPI request")
	}
	clone := r.Clone(r.Context())
	clone.Host = localAPIHost
	clone.Header.Del("Proxy-Authorization")
	clone.SetBasicAuth("", string(t.credentials.token))
	res, err := t.base.RoundTrip(clone)
	if err != nil {
		return nil, errors.New("tailscale GUI LocalAPI transport failed")
	}
	return res, nil
}
func (t *guiTransport) CloseIdleConnections() { t.base.CloseIdleConnections() }
func guiRequestAllowed(r *http.Request) bool {
	if r.URL.Scheme != "http" || r.URL.Host != localAPIHost || r.URL.User != nil || r.URL.Opaque != "" || r.URL.Fragment != "" || r.URL.RawPath != "" {
		return false
	}
	switch r.URL.Path {
	case "/localapi/v0/status":
		return r.Method == http.MethodGet && r.URL.RawQuery == ""
	case "/localapi/v0/serve-config":
		return r.URL.RawQuery == "" && (r.Method == http.MethodGet || (r.Method == http.MethodPost && r.Header.Get("If-Match") != ""))
	case "/localapi/v0/watch-ipn-bus":
		return r.Method == http.MethodGet && r.URL.RawQuery == "mask=18"
	default:
		return false
	}
}
func supportedGUIBackend(version string) bool {
	return version == guiBackendVersion || strings.HasPrefix(version, guiBackendVersion+"-")
}
func (p *Provider) initGUI(ctx context.Context, settings map[string]string, rt ingress.Runtime) error {
	if runtime.GOOS != "darwin" || settings["local_api_socket"] != "" {
		return errors.New("macos-gui requires macOS and cannot be combined with local_api_socket")
	}
	credentials, err := discoverGUI(ctx, rt.HelperPath)
	if err != nil {
		return err
	}
	client := newGUIClient(credentials)
	if err = checkGUIBackend(ctx, client); err != nil {
		client.CloseIdleConnections()
		clear(credentials.token)
		return err
	}
	p.owned.client = client
	p.owned.gui = true
	// All helper paths and rotating ports for this user's GUI share one lock.
	p.owned.socket = "macos-gui-user-" + strconv.Itoa(os.Getuid())
	digest := sha256.Sum256(append([]byte(credentials.port+":"), credentials.token...))
	p.owned.sessionScope = p.owned.socket + ":" + hex.EncodeToString(digest[:])
	return nil
}
func checkGUIBackend(ctx context.Context, client *http.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+localAPIHost+"/localapi/v0/status", nil)
	if err != nil {
		return errors.New("tailscale GUI status request unavailable")
	}
	res, err := client.Do(req)
	if err != nil {
		return errors.New("tailscale GUI authentication or transport unavailable")
	}
	defer func() { _ = res.Body.Close() }()
	var state struct {
		Version, BackendState string
		Self                  struct{ ID, PublicKey, DNSName string }
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&state) != nil || !supportedGUIBackend(state.Version) || state.BackendState != "Running" || state.Self.ID == "" || state.Self.PublicKey == "" || state.Self.DNSName == "" {
		return errors.New("unsupported or inactive Tailscale GUI backend; use the documented tested version")
	}
	return nil
}
func (p *Provider) leaseScope() string {
	if p.owned.sessionScope != "" {
		return p.owned.sessionScope
	}
	return p.owned.socket
}
