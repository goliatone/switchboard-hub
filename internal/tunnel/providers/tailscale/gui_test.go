package tailscale

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

const guiTestToken = "0123456789abcdef0123"

type guiFixture struct {
	d                                    *fakeDaemon
	server                               *httptest.Server
	helper, credentialsFile, versionFile string
	mu                                   sync.Mutex
	token, version, node, mode           string
	posts                                int
}

func newGUIFixture(t *testing.T) *guiFixture {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("macOS GUI initialization is platform gated")
	}
	f := &guiFixture{d: newDaemon(t), token: guiTestToken, version: guiBackendVersion, node: "node-a"}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(func() { f.server.CloseClientConnections(); f.server.Close() })
	dir := t.TempDir()
	f.helper = filepath.Join(dir, "tailscale")
	f.credentialsFile = filepath.Join(dir, "credentials")
	f.versionFile = filepath.Join(dir, "version")
	writeGUIFixture(t, f.versionFile, guiHelperVersion+"\n", 0o600)
	f.setDiscovery(t, guiTestToken)
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in\nversion) /bin/cat %q ;;\n'debug local-creds') /bin/cat %q ;;\n*) exit 1 ;;\nesac\n", f.versionFile, f.credentialsFile)
	writeGUIFixture(t, f.helper, script, 0o700)
	return f
}
func writeGUIFixture(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}
func (f *guiFixture) setDiscovery(t *testing.T, token string) {
	t.Helper()
	port := strings.TrimPrefix(f.server.URL, "http://127.0.0.1:")
	writeGUIFixture(t, f.credentialsFile, "curl -u:"+token+" http://localhost:"+port+"/localapi/v0/status\n", 0o600)
}
func (f *guiFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	token, version, node, mode := f.token, f.version, f.node, f.mode
	if r.Method == http.MethodPost {
		f.posts++
	}
	f.mu.Unlock()
	user, auth, ok := r.BasicAuth()
	if !ok || user != "" || auth != token || r.Host != localAPIHost {
		http.Error(w, "private-auth-error-"+guiTestToken, http.StatusForbidden)
		return
	}
	if mode == "drop-post" && r.Method == http.MethodPost {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
		return
	}
	if mode == "redirect" {
		http.Redirect(w, r, "http://127.0.0.1:1/credential-leak", http.StatusTemporaryRedirect)
		return
	}
	if r.URL.Path == "/localapi/v0/status" {
		_ = json.NewEncoder(w).Encode(map[string]any{"Version": version, "BackendState": "Running", "Self": map[string]string{"ID": node, "PublicKey": "key-a", "DNSName": "host.ts.net."}})
		return
	}
	f.d.serve(w, r)
}
func (f *guiFixture) provider(t *testing.T, coord string) *Provider {
	t.Helper()
	p := New()
	rt := ingress.Runtime{StateDir: filepath.Join(t.TempDir(), "state"), HelperPath: f.helper}
	p.owned.runtime = rt
	if err := p.InitIngressPrivate(context.Background(), map[string]string{"local_api_transport": "macos-gui", "coordination_dir": coord}, rt, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.owned.client.CloseIdleConnections)
	return p
}
func TestGUIScopedLifecycleCASAndStableCoordination(t *testing.T) {
	f := newGUIFixture(t)
	ctx := context.Background()
	coord := filepath.Join(t.TempDir(), "coord")
	a, b := f.provider(t, coord), f.provider(t, coord)
	ra, err := a.EnsureOwnedIngress(ctx, resource(8443, "a"))
	if err != nil {
		t.Fatal(err)
	}
	rb, err := b.EnsureOwnedIngress(ctx, resource(10000, "b"))
	if err != nil {
		t.Fatal(err)
	}
	rb, err = b.StartOwnedIngress(ctx, rb)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.StopOwnedIngress(ctx, rb) })
	f.d.mu.Lock()
	f.d.conflict = true
	f.d.mu.Unlock()
	if _, err = a.StartOwnedIngress(ctx, ra); err == nil {
		t.Fatal("stale ETag accepted")
	}
	if s, e := b.StatusOwnedIngress(ctx, rb); e != nil || !s.Ready {
		t.Fatal("B damaged by CAS refusal", e)
	}
	ra, err = a.StartOwnedIngress(ctx, ra)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.StopOwnedIngress(ctx, ra); err != nil {
		t.Fatal(err)
	}
	if s, e := b.StatusOwnedIngress(ctx, rb); e != nil || !s.Ready {
		t.Fatal("B damaged by A stop", e)
	}
	if a.owned.socket != b.owned.socket {
		t.Fatal("different GUI coordination identity")
	}
	unlock, err := a.lockOwned(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wait, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if release, e := b.lockOwned(wait); e == nil {
		release()
		t.Error("different stores bypassed GUI authority lock")
	}
	unlock()
}
func TestGUICredentialRotationDoesNotReplayOrLoseLockIdentity(t *testing.T) {
	f := newGUIFixture(t)
	coord := filepath.Join(t.TempDir(), "coord")
	a := f.provider(t, coord)
	rotated := "abcdef0123456789abcd"
	f.mu.Lock()
	f.token = rotated
	f.mu.Unlock()
	f.setDiscovery(t, rotated)
	if _, _, err := a.getConfig(context.Background()); err == nil || strings.Contains(err.Error(), guiTestToken) {
		t.Fatal("stale authentication did not fail privately")
	}
	b := f.provider(t, coord)
	if a.owned.socket != b.owned.socket || a.leaseScope() == b.leaseScope() {
		t.Fatal("rotation changed lock or reused session namespace")
	}
	if _, err := b.EnsureOwnedIngress(context.Background(), resource(8443, "fresh")); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 0 {
		t.Fatal("credential recovery wrote configuration")
	}
}
func TestGUIRefusesProfileChangeRedirectAndBackendUpdate(t *testing.T) {
	f := newGUIFixture(t)
	ctx := context.Background()
	p := f.provider(t, filepath.Join(t.TempDir(), "coord"))
	r, err := p.EnsureOwnedIngress(ctx, resource(8443, "a"))
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"node", "version", "redirect"} {
		f.mu.Lock()
		f.node = "node-a"
		f.version = guiBackendVersion
		f.mode = ""
		switch change {
		case "node":
			f.node = "other-node"
		case "version":
			f.version = "1.105.0"
		case "redirect":
			f.mode = "redirect"
		}
		f.mu.Unlock()
		if _, err = p.StartOwnedIngress(ctx, r); err == nil {
			t.Fatal("unsafe backend accepted", change)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 0 {
		t.Fatal("unsafe preflight mutated configuration")
	}
}
func TestGUIInitRejectsUnsafeDiscovery(t *testing.T) {
	f := newGUIFixture(t)
	for _, body := range []string{
		"curl --unix-socket /tmp/daemon.sock http://local-tailscaled.sock/localapi/v0/status\n",
		"curl -u:" + guiTestToken + " http://example.com:443/localapi/v0/status\n",
		"curl -u:" + guiTestToken + " http://localhost:1234/localapi/v0/status; touch /tmp/unsafe\n",
		"curl -u:" + guiTestToken + " http://localhost:0/localapi/v0/status\n",
		"curl -u:" + guiTestToken + " http://localhost:65536/localapi/v0/status\n",
		"curl -u:" + guiTestToken + " http://localhost:01234/localapi/v0/status\n",
		strings.Repeat(guiTestToken, 1000),
	} {
		writeGUIFixture(t, f.credentialsFile, body, 0o600)
		_, err := discoverGUI(context.Background(), f.helper)
		if err == nil || strings.Contains(err.Error(), guiTestToken) {
			t.Fatal("unsafe discovery or secret error")
		}
	}
	f.setDiscovery(t, guiTestToken)
	writeGUIFixture(t, f.versionFile, "1.105.0\n", 0o600)
	if _, err := discoverGUI(context.Background(), f.helper); err == nil {
		t.Fatal("unsupported helper accepted")
	}
	writeGUIFixture(t, f.versionFile, guiHelperVersion+"\n", 0o600)
	if err := os.Chmod(f.helper, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := discoverGUI(context.Background(), f.helper); err == nil {
		t.Fatal("writable helper accepted")
	}
}
func TestGUIHelperEnvironmentCancellationAndErrorPrivacy(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "helper")
	t.Setenv("TUNNEL_TOKEN", guiTestToken)
	t.Setenv("HTTPS_PROXY", "http://private-secret.invalid")
	t.Setenv("SWITCHD_CF_API_TOKEN", guiTestToken)
	writeGUIFixture(t, helper, "#!/bin/sh\n/usr/bin/env\n", 0o700)
	raw, err := guiHelperOutput(context.Background(), helper, "version")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	for _, bad := range []string{guiTestToken, "HTTPS_PROXY", "SWITCHD_CF_API_TOKEN", "TUNNEL_TOKEN"} {
		if strings.Contains(string(raw), bad) {
			t.Fatal("helper inherited secret")
		}
	}
	writeGUIFixture(t, helper, "#!/bin/sh\necho "+guiTestToken+" >&2\nexit 1\n", 0o700)
	if _, err = guiHelperOutput(context.Background(), helper); err == nil || strings.Contains(err.Error(), guiTestToken) {
		t.Fatal("helper error disclosure")
	}
	marker := filepath.Join(dir, "started")
	writeGUIFixture(t, helper, fmt.Sprintf("#!/bin/sh\ntouch %q\n", marker), 0o700)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = guiHelperOutput(ctx, helper); err == nil {
		t.Fatal("cancelled helper accepted")
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("cancelled helper started")
	}
}
func TestGUITransportRejectsUntrustedDestinationsAndUnconditionalWrites(t *testing.T) {
	client := newGUIClient(guiCredentials{port: "1", token: []byte(guiTestToken)})
	for _, url := range []string{"https://local-tailscaled.sock/localapi/v0/status", "http://example.com/localapi/v0/status", "http://user@local-tailscaled.sock/localapi/v0/status", "http://local-tailscaled.sock/localapi/v0/status?redirect=1", "http://local-tailscaled.sock/localapi/v0/prefs"} {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		res, callErr := client.Do(req)
		if res != nil {
			_ = res.Body.Close()
		}
		if callErr == nil || strings.Contains(callErr.Error(), guiTestToken) {
			t.Fatal("unsafe request accepted")
		}
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://local-tailscaled.sock/localapi/v0/serve-config", nil)
	if err != nil {
		t.Fatal(err)
	}
	if guiRequestAllowed(req) {
		t.Fatal("unconditional config write accepted")
	}
}

func TestGUIInitializationFailsClosedAndAliasesShareCoordination(t *testing.T) {
	f := newGUIFixture(t)
	coord := filepath.Join(t.TempDir(), "coord")
	a := f.provider(t, coord)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(f.helper, alias); err != nil {
		t.Fatal(err)
	}
	f.helper = alias
	b := f.provider(t, coord)
	other := newGUIFixture(t).provider(t, coord)
	if a.owned.socket != b.owned.socket || a.owned.socket != other.owned.socket || a.leaseScope() == other.leaseScope() {
		t.Fatal("helper aliases or port rotation bypass coordination")
	}
	settings := map[string]string{"local_api_transport": "macos-gui", "coordination_dir": coord, "local_api_socket": "/tmp/other.sock"}
	if err := a.InitIngressPrivate(context.Background(), settings, ingress.Runtime{HelperPath: alias}, nil); err == nil || a.owned.client != nil {
		t.Fatal("ambiguous transport retained an active client")
	}
	settings["local_api_socket"] = ""
	if err := b.InitIngressPrivate(context.Background(), settings, ingress.Runtime{HelperPath: alias}, []byte(guiTestToken)); err == nil || b.owned.client != nil {
		t.Fatal("private initialization failure retained an active client")
	}
}
func TestGUITransportNeverReplaysUncertainWrite(t *testing.T) {
	f := newGUIFixture(t)
	p := f.provider(t, filepath.Join(t.TempDir(), "coord"))
	ctx := context.Background()
	r, err := p.EnsureOwnedIngress(ctx, resource(8443, "lost"))
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.mode = "drop-post"
	f.mu.Unlock()
	if _, err = p.StartOwnedIngress(ctx, r); err == nil || strings.Contains(err.Error(), guiTestToken) {
		t.Fatal("uncertain write reported success or leaked authentication")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.posts != 1 {
		t.Fatalf("uncertain write sent %d times", f.posts)
	}
}
