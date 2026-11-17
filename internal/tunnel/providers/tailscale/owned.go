package tailscale

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goliatone/switchboard-hub/internal/safeio"
	"github.com/goliatone/switchboard-hub/internal/tunnel/ownedstate"
	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

type document map[string]json.RawMessage
type ownedConfig struct {
	runtime              ingress.Runtime
	client               *http.Client
	socket, coordination string
	sessionScope         string
	gui                  bool
}

var foregroundLeases sync.Map // daemon/session ID -> cancel; contains no credentials

func (p *Provider) ValidateIngress(_ context.Context, r ingress.Resource, rt ingress.Runtime) error {
	if r.Binding.HTTPSPort != 443 && r.Binding.HTTPSPort != 8443 && r.Binding.HTTPSPort != 10000 {
		return errors.New("unsupported Funnel HTTPS port")
	}
	if r.Binding.Path != "/" || !strings.HasSuffix(r.PublicHost, ".ts.net") {
		return errors.New("owned Funnel requires a dedicated root HTTPS listener")
	}
	if r.Generation == "" {
		return errors.New("owned generation required")
	}
	p.owned.runtime = rt
	return nil
}
func (p *Provider) InitIngressPrivate(ctx context.Context, settings map[string]string, rt ingress.Runtime, secret []byte) error {
	// A failed reinitialization must not keep an earlier transport usable.
	if p.owned.client != nil {
		p.owned.client.CloseIdleConnections()
	}
	p.owned.client = nil
	p.owned.gui = false
	p.owned.sessionScope = ""
	if len(secret) > 0 {
		return errors.New("tailscale LocalAPI does not accept provider credentials")
	}
	coordination := settings["coordination_dir"]
	if !filepath.IsAbs(coordination) {
		return errors.New("configure a shared absolute coordination_dir")
	}
	p.owned.coordination = coordination
	switch settings["local_api_transport"] {
	case "macos-gui":
		return p.initGUI(ctx, settings, rt)
	case "", "unix":
	default:
		return errors.New("unsupported Tailscale LocalAPI transport")
	}
	socket := settings["local_api_socket"]
	if !filepath.IsAbs(socket) || !filepath.IsAbs(coordination) {
		return errors.New("configure local_api_socket and shared coordination_dir; unsupported LocalAPI must not fall back to persistent CLI")
	}
	canonical, err := filepath.EvalSymlinks(socket)
	if err != nil {
		return errors.New("tailscale LocalAPI socket unavailable")
	}
	p.owned.socket = canonical
	p.owned.coordination = coordination
	p.owned.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{ResponseHeaderTimeout: 10 * time.Second, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "unix", canonical)
	}}}
	return nil
}
func (p *Provider) lockOwned(ctx context.Context) (func(), error) {
	if p.owned.client == nil || p.owned.socket == "" {
		return nil, errors.New("owned Tailscale not initialized")
	}
	dir, err := safeio.PrivateDirectory(p.owned.coordination)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(p.owned.socket))
	return safeio.Lock(ctx, filepath.Join(dir, hex.EncodeToString(hash[:])+".lock"))
}
func (p *Provider) node(ctx context.Context, r ingress.Resource) (map[string]string, error) {
	var state struct {
		Version      string
		BackendState string
		Self         struct{ ID, PublicKey, DNSName string }
	}
	if _, err := p.request(ctx, "GET", "/status", nil, "", &state); err != nil {
		return nil, err
	}
	if (p.owned.gui && !supportedGUIBackend(state.Version)) || state.BackendState != "Running" || state.Self.ID == "" || state.Self.PublicKey == "" || strings.TrimSuffix(state.Self.DNSName, ".") != r.PublicHost {
		return nil, errors.New("tailscale node/profile/host is not the requested running endpoint")
	}
	return map[string]string{"node": state.Self.ID, "profile_key": state.Self.PublicKey, "visibility": "public", "lifetime": "foreground"}, nil
}
func (p *Provider) checkNode(ctx context.Context, r ingress.Resource) error {
	node, err := p.node(ctx, r)
	if err != nil {
		return err
	}
	for key, value := range node {
		if r.ProviderState[key] != value {
			return errors.New("tailscale node/profile identity changed")
		}
	}
	return nil
}
func (p *Provider) EnsureOwnedIngress(ctx context.Context, r ingress.Resource) (ingress.Resource, error) {
	unlock, err := p.lockOwned(ctx)
	if err != nil {
		return r, err
	}
	defer unlock()
	node, err := p.node(ctx, r)
	if err != nil {
		return r, err
	}
	saved, exists, err := ownedstate.Load(p.owned.runtime.StateDir, r)
	if err != nil {
		return r, err
	}
	if exists {
		if !sameBinding(saved, r) {
			return r, errors.New("release Tailscale ownership before changing binding")
		}
		if err := p.checkNode(ctx, saved); err != nil {
			return r, err
		}
		saved.OperationID = r.OperationID
		return saved, ownedstate.Save(p.owned.runtime.StateDir, saved)
	}
	config, _, err := p.getConfig(ctx)
	if err != nil {
		return r, err
	}
	if err := portFree(config, r.Binding.HTTPSPort, ""); err != nil {
		return r, err
	}
	r.Binding.EndpointID = "tailscale-" + r.Generation
	r.ProviderState = node
	return r, ownedstate.Save(p.owned.runtime.StateDir, r)
}
func sameBinding(a, b ingress.Resource) bool {
	return a.PublicHost == b.PublicHost && a.LocalURL == b.LocalURL && a.Binding.HTTPSPort == b.Binding.HTTPSPort && a.Binding.Path == b.Binding.Path && a.Owner == b.Owner && a.Generation == b.Generation
}
func (p *Provider) StartOwnedIngress(ctx context.Context, r ingress.Resource) (ingress.Resource, error) {
	unlock, err := p.lockOwned(ctx)
	if err != nil {
		return r, err
	}
	defer unlock()
	if err := p.checkReceipt(r); err != nil {
		return r, err
	}
	if err := p.checkNode(ctx, r); err != nil {
		return r, err
	}
	config, tag, err := p.getConfig(ctx)
	if err != nil {
		return r, err
	}
	if err := portFree(config, r.Binding.HTTPSPort, ""); err != nil {
		return r, err
	}
	id, cancel, err := p.watch(ctx)
	if err != nil {
		return r, err
	}
	keep := false
	defer func() {
		if !keep {
			cancel()
		}
	}()
	r.Session = ingress.SessionIdentity{ID: id, Identity: map[string]string{"generation": r.Generation, "node": r.ProviderState["node"]}}
	// Persist the daemon-issued session ID before publishing any listener.
	if err := ownedstate.Save(p.owned.runtime.StateDir, r); err != nil {
		return r, err
	}
	foreground, err := object(config, "Foreground")
	if err != nil {
		return r, err
	}
	foreground[id] = exposure(r)
	config["Foreground"] = marshal(foreground)
	if _, err := p.request(ctx, "POST", "/serve-config", config, tag, nil); err != nil {
		return r, err
	}
	if err := p.verifyEffective(ctx, r); err != nil {
		return r, err
	}
	keep = true
	return r, nil
}
func (p *Provider) checkReceipt(r ingress.Resource) error {
	saved, found, err := ownedstate.Load(p.owned.runtime.StateDir, r)
	if err != nil {
		return err
	}
	if !found || !sameBinding(saved, r) || saved.Binding.EndpointID != r.Binding.EndpointID || (r.Session.ID != "" && saved.Session.ID != "" && saved.Session.ID != r.Session.ID) {
		return errors.New("missing or changed Tailscale ownership receipt")
	}
	return nil
}
func (p *Provider) StopOwnedIngress(ctx context.Context, r ingress.Resource) error {
	unlock, err := p.lockOwned(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := p.checkReceipt(r); err != nil {
		return err
	}
	if err := p.checkNode(ctx, r); err != nil {
		return err
	}
	config, tag, err := p.getConfig(ctx)
	if err != nil {
		return err
	}
	id := r.Session.ID
	if id == "" {
		return portFree(config, r.Binding.HTTPSPort, "")
	}
	foreground, err := object(config, "Foreground")
	if err != nil {
		return err
	}
	if actual, ok := foreground[id]; ok {
		saved, _, err := ownedstate.Load(p.owned.runtime.StateDir, r)
		if err != nil {
			return err
		}
		if saved.Session.ID != id {
			return errors.New("foreground session lacks durable ownership")
		}
		if !jsonEqual(actual, exposure(r)) {
			return errors.New("owned foreground binding changed; preserve operator edits")
		}
		if err := portFree(config, r.Binding.HTTPSPort, id); err != nil {
			return err
		}
		delete(foreground, id)
		config["Foreground"] = marshal(foreground)
		if _, err := p.request(ctx, "POST", "/serve-config", config, tag, nil); err != nil {
			return err
		}
	}
	if cancel, ok := foregroundLeases.LoadAndDelete(p.leaseScope() + "\x00" + id); ok {
		cancel.(context.CancelFunc)()
	}
	after, _, err := p.getConfig(ctx)
	if err != nil {
		return err
	}
	if err := portFree(after, r.Binding.HTTPSPort, ""); err != nil {
		return err
	}
	r.Session = ingress.SessionIdentity{}
	return ownedstate.Save(p.owned.runtime.StateDir, r)
}
func (p *Provider) ReleaseOwnedIngress(ctx context.Context, r ingress.Resource) error {
	return p.StopOwnedIngress(ctx, r)
}
func (p *Provider) VerifyOwnedIngressSession(ctx context.Context, r ingress.Resource) error {
	if err := p.checkReceipt(r); err != nil {
		return err
	}
	if err := p.checkNode(ctx, r); err != nil {
		return err
	}
	config, _, err := p.getConfig(ctx)
	if err != nil {
		return err
	}
	fg, err := object(config, "Foreground")
	if err != nil {
		return err
	}
	if value, exists := fg[r.Session.ID]; exists && !jsonEqual(value, exposure(r)) {
		return errors.New("foreground session ownership changed")
	}
	return nil
}
func (p *Provider) StatusOwnedIngress(ctx context.Context, r ingress.Resource) (ingress.Status, error) {
	if err := p.checkReceipt(r); err != nil {
		return ingress.Status{}, err
	}
	if err := p.checkNode(ctx, r); err != nil {
		return ingress.Status{}, err
	}
	cfg, _, err := p.getConfig(ctx)
	if err != nil {
		return ingress.Status{}, err
	}
	fg, err := object(cfg, "Foreground")
	if err != nil {
		return ingress.Status{}, err
	}
	value, found := fg[r.Session.ID]
	if !found {
		return ingress.Status{}, nil
	}
	if !jsonEqual(value, exposure(r)) {
		return ingress.Status{}, errors.New("effective binding differs from ownership")
	}
	if err := portFree(cfg, r.Binding.HTTPSPort, r.Session.ID); err != nil {
		return ingress.Status{}, err
	}
	return ingress.Status{Ready: true, SessionID: r.Session.ID}, nil
}
func (p *Provider) ReconcileOwnedIngress(ctx context.Context, progress ingress.Progress) (ingress.RecoveryResult, error) {
	r := progress.Resource
	saved, found, err := ownedstate.Load(p.owned.runtime.StateDir, r)
	if err != nil {
		return ingress.RecoveryResult{}, err
	}
	if !found {
		return ingress.RecoveryResult{Outcome: "not-applied"}, nil
	}
	if !sameBinding(saved, r) {
		return ingress.RecoveryResult{Outcome: "unknown"}, nil
	}
	if err := p.checkNode(ctx, saved); err != nil {
		return ingress.RecoveryResult{}, err
	}
	if progress.Step == "ensure" {
		saved.OperationID = r.OperationID
		return ingress.RecoveryResult{Outcome: "applied", Resource: saved}, nil
	}
	cfg, _, err := p.getConfig(ctx)
	if err != nil {
		return ingress.RecoveryResult{}, err
	}
	fg, err := object(cfg, "Foreground")
	if err != nil {
		return ingress.RecoveryResult{}, err
	}
	if progress.Step == "start" {
		if saved.OperationID != r.OperationID {
			return ingress.RecoveryResult{Outcome: "unknown"}, nil
		}
		if raw, ok := fg[saved.Session.ID]; ok {
			if !jsonEqual(raw, exposure(saved)) {
				return ingress.RecoveryResult{Outcome: "unknown"}, nil
			}
			return ingress.RecoveryResult{Outcome: "applied", Resource: saved}, nil
		}
		if err := portFree(cfg, r.Binding.HTTPSPort, ""); err != nil {
			return ingress.RecoveryResult{}, err
		}
		return ingress.RecoveryResult{Outcome: "not-applied"}, nil
	}
	if err := portFree(cfg, r.Binding.HTTPSPort, ""); err == nil {
		r.Session = ingress.SessionIdentity{}
		return ingress.RecoveryResult{Outcome: "applied", Resource: r}, nil
	}
	if raw, ok := fg[r.Session.ID]; ok && jsonEqual(raw, exposure(r)) {
		return ingress.RecoveryResult{Outcome: "not-applied"}, nil
	}
	return ingress.RecoveryResult{Outcome: "unknown"}, nil
}
func (p *Provider) verifyEffective(ctx context.Context, r ingress.Resource) error {
	status, err := p.StatusOwnedIngress(ctx, r)
	if err != nil {
		return err
	}
	if !status.Ready {
		return errors.New("foreground exposure not effective")
	}
	return nil
}
func exposure(r ingress.Resource) json.RawMessage {
	port := strconv.Itoa(r.Binding.HTTPSPort)
	host := net.JoinHostPort(r.PublicHost, port)
	return marshal(map[string]any{"TCP": map[string]any{port: map[string]bool{"HTTPS": true}}, "Web": map[string]any{host: map[string]any{"Handlers": map[string]any{"/": map[string]string{"Proxy": r.LocalURL}}}}, "AllowFunnel": map[string]bool{host: true}})
}
func marshal(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }
func object(d document, key string) (document, error) {
	out := document{}
	raw := d[key]
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return nil, errors.New("unsupported Tailscale configuration shape")
	}
	return out, nil
}
func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}
func portFree(d document, port int, except string) error { return portFreeDepth(d, port, except, 0) }
func portFreeDepth(d document, port int, except string, depth int) error {
	if depth > 8 {
		return errors.New("unsupported nested configuration depth")
	}
	tcp, err := object(d, "TCP")
	if err != nil {
		return err
	}
	if _, exists := tcp[strconv.Itoa(port)]; exists {
		return errors.New("HTTPS port already owned by another exposure")
	}
	for _, key := range []string{"Web", "AllowFunnel"} {
		m, err := object(d, key)
		if err != nil {
			return err
		}
		for host := range m {
			_, p, err := net.SplitHostPort(host)
			if err != nil {
				return errors.New("unknown binding in daemon config")
			}
			if p == strconv.Itoa(port) {
				return errors.New("private/public port collision")
			}
		}
	}
	for _, key := range []string{"Foreground", "Services"} {
		children, err := object(d, key)
		if err != nil {
			return err
		}
		for id, raw := range children {
			if key == "Foreground" && id == except {
				continue
			}
			var child document
			if json.Unmarshal(raw, &child) != nil {
				return errors.New("unknown nested configuration")
			}
			if err := portFreeDepth(child, port, "", depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

var _ ingress.OwnedProvider = (*Provider)(nil)
var _ ingress.RecoveryProvider = (*Provider)(nil)
