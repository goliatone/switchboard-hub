package tailscale

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

type fakeDaemon struct {
	mu        sync.Mutex
	cfg       document
	rev, next int
	conflict  bool
	watches   map[string]chan struct{}
	server    *httptest.Server
}

func newDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	d := &fakeDaemon{cfg: document{"Unknown": json.RawMessage(`{"preserve":true}`)}, watches: map[string]chan struct{}{}}
	d.server = httptest.NewServer(http.HandlerFunc(d.serve))
	t.Cleanup(func() {
		d.mu.Lock()
		for _, c := range d.watches {
			close(c)
		}
		d.watches = map[string]chan struct{}{}
		d.mu.Unlock()
		d.server.Close()
	})
	return d
}
func (d *fakeDaemon) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/localapi/v0/watch-ipn-bus" {
		d.mu.Lock()
		d.next++
		id := fmt.Sprint("session-", d.next)
		end := make(chan struct{})
		d.watches[id] = end
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"SessionID": id})
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-end:
		}
		d.mu.Lock()
		fg, _ := object(d.cfg, "Foreground")
		delete(fg, id)
		d.cfg["Foreground"] = marshal(fg)
		d.rev++
		delete(d.watches, id)
		d.mu.Unlock()
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch r.URL.Path {
	case "/localapi/v0/status":
		_ = json.NewEncoder(w).Encode(map[string]any{"BackendState": "Running", "Self": map[string]string{"ID": "node-a", "PublicKey": "key-a", "DNSName": "host.ts.net."}})
	case "/localapi/v0/serve-config":
		if r.Method == "POST" {
			if d.conflict {
				d.conflict = false
				d.rev++
				d.cfg["operator"] = marshal("concurrent")
			}
			if r.Header.Get("If-Match") != fmt.Sprint(d.rev) {
				w.WriteHeader(412)
				return
			}
			var cfg document
			if json.NewDecoder(r.Body).Decode(&cfg) != nil {
				w.WriteHeader(400)
				return
			}
			d.cfg = cfg
			d.rev++
		}
		w.Header().Set("ETag", fmt.Sprint(d.rev))
		_ = json.NewEncoder(w).Encode(d.cfg)
	default:
		w.WriteHeader(404)
	}
}
func (d *fakeDaemon) provider(t *testing.T, state, coord string) *Provider {
	t.Helper()
	p := New()
	p.owned = ownedConfig{socket: d.server.URL, coordination: coord, runtime: ingress.Runtime{StateDir: state}, client: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", d.server.Listener.Addr().String())
	}}}}
	return p
}
func resource(port int, generation string) ingress.Resource {
	return ingress.Resource{Name: generation, Provider: "tailscale", PublicHost: "host.ts.net", LocalURL: "http://127.0.0.1:8080", Owner: ingress.Owner{System: "test", ScopeID: generation}, Generation: generation, OperationID: "op-" + generation, Binding: ingress.Binding{HTTPSPort: port, Path: "/"}}
}
func setupOwned(t *testing.T, d *fakeDaemon, port int, gen, coord string) (*Provider, ingress.Resource) {
	t.Helper()
	p := d.provider(t, filepath.Join(t.TempDir(), "state"), coord)
	r, err := p.EnsureOwnedIngress(context.Background(), resource(port, gen))
	if err != nil {
		t.Fatal(err)
	}
	return p, r
}
func TestOwnedScopedLifecycleAndFreshProvider(t *testing.T) {
	d := newDaemon(t)
	coord := filepath.Join(t.TempDir(), "coord")
	a, ra := setupOwned(t, d, 8443, "a", coord)
	b, rb := setupOwned(t, d, 10000, "b", coord)
	ctx := context.Background()
	var err error
	rb, err = b.StartOwnedIngress(ctx, rb)
	if err != nil {
		t.Fatal(err)
	}
	ra, err = a.StartOwnedIngress(ctx, ra)
	if err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	before := append([]byte(nil), d.cfg["Unknown"]...)
	bconfig := exposure(rb)
	d.mu.Unlock()
	// A fresh provider has no in-instance session cache.
	fresh := d.provider(t, a.owned.runtime.StateDir, coord)
	if err = fresh.StopOwnedIngress(ctx, ra); err != nil {
		t.Fatal(err)
	}
	if err = fresh.StopOwnedIngress(ctx, ra); err != nil {
		t.Fatal(err)
	}
	if err = fresh.ReleaseOwnedIngress(ctx, ra); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	fg, _ := object(d.cfg, "Foreground")
	preserved := jsonEqual(fg[rb.Session.ID], bconfig) && jsonEqual(d.cfg["Unknown"], before)
	d.mu.Unlock()
	if !preserved {
		t.Fatal("other state changed")
	}
	status, err := b.StatusOwnedIngress(ctx, rb)
	if err != nil || !status.Ready {
		t.Fatalf("B not ready: %+v %v", status, err)
	}
	if err = b.StopOwnedIngress(ctx, rb); err != nil {
		t.Fatal(err)
	}
}
func TestOwnedRefusesPrivateCollisionAndChangedBinding(t *testing.T) {
	d := newDaemon(t)
	coord := filepath.Join(t.TempDir(), "coord")
	d.mu.Lock()
	d.cfg["TCP"] = marshal(map[string]any{"443": map[string]bool{"HTTPS": true}})
	d.mu.Unlock()
	p := d.provider(t, filepath.Join(t.TempDir(), "state"), coord)
	if _, err := p.EnsureOwnedIngress(context.Background(), resource(443, "collision")); err == nil {
		t.Fatal("private port adopted")
	}
	p, r := setupOwned(t, d, 8443, "a", coord)
	r, err := p.StartOwnedIngress(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	fg, _ := object(d.cfg, "Foreground")
	changed := r
	changed.LocalURL = "http://127.0.0.1:9999"
	fg[r.Session.ID] = exposure(changed)
	d.cfg["Foreground"] = marshal(fg)
	d.rev++
	d.mu.Unlock()
	if err = p.StopOwnedIngress(context.Background(), r); err == nil {
		t.Fatal("changed route removed")
	}
	d.mu.Lock()
	fg, _ = object(d.cfg, "Foreground")
	preserved := jsonEqual(fg[r.Session.ID], exposure(changed))
	d.mu.Unlock()
	if !preserved {
		t.Fatal("operator edits overwritten")
	}
}
func TestOwnedCASConflictPreservesConcurrentEdit(t *testing.T) {
	d := newDaemon(t)
	p, r := setupOwned(t, d, 8443, "a", filepath.Join(t.TempDir(), "coord"))
	d.mu.Lock()
	d.conflict = true
	d.mu.Unlock()
	if _, err := p.StartOwnedIngress(context.Background(), r); err == nil {
		t.Fatal("CAS conflict ignored")
	}
	d.mu.Lock()
	edit := string(d.cfg["operator"])
	d.mu.Unlock()
	if edit != `"concurrent"` {
		t.Fatal("concurrent change lost")
	}
}
func TestOwnedRecoveryNeverRestarts(t *testing.T) {
	d := newDaemon(t)
	coord := filepath.Join(t.TempDir(), "coord")
	p, r := setupOwned(t, d, 8443, "a", coord)
	b, rb := setupOwned(t, d, 10000, "b", coord)
	ctx := context.Background()
	rb, err := b.StartOwnedIngress(ctx, rb)
	if err != nil {
		t.Fatal(err)
	}
	started, err := p.StartOwnedIngress(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	fresh := d.provider(t, p.owned.runtime.StateDir, coord)
	out, err := fresh.ReconcileOwnedIngress(ctx, ingress.Progress{Step: "start", Resource: r})
	if err != nil || out.Outcome != "applied" || out.Resource.Session.ID != started.Session.ID {
		t.Fatalf("lost reply: %+v %v", out, err)
	}
	// Closing the owning watch models owner loss. The fake daemon removes its foreground entry.
	d.mu.Lock()
	close(d.watches[started.Session.ID])
	delete(d.watches, started.Session.ID)
	d.mu.Unlock()
	deadline := time.Now().Add(time.Second)
	for {
		status, e := fresh.StatusOwnedIngress(ctx, started)
		if e != nil {
			t.Fatal(e)
		}
		if !status.Ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owner loss kept exposure")
		}
		time.Sleep(time.Millisecond)
	}
	out, err = fresh.ReconcileOwnedIngress(ctx, ingress.Progress{Step: "start", Resource: r})
	if err != nil || out.Outcome != "not-applied" {
		t.Fatalf("owner loss: %+v %v", out, err)
	}
	status, err := b.StatusOwnedIngress(ctx, rb)
	if err != nil || !status.Ready || status.SessionID != rb.Session.ID {
		t.Fatalf("B changed after A lost its owner: %+v %v", status, err)
	}
	d.mu.Lock()
	fg, _ := object(d.cfg, "Foreground")
	count := d.next
	preserved := jsonEqual(fg[rb.Session.ID], exposure(rb))
	d.mu.Unlock()
	if count != 2 {
		t.Fatal("recovery opened another session")
	}
	if !preserved {
		t.Fatal("B's binding or visibility changed")
	}
}
func TestOwnedCrossStoreContention(t *testing.T) {
	d := newDaemon(t)
	coord := filepath.Join(t.TempDir(), "coord")
	a, ra := setupOwned(t, d, 8443, "a", coord)
	b, rb := setupOwned(t, d, 8443, "b", coord)
	ctx := context.Background()
	results := make(chan error, 2)
	go func() { _, err := a.StartOwnedIngress(ctx, ra); results <- err }()
	go func() { _, err := b.StartOwnedIngress(ctx, rb); results <- err }()
	success := 0
	for range 2 {
		if <-results == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("%d successful competing bindings", success)
	}
}

func TestOwnedRefusesWrongNodeAndUnownedSession(t *testing.T) {
	d := newDaemon(t)
	p, r := setupOwned(t, d, 8443, "a", filepath.Join(t.TempDir(), "coord"))
	ctx := context.Background()
	started, err := p.StartOwnedIngress(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	changed := started
	changed.Session.ID = "unowned"
	if err = p.StopOwnedIngress(ctx, changed); err == nil {
		t.Fatal("unowned session accepted")
	}
	changed = started
	changed.ProviderState = map[string]string{"node": "wrong"}
	if err = p.StopOwnedIngress(ctx, changed); err == nil {
		t.Fatal("changed node accepted")
	}
	status, err := p.StatusOwnedIngress(ctx, started)
	if err != nil || !status.Ready {
		t.Fatal("valid exposure damaged")
	}
}
func TestOwnedCancelledLockAndUnsupportedInitialization(t *testing.T) {
	d := newDaemon(t)
	p, r := setupOwned(t, d, 8443, "a", filepath.Join(t.TempDir(), "coord"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.StartOwnedIngress(ctx, r); err == nil {
		t.Fatal("cancelled start accepted")
	}
	if err := New().InitIngressPrivate(context.Background(), nil, ingress.Runtime{}, nil); err == nil {
		t.Fatal("missing transport accepted")
	}
	d.mu.Lock()
	calls := d.next
	d.mu.Unlock()
	if calls != 0 {
		t.Fatal("cancelled operation opened session")
	}
	for _, cfg := range []document{{"TCP": json.RawMessage(`[]`)}, {"Web": json.RawMessage(`{"invalid":{}}`)}} {
		if err := portFree(cfg, 8443, ""); err == nil {
			t.Fatal("unknown daemon state accepted")
		}
	}
}

func TestOwnedUnixLocalAPIWireLifecycle(t *testing.T) {
	d := newDaemon(t)
	dir, err := os.MkdirTemp("/tmp", "t06-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socket := filepath.Join(dir, "daemon.sock")
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(d.serve), ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close() }()
	p := New()
	r := resource(8443, "wire")
	rt := ingress.Runtime{StateDir: filepath.Join(dir, "state")}
	ctx := context.Background()
	if err = p.ValidateIngress(ctx, r, rt); err != nil {
		t.Fatal(err)
	}
	if err = p.InitIngressPrivate(ctx, map[string]string{"local_api_socket": socket, "coordination_dir": filepath.Join(dir, "coord")}, rt, nil); err != nil {
		t.Fatal(err)
	}
	r, err = p.EnsureOwnedIngress(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	r, err = p.StartOwnedIngress(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if err = p.StopOwnedIngress(ctx, r); err != nil {
		t.Fatal(err)
	}
}
