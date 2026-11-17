package cloudflare

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/goliatone/switchboard-hub/internal/safeio"
	"github.com/goliatone/switchboard-hub/internal/tunnel/ownedstate"
	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

type ownedSettings struct {
	runtime                                            ingress.Runtime
	api                                                ownedAPI
	account, zone, authority, coordination, supervisor string
}

var safeIdentifier = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func (p *Provider) ValidateIngress(ctx context.Context, r ingress.Resource, rt ingress.Runtime) error {
	if r.Generation == "" || r.OperationID == "" || r.Binding.Path != "/" || r.Binding.HTTPSPort != 443 || !filepath.IsAbs(rt.HelperPath) {
		return errors.New("cloudflare requires an owned dedicated root HTTPS endpoint and absolute helper")
	}
	probe := exec.CommandContext(ctx, rt.HelperPath, "tunnel", "run", "--help") // #nosec G204 -- explicitly configured helper, fixed capability probe.
	probe.Env = connectorEnvironment(rt.RuntimeDir)
	raw, err := probe.Output()
	if err != nil || !strings.Contains(string(raw), "--token-file") {
		return errors.New("cloudflared token-file capability required")
	}
	return nil
}
func (p *Provider) InitIngressPrivate(ctx context.Context, settings map[string]string, rt ingress.Runtime, secret []byte) error {
	if settings["ownership_mode"] != "single-writer" || !safeIdentifier.MatchString(settings["authority_id"]) || !safeIdentifier.MatchString(settings["account_id"]) || !safeIdentifier.MatchString(settings["zone_id"]) || len(secret) == 0 {
		return errors.New("configure single-writer ownership, authority/account/zone and private credential resolver")
	}
	if !filepath.IsAbs(settings["coordination_dir"]) || !filepath.IsAbs(settings["supervisor_path"]) {
		return errors.New("shared private coordination_dir and connector supervisor_path required")
	}
	if settings["api_base_url"] != "" {
		return errors.New("owned Cloudflare uses the fixed HTTPS API endpoint")
	}
	dir, err := safeio.PrivateDirectory(settings["coordination_dir"])
	if err != nil {
		return err
	}
	probe := exec.CommandContext(ctx, settings["supervisor_path"], "--protocol") // #nosec G204 -- explicit supervisor executable, fixed capability query.
	probe.Env = connectorEnvironment(rt.RuntimeDir)
	raw, err := probe.Output()
	if err != nil || strings.TrimSpace(string(raw)) != "switchboard-connector-v1" {
		return errors.New("compatible connector supervisor required")
	}
	rt.RuntimeDir, err = safeio.PrivateDirectory(rt.RuntimeDir)
	if err != nil {
		return err
	}
	rt.StateDir, err = safeio.PrivateDirectory(rt.StateDir)
	if err != nil {
		return err
	}
	_, socket, _ := supervisorPaths(rt, "preflight")
	if len(socket) > 103 {
		return errors.New("use a shorter private runtime directory for connector sockets")
	}
	p.owned = ownedSettings{runtime: rt, api: newOwnedAPI(secret), account: settings["account_id"], zone: settings["zone_id"], authority: settings["authority_id"], coordination: dir, supervisor: settings["supervisor_path"]}
	return nil
}
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func (p *Provider) lockOwned(ctx context.Context) (func(), error) {
	if p.owned.authority == "" || len(p.owned.api.secret) == 0 {
		return nil, errors.New("single-writer Cloudflare not initialized")
	}
	base := filepath.Join(p.owned.coordination, "cloudflare-"+digest(p.owned.account))
	unlock, err := safeio.Lock(ctx, base+".lock")
	if err != nil {
		return nil, err
	}
	var authority string
	err = readPrivateJSON(base+".json", &authority)
	if errors.Is(err, os.ErrNotExist) {
		err = safeio.AtomicWriteFile(base+".json", []byte(jsonText(p.owned.authority)), 0o600)
		authority = p.owned.authority
	}
	if err != nil || authority != p.owned.authority {
		unlock()
		return nil, errors.New("cloudflare account is enrolled with another authority or unreadable coordination state")
	}
	return unlock, nil
}
func sameOwnedBinding(a, b ingress.Resource) bool {
	return a.Name == b.Name && a.Owner == b.Owner && a.Generation == b.Generation && a.Provider == b.Provider && a.PublicHost == b.PublicHost && a.LocalURL == b.LocalURL && a.Binding.Path == b.Binding.Path && a.Binding.HTTPSPort == b.Binding.HTTPSPort
}
func (p *Provider) load(r ingress.Resource) (ingress.Resource, bool, error) {
	s, found, err := ownedstate.Load(p.owned.runtime.StateDir, r)
	if err != nil || !found {
		return s, found, err
	}
	if !sameOwnedBinding(s, r) || (r.Binding.EndpointID != "" && s.Binding.EndpointID != r.Binding.EndpointID) || s.ProviderState["account"] != p.owned.account || s.ProviderState["zone"] != p.owned.zone || s.ProviderState["authority"] != p.owned.authority || s.ProviderState["coordination"] != p.owned.coordination {
		return s, false, errors.New("cloudflare ownership, binding or writer authority changed")
	}
	return s, true, nil
}
func (p *Provider) save(r ingress.Resource) error {
	return ownedstate.Save(p.owned.runtime.StateDir, r)
}
func (p *Provider) EnsureOwnedIngress(ctx context.Context, r ingress.Resource) (ingress.Resource, error) {
	unlock, err := p.lockOwned(ctx)
	if err != nil {
		return r, err
	}
	defer unlock()
	s, found, err := p.load(r)
	if err != nil {
		return r, err
	}
	if found {
		s.OperationID = r.OperationID
		r = s
		if r.ProviderState["released"] == "true" {
			return r, errors.New("released generation cannot be reused")
		}
	} else {
		if r.Binding.EndpointID != "" {
			return r, errors.New("cannot adopt an existing Cloudflare tunnel")
		}
		var records []ownedDNS
		if err = p.owned.api.call(ctx, "GET", strings.TrimSuffix(p.dnsPath(""), "/")+"?name="+url.QueryEscape(r.PublicHost), nil, &records); err != nil {
			return r, err
		}
		if len(records) != 0 {
			return r, errors.New("unowned DNS collision; choose an unused hostname")
		}
		r.ProviderState = map[string]string{"account": p.owned.account, "zone": p.owned.zone, "authority": p.owned.authority, "coordination": p.owned.coordination, "creation_operation": r.OperationID, "tunnel_name": "switchboard-" + digest(r.Generation+"\x00"+r.OperationID), "dns_marker": "switchboard:" + digest(r.Generation+"\x00"+r.OperationID)}
		if err = p.save(r); err != nil {
			return r, err
		}
	}
	if err = p.ensureTunnel(ctx, &r); err != nil {
		return r, err
	}
	if err = p.ensureConfiguration(ctx, &r); err != nil {
		return r, err
	}
	if err = p.ensureDNS(ctx, &r); err != nil {
		return r, err
	}
	r.ProviderState["ready"] = "true"
	return r, p.save(r)
}
func (p *Provider) ensureTunnel(ctx context.Context, r *ingress.Resource) error {
	if r.Binding.EndpointID != "" {
		return p.checkTunnel(ctx, *r)
	}
	name := r.ProviderState["tunnel_name"]
	var matches []ownedTunnel
	if err := p.owned.api.call(ctx, "GET", strings.TrimSuffix(p.tunnelPath(""), "/")+"?is_deleted=false&name="+url.QueryEscape(name), nil, &matches); err != nil {
		return err
	}
	var t ownedTunnel
	if r.ProviderState["tunnel_attempt"] == "pending" {
		// Durable absent preflight + unpredictable creation intent + exclusive writer
		// authority, not a hostname/name match alone, permits lost-reply recovery.
		if len(matches) != 1 || matches[0].Name != name || matches[0].ConfigSource != "cloudflare" || matches[0].CreatedAt == "" {
			return errors.New("tunnel creation outcome uncertain; preserve attempt, do not recreate")
		}
		t = matches[0]
	} else {
		if len(matches) > 0 {
			return errors.New("unowned tunnel collision")
		}
		r.ProviderState["tunnel_attempt"] = "pending"
		if err := p.save(*r); err != nil {
			return err
		}
		if err := p.owned.api.call(ctx, "POST", strings.TrimSuffix(p.tunnelPath(""), "/"), map[string]string{"name": name, "config_src": "cloudflare"}, &t); err != nil {
			return p.recordRejection(r, "tunnel_attempt", err)
		}
	}
	if !safeIdentifier.MatchString(t.ID) || t.Name != name || t.ConfigSource != "cloudflare" || t.CreatedAt == "" || t.DeletedAt != nil {
		return errors.New("created tunnel identity unavailable")
	}
	r.Binding.EndpointID = t.ID
	r.ProviderState["tunnel_snapshot"] = jsonText(t)
	r.ProviderState["tunnel_attempt"] = "confirmed"
	return p.save(*r)
}
func (p *Provider) checkTunnel(ctx context.Context, r ingress.Resource) error {
	var t ownedTunnel
	if err := p.owned.api.call(ctx, "GET", p.tunnelPath(r.Binding.EndpointID), nil, &t); err != nil {
		return err
	}
	if jsonText(t) != r.ProviderState["tunnel_snapshot"] {
		return errors.New("owned tunnel changed; preserve resource")
	}
	return nil
}
func (p *Provider) ensureConfiguration(ctx context.Context, r *ingress.Resource) error {
	if r.ProviderState["config_snapshot"] != "" {
		return p.checkConfiguration(ctx, *r)
	}
	if r.ProviderState["config_attempt"] != "pending" {
		r.ProviderState["config_attempt"] = "pending"
		if err := p.save(*r); err != nil {
			return err
		}
		if err := p.owned.api.call(ctx, "PUT", p.tunnelPath(r.Binding.EndpointID)+"/configurations", configFor(r.PublicHost, r.LocalURL), nil); err != nil {
			return err
		}
	}
	var c ownedConfiguration
	if err := p.owned.api.call(ctx, "GET", p.tunnelPath(r.Binding.EndpointID)+"/configurations", nil, &c); err != nil {
		return err
	}
	// Reject extra ingress rules or nonempty origin/warp options, including on
	// an interrupted configuration write; never repair by overwriting drift.
	var actual map[string]json.RawMessage
	if json.Unmarshal(c.Config, &actual) != nil {
		return errors.New("unknown tunnel configuration")
	}
	expected := configFor(r.PublicHost, r.LocalURL)["config"].(map[string]any)
	var rules any
	if json.Unmarshal(actual["ingress"], &rules) != nil || jsonText(rules) != jsonText(expected["ingress"]) {
		return errors.New("tunnel configuration outcome uncertain or changed")
	}
	for k, v := range actual {
		if k != "ingress" && string(v) != "{}" && string(v) != "null" {
			return errors.New("unexpected tunnel configuration settings")
		}
	}
	r.ProviderState["config_snapshot"] = jsonText(c)
	r.ProviderState["config_attempt"] = "confirmed"
	return p.save(*r)
}
func (p *Provider) checkConfiguration(ctx context.Context, r ingress.Resource) error {
	var c ownedConfiguration
	if err := p.owned.api.call(ctx, "GET", p.tunnelPath(r.Binding.EndpointID)+"/configurations", nil, &c); err != nil {
		return err
	}
	if jsonText(c) != r.ProviderState["config_snapshot"] {
		return errors.New("owned tunnel configuration changed; preserve resource")
	}
	return nil
}
func (p *Provider) ensureDNS(ctx context.Context, r *ingress.Resource) error {
	if r.ProviderState["dns_id"] != "" {
		return p.checkDNS(ctx, *r)
	}
	var records []ownedDNS
	if err := p.owned.api.call(ctx, "GET", strings.TrimSuffix(p.dnsPath(""), "/")+"?name="+url.QueryEscape(r.PublicHost), nil, &records); err != nil {
		return err
	}
	desired := ownedDNS{Type: "CNAME", Name: r.PublicHost, Content: r.Binding.EndpointID + ".cfargotunnel.com", Proxied: true, TTL: 1, Comment: r.ProviderState["dns_marker"]}
	var d ownedDNS
	if r.ProviderState["dns_attempt"] == "pending" {
		if len(records) != 1 {
			return errors.New("DNS creation outcome uncertain; do not recreate")
		}
		d = records[0]
	} else {
		if len(records) != 0 {
			return errors.New("unowned DNS collision; preserve record")
		}
		r.ProviderState["dns_attempt"] = "pending"
		if err := p.save(*r); err != nil {
			return err
		}
		if err := p.owned.api.call(ctx, "POST", strings.TrimSuffix(p.dnsPath(""), "/"), desired, &d); err != nil {
			return p.recordRejection(r, "dns_attempt", err)
		}
	}
	if !safeIdentifier.MatchString(d.ID) || d.Type != desired.Type || d.Name != desired.Name || d.Content != desired.Content || d.Proxied != desired.Proxied || d.TTL != 1 || d.Comment != desired.Comment || d.CreatedAt == "" {
		return errors.New("DNS creation identity unavailable or changed")
	}
	r.ProviderState["dns_id"] = d.ID
	r.ProviderState["dns_snapshot"] = jsonText(d)
	r.ProviderState["dns_attempt"] = "confirmed"
	return p.save(*r)
}
func (p *Provider) checkDNS(ctx context.Context, r ingress.Resource) error {
	var d ownedDNS
	if err := p.owned.api.call(ctx, "GET", p.dnsPath(r.ProviderState["dns_id"]), nil, &d); err != nil {
		return err
	}
	if jsonText(d) != r.ProviderState["dns_snapshot"] {
		return errors.New("owned DNS record changed; preserve resource")
	}
	return nil
}
func (p *Provider) checkReady(ctx context.Context, r ingress.Resource) error {
	if r.ProviderState["ready"] != "true" || r.ProviderState["released"] == "true" {
		return errors.New("cloudflare endpoint is not provisioned")
	}
	if err := p.checkTunnel(ctx, r); err != nil {
		return err
	}
	if err := p.checkConfiguration(ctx, r); err != nil {
		return err
	}
	return p.checkDNS(ctx, r)
}
func (p *Provider) ReleaseOwnedIngress(ctx context.Context, r ingress.Resource) error {
	unlock, err := p.lockOwned(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	s, found, err := p.load(r)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("missing Cloudflare ownership receipt")
	}
	s.OperationID = r.OperationID
	r = s
	if err = p.stopConnector(ctx, &r); err != nil {
		return err
	}
	if r.ProviderState["released"] == "true" {
		return nil
	}
	// Validate all surviving resources before the first destructive step.
	tunnelErr := p.checkTunnel(ctx, r)
	if tunnelErr != nil && !absent(tunnelErr) {
		return tunnelErr
	}
	if absent(tunnelErr) {
		if err = p.confirmAbsent(ctx, r, false); err != nil {
			return err
		}
	}
	if tunnelErr == nil {
		if err = p.checkConfiguration(ctx, r); err != nil {
			return err
		}
		var connections []json.RawMessage
		if err = p.owned.api.call(ctx, "GET", p.tunnelPath(r.Binding.EndpointID)+"/connections", nil, &connections); err != nil {
			return err
		}
		if len(connections) > 0 {
			return errors.New("tunnel still has connectors; never force disconnect unknown sessions")
		}
	}
	if err = p.releaseDNS(ctx, &r); err != nil {
		return err
	}
	if tunnelErr == nil {
		// Recheck identity/config after DNS work. The exclusive-writer contract,
		// not a nonexistent If-Match header, protects this read/delete interval.
		if err = p.checkTunnel(ctx, r); err != nil {
			return err
		}
		if err = p.checkConfiguration(ctx, r); err != nil {
			return err
		}
		if err = p.owned.api.call(ctx, "DELETE", p.tunnelPath(r.Binding.EndpointID), nil, nil); err != nil && !absent(err) {
			return err
		}
	}
	r.ProviderState["released"] = "true"
	return p.save(r)
}

var _ ingress.OwnedProvider = (*Provider)(nil)
var _ ingress.RecoveryProvider = (*Provider)(nil)

func (p *Provider) releaseDNS(ctx context.Context, r *ingress.Resource) error {
	if r.ProviderState["dns_deleted"] == "true" {
		return nil
	}
	err := p.checkDNS(ctx, *r)
	if absent(err) {
		if e := p.confirmAbsent(ctx, *r, true); e != nil {
			return e
		}
	} else if err != nil {
		return err
	}
	if err == nil {
		if err = p.owned.api.call(ctx, "DELETE", p.dnsPath(r.ProviderState["dns_id"]), nil, nil); err != nil && !absent(err) {
			return err
		}
	}
	r.ProviderState["dns_deleted"] = "true"
	return p.save(*r)
}

func (p *Provider) recordRejection(r *ingress.Resource, key string, cause error) error {
	var status ownedAPIError
	if errors.As(cause, &status) && (status == 400 || status == 401 || status == 403 || status == 422) {
		r.ProviderState[key] = "rejected"
		if err := p.save(*r); err != nil {
			return err
		}
	}
	return cause
}

// A per-ID 404 alone is insufficient evidence of absence. Require an authorized
// scoped listing and preserve same-name replacements instead of deleting their
// underlying tunnel. This also avoids treating hidden/unauthorized IDs as gone.
func (p *Provider) confirmAbsent(ctx context.Context, r ingress.Resource, dns bool) error {
	if dns {
		var records []ownedDNS
		if err := p.owned.api.call(ctx, "GET", strings.TrimSuffix(p.dnsPath(""), "/")+"?name="+url.QueryEscape(r.PublicHost), nil, &records); err != nil {
			return err
		}
		if len(records) > 0 {
			return errors.New("DNS absence is ambiguous or hostname has a replacement; preserve it")
		}
	} else {
		var records []ownedTunnel
		if err := p.owned.api.call(ctx, "GET", strings.TrimSuffix(p.tunnelPath(""), "/")+"?is_deleted=false&name="+url.QueryEscape(r.ProviderState["tunnel_name"]), nil, &records); err != nil {
			return err
		}
		if len(records) > 0 {
			return errors.New("tunnel absence is ambiguous or name has a replacement; preserve it")
		}
	}
	return nil
}
