package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/internal/tunnel/ownedstate"
	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

type ownedFake struct {
	mu         sync.Mutex
	tunnels    map[string]ownedTunnel
	dns        map[string]ownedDNS
	configs    map[string]ownedConfiguration
	calls      []string
	lost       string
	failDelete bool
}

func newOwnedFake(t *testing.T) (*ownedFake, *httptest.Server) {
	t.Helper()
	f := &ownedFake{tunnels: map[string]ownedTunnel{}, dns: map[string]ownedDNS{}, configs: map[string]ownedConfiguration{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}
func (f *ownedFake) serve(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req.Method+" "+req.URL.RequestURI())
	if req.Header.Get("Authorization") != "Bearer dummy-private-api" {
		http.Error(w, "dummy-private-api", http.StatusForbidden)
		return
	}
	path := strings.TrimSuffix(req.URL.Path, "/")
	parts := strings.Split(path, "/")
	last := parts[len(parts)-1]
	var result any
	switch {
	case strings.Contains(path, "/dns_records"):
		switch req.Method {
		case "GET":
			if last == "dns_records" {
				list := []ownedDNS{}
				for _, r := range f.dns {
					if r.Name == req.URL.Query().Get("name") {
						list = append(list, r)
					}
				}
				result = list
			} else {
				r, ok := f.dns[last]
				if !ok {
					http.Error(w, "absent", 404)
					return
				}
				result = r
			}
		case "POST":
			var r ownedDNS
			_ = json.NewDecoder(req.Body).Decode(&r)
			r.ID = fmt.Sprintf("dns-%d", len(f.dns)+1)
			r.CreatedAt = "created"
			r.ModifiedAt = "modified"
			f.dns[r.ID] = r
			result = r
		case "DELETE":
			delete(f.dns, last)
			result = map[string]string{"id": last}
		}
	case strings.Contains(path, "/cfd_tunnel"):
		switch {
		case req.Method == "POST":
			var r ownedTunnel
			_ = json.NewDecoder(req.Body).Decode(&r)
			r.ID = fmt.Sprintf("tunnel-%d", len(f.tunnels)+1)
			r.CreatedAt = "created"
			f.tunnels[r.ID] = r
			result = r
		case last == "configurations":
			id := parts[len(parts)-2]
			if req.Method == "PUT" {
				var c ownedConfiguration
				_ = json.NewDecoder(req.Body).Decode(&c)
				c.Version = 1
				f.configs[id] = c
			}
			result = f.configs[id]
		case last == "connections":
			result = []any{}
		case last == "token":
			result = "dummy-connector-token"
		case req.Method == "DELETE":
			if req.URL.RawQuery != "" {
				http.Error(w, "force forbidden", 400)
				return
			}
			if f.failDelete {
				http.Error(w, "dummy-private-api", http.StatusForbidden)
				return
			}
			delete(f.tunnels, last)
			result = map[string]string{"id": last}
		case last == "cfd_tunnel":
			list := []ownedTunnel{}
			for _, r := range f.tunnels {
				if r.Name == req.URL.Query().Get("name") {
					list = append(list, r)
				}
			}
			result = list
		default:
			r, ok := f.tunnels[last]
			if !ok {
				http.Error(w, "absent", 404)
				return
			}
			result = r
		}
	default:
		http.Error(w, "unknown", 400)
		return
	}
	if f.lost != "" && strings.Contains(req.Method+" "+path, f.lost) {
		f.lost = ""
		http.Error(w, "lost response with dummy-private-api", 500)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": result})
}
func ownedFixture(t *testing.T, srv *httptest.Server, scope string) (*Provider, ingress.Resource) {
	t.Helper()
	root := t.TempDir()
	p := New()
	p.owned = ownedSettings{account: "account", zone: "zone", authority: "authority", coordination: filepath.Join(root, "coord"), runtime: ingress.Runtime{StateDir: filepath.Join(root, "state"), RuntimeDir: filepath.Join(root, "runtime")}, api: ownedAPI{secret: []byte("dummy-private-api"), base: srv.URL, client: srv.Client()}}
	for _, d := range []string{p.owned.coordination, p.owned.runtime.StateDir, p.owned.runtime.RuntimeDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return p, ingress.Resource{Name: scope, Provider: "cloudflare", PublicHost: scope + ".example.test", LocalURL: "http://127.0.0.1:8080", Owner: ingress.Owner{System: "test", ScopeID: scope}, Generation: "generation-" + scope, OperationID: "operation-" + scope, Binding: ingress.Binding{Path: "/", HTTPSPort: 443}}
}
func TestOwnedCloudflareReleasePreservesBAndResumes(t *testing.T) {
	f, srv := newOwnedFake(t)
	p, a := ownedFixture(t, srv, "a")
	q, b := ownedFixture(t, srv, "b")
	q.owned.coordination = p.owned.coordination
	a, err := p.EnsureOwnedIngress(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	b, err = q.EnsureOwnedIngress(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	bDNS := f.dns[b.ProviderState["dns_id"]]
	bTunnel := f.tunnels[b.Binding.EndpointID]
	f.failDelete = true
	if err = p.ReleaseOwnedIngress(context.Background(), a); err == nil {
		t.Fatal("expected retained partial release")
	}
	saved, _, _ := ownedstate.Load(p.owned.runtime.StateDir, a)
	if saved.ProviderState["dns_deleted"] != "true" || saved.ProviderState["released"] == "true" {
		t.Fatal("lost cleanup responsibility")
	}
	f.failDelete = false
	if err = p.ReleaseOwnedIngress(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if err = p.ReleaseOwnedIngress(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if jsonText(f.dns[b.ProviderState["dns_id"]]) != jsonText(bDNS) || f.tunnels[b.Binding.EndpointID] != bTunnel {
		t.Fatal("changed B")
	}
	for _, c := range f.calls {
		if strings.Contains(c, "force=") {
			t.Fatal("force deletion")
		}
	}
}
func TestOwnedCloudflareDriftAndCollisions(t *testing.T) {
	for _, kind := range []string{"dns", "config", "tunnel", "authority", "unowned"} {
		t.Run(kind, func(t *testing.T) {
			f, srv := newOwnedFake(t)
			p, r := ownedFixture(t, srv, "a")
			if kind == "unowned" {
				f.dns["other"] = ownedDNS{ID: "other", Name: r.PublicHost}
				if _, err := p.EnsureOwnedIngress(context.Background(), r); err == nil {
					t.Fatal("adopted collision")
				}
				if len(f.tunnels) != 0 {
					t.Fatal("created before collision check")
				}
				return
			}
			r, err := p.EnsureOwnedIngress(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "dns":
				d := f.dns[r.ProviderState["dns_id"]]
				d.Comment = "operator change"
				f.dns[d.ID] = d
			case "config":
				c := f.configs[r.Binding.EndpointID]
				c.Version++
				f.configs[r.Binding.EndpointID] = c
			case "tunnel":
				v := f.tunnels[r.Binding.EndpointID]
				v.Name = "operator"
				f.tunnels[v.ID] = v
			case "authority":
				p.owned.authority = "other"
			}
			f.calls = nil
			if err = p.ReleaseOwnedIngress(context.Background(), r); err == nil {
				t.Fatal("deleted drift")
			}
			for _, c := range f.calls {
				if strings.HasPrefix(c, "DELETE") {
					t.Fatal("destructive call on drift")
				}
			}
		})
	}
}
func TestOwnedCloudflareLostCreateAndDeleteReplies(t *testing.T) {
	for _, kind := range []string{"tunnel", "dns"} {
		t.Run(kind, func(t *testing.T) {
			f, srv := newOwnedFake(t)
			p, r := ownedFixture(t, srv, "a")
			if kind == "tunnel" {
				f.lost = "POST /accounts/account/cfd_tunnel"
			} else {
				f.lost = "POST /zones/zone/dns_records"
			}
			if _, err := p.EnsureOwnedIngress(context.Background(), r); err == nil {
				t.Fatal("expected uncertainty")
			}
			got, err := p.EnsureOwnedIngress(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if len(f.tunnels) != 1 || len(f.dns) != 1 {
				t.Fatal("duplicate resources")
			}
			f.lost = "DELETE /zones/zone/dns_records"
			if err = p.ReleaseOwnedIngress(context.Background(), got); err == nil {
				t.Fatal("expected uncertainty")
			}
			if err = p.ReleaseOwnedIngress(context.Background(), got); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestOwnedCloudflareAPIPrivacyAndAuthorityLock(t *testing.T) {
	f, srv := newOwnedFake(t)
	p, r := ownedFixture(t, srv, "a")
	f.lost = "POST"
	_, err := p.EnsureOwnedIngress(context.Background(), r)
	if err == nil || strings.Contains(err.Error(), "dummy-private-api") {
		t.Fatal("secret reflected")
	}
	unlock, err := p.lockOwned(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err = p.lockOwned(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lock ignored cancellation", err)
	}
	raw, err := os.ReadFile(ownedstate.Path(p.owned.runtime.StateDir, r))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "dummy-private-api") {
		t.Fatal("credential persisted")
	}
}

func TestOwnedCleanupRecoveryPreservesOperationIdentity(t *testing.T) {
	_, srv := newOwnedFake(t)
	p, r := ownedFixture(t, srv, "a")
	r, err := p.EnsureOwnedIngress(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"stop", "release"} {
		r.OperationID = "new-" + step
		if step == "stop" {
			err = p.StopOwnedIngress(context.Background(), r)
		} else {
			err = p.ReleaseOwnedIngress(context.Background(), r)
		}
		if err != nil {
			t.Fatal(err)
		}
		outcome, err := p.ReconcileOwnedIngress(context.Background(), ingress.Progress{Step: step, Resource: r})
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Outcome != "applied" || outcome.Resource.OperationID != r.OperationID || outcome.Resource.Session.ID != "" {
			t.Fatalf("bad cleanup recovery: %+v", outcome)
		}
	}
}

func TestOwnedCloudflarePreservesReplacementAfterMissingID(t *testing.T) {
	f, srv := newOwnedFake(t)
	p, r := ownedFixture(t, srv, "a")
	r, err := p.EnsureOwnedIngress(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	replacement := f.dns[r.ProviderState["dns_id"]]
	delete(f.dns, replacement.ID)
	replacement.ID = "replacement"
	f.dns[replacement.ID] = replacement
	if err = p.ReleaseOwnedIngress(context.Background(), r); err == nil {
		t.Fatal("removed tunnel beneath replacement DNS")
	}
	if len(f.tunnels) != 1 || len(f.dns) != 1 {
		t.Fatal("changed replacement resources")
	}
}
