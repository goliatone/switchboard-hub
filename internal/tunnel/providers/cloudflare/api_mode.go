package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/goliatone/switchboard-hub/internal/diag"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
)

const (
	providerModeCLI = "cli"
	providerModeAPI = "api"

	defaultCloudflareAPIBaseURL  = "https://api.cloudflare.com/client/v4"
	defaultCloudflareAPITokenEnv = "SWITCHD_CF_API_TOKEN"
)

type providerRuntimeConfig struct {
	Mode        string
	AccountID   string
	ZoneID      string
	BaseDomain  string
	APITokenEnv string
	APIToken    string
	APIBaseURL  string
	DNSProxied  bool
}

type cloudflareAPIClient struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client
}

type cloudflareAPIEnvelope struct {
	Success bool               `json:"success"`
	Errors  []cloudflareAPIMsg `json:"errors"`
	Result  json.RawMessage    `json:"result"`
}

type cloudflareAPIMsg struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cloudflareAPIError struct {
	StatusCode int
	Messages   []string
}

func (e *cloudflareAPIError) Error() string {
	if e == nil {
		return ""
	}
	parts := []string{fmt.Sprintf("cloudflare api status %d", e.StatusCode)}
	if len(e.Messages) > 0 {
		parts = append(parts, strings.Join(e.Messages, "; "))
	}
	return strings.Join(parts, ": ")
}

type apiTunnel struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Connections []apiConnection `json:"connections,omitempty"`
}

type apiConnection struct {
	ID string `json:"id"`
}

type apiDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
}

func resolveProviderRuntimeConfig(cfg tunnel.ProviderConfig) (providerRuntimeConfig, error) {
	out := providerRuntimeConfig{
		Mode:        providerModeCLI,
		APITokenEnv: defaultCloudflareAPITokenEnv,
		APIBaseURL:  defaultCloudflareAPIBaseURL,
		DNSProxied:  true,
	}
	mode := strings.ToLower(strings.TrimSpace(providerConfigValue(cfg, "mode")))
	if mode != "" {
		if mode != providerModeCLI && mode != providerModeAPI {
			return out, &initError{
				code: errCodeAPIConfigInvalid,
				what: fmt.Sprintf("invalid cloudflare mode %q", mode),
				why:  "cloudflare provider mode must be either `cli` or `api`",
				nextSteps: []string{
					"Set mode with: switchd tunnel init --provider cloudflare --mode cli|api",
				},
			}
		}
		out.Mode = mode
	}
	if out.Mode != providerModeAPI {
		return out, nil
	}

	out.AccountID = strings.TrimSpace(providerConfigValue(cfg, "account_id"))
	out.ZoneID = strings.TrimSpace(providerConfigValue(cfg, "zone_id", "zone"))
	out.BaseDomain = strings.TrimSpace(providerConfigValue(cfg, "base_domain"))
	if env := strings.TrimSpace(providerConfigValue(cfg, "api_token_env")); env != "" {
		out.APITokenEnv = env
	}
	if base := strings.TrimSpace(providerConfigValue(cfg, "api_base_url")); base != "" {
		out.APIBaseURL = strings.TrimRight(base, "/")
	}
	if raw := strings.TrimSpace(providerConfigValue(cfg, "dns_proxied")); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return out, &initError{
				code: errCodeAPIConfigInvalid,
				what: "cloudflare dns_proxied has an invalid value",
				why:  "dns_proxied must be true or false when provided",
				checks: []string{
					"read tunnels.providers.cloudflare.values.dns_proxied",
				},
				nextSteps: []string{
					"Use --dns-proxied true|false or remove the key to use the default",
				},
				cause: diag.SanitizeError(err),
			}
		}
		out.DNSProxied = v
	}
	if out.AccountID == "" || out.ZoneID == "" {
		return out, &initError{
			code: errCodeAPIConfigInvalid,
			what: "missing required cloudflare API configuration",
			why:  "account_id and zone_id are required for Cloudflare API mode",
			checks: []string{
				fmt.Sprintf("account_id set=%t", out.AccountID != ""),
				fmt.Sprintf("zone_id set=%t", out.ZoneID != ""),
			},
			nextSteps: []string{
				"Run: switchd tunnel init --provider cloudflare --mode api --account-id <id> --zone-id <id>",
			},
		}
	}
	out.APIToken = strings.TrimSpace(os.Getenv(out.APITokenEnv))
	if out.APIToken == "" {
		return out, &initError{
			code: errCodeAPITokenMissing,
			what: "cloudflare API token is missing",
			why:  "API mode needs a bearer token to manage tunnels and DNS",
			checks: []string{
				"mode=api",
				fmt.Sprintf("env %s is not set", out.APITokenEnv),
			},
			nextSteps: []string{
				fmt.Sprintf("Export token: export %s=<cloudflare_api_token>", out.APITokenEnv),
				"Re-run: switchd tunnel init --provider cloudflare --mode api",
			},
		}
	}
	return out, nil
}

func (p *Provider) initAPIMode(ctx context.Context, cfg providerRuntimeConfig) error {
	client := newCloudflareAPIClient(cfg)
	if _, err := client.listTunnels(ctx, cfg.AccountID); err != nil {
		return classifyCloudflareAPIInitError(err, cfg)
	}
	return nil
}

func (p *Provider) ensureEndpointAPIMode(ctx context.Context, req tunnel.EndpointRequest) (tunnel.Endpoint, error) {
	if strings.TrimSpace(req.Name) == "" {
		return tunnel.Endpoint{}, errors.New("endpoint name is required")
	}
	publicHost := strings.ToLower(strings.TrimSpace(req.PublicHost))
	if publicHost == "" {
		return tunnel.Endpoint{}, errors.New("public host is required")
	}
	if strings.TrimSpace(req.LocalURL) == "" {
		return tunnel.Endpoint{}, errors.New("local url is required")
	}
	if err := p.Capabilities().ValidateOAuthUse(publicHost); err != nil {
		return tunnel.Endpoint{}, err
	}
	cfg := p.getRuntimeConfig()
	client := newCloudflareAPIClient(cfg)
	tunnelName := tunnelNameFrom(req.Name)
	tunnelID, err := p.ensureAPITunnel(ctx, client, cfg, tunnelName)
	if err != nil {
		return tunnel.Endpoint{}, err
	}
	if err := p.upsertAPITunnelIngress(ctx, client, cfg, tunnelID, publicHost, req.LocalURL); err != nil {
		return tunnel.Endpoint{}, err
	}
	target := tunnelID + ".cfargotunnel.com"
	if err := p.ensureAPIDNSCNAME(ctx, client, cfg, publicHost, target); err != nil {
		return tunnel.Endpoint{}, err
	}
	return tunnel.Endpoint{
		ID:       tunnelID,
		Provider: providerName,
		Name:     tunnelName,
		Host:     publicHost,
		Metadata: req.Metadata,
	}, nil
}

func (p *Provider) startAPIMode(ctx context.Context, req tunnel.StartRequest) (tunnel.Session, error) {
	if strings.TrimSpace(req.Endpoint.ID) == "" {
		return tunnel.Session{}, errors.New("endpoint id is required")
	}
	if strings.TrimSpace(req.LocalURL) == "" {
		return tunnel.Session{}, errors.New("local url is required")
	}
	cfg := p.getRuntimeConfig()
	client := newCloudflareAPIClient(cfg)
	host := strings.ToLower(strings.TrimSpace(req.Endpoint.Host))
	if host != "" {
		if err := p.upsertAPITunnelIngress(ctx, client, cfg, req.Endpoint.ID, host, req.LocalURL); err != nil {
			return tunnel.Session{}, err
		}
	}
	token, err := client.getTunnelToken(ctx, cfg.AccountID, req.Endpoint.ID)
	if err != nil {
		return tunnel.Session{}, fmt.Errorf("fetch cloudflare tunnel token for %q: %w", req.Endpoint.ID, err)
	}

	proc, err := p.start("cloudflared", "tunnel", "run", "--token", token)
	if err != nil {
		return tunnel.Session{}, fmt.Errorf("start cloudflare tunnel %q: %w", req.Endpoint.ID, err)
	}

	startedAt := p.now().UTC()
	sessionID := fmt.Sprintf("%s-%d", req.Endpoint.ID, proc.PID())
	state := sessionState{
		endpointID: req.Endpoint.ID,
		process:    proc,
		startedAt:  startedAt,
	}

	p.mu.Lock()
	p.sessions[sessionID] = state
	p.mu.Unlock()

	return tunnel.Session{
		ID:         sessionID,
		Provider:   providerName,
		EndpointID: req.Endpoint.ID,
		PID:        proc.PID(),
		StartedAt:  startedAt,
		Metadata: map[string]string{
			"local_url": req.LocalURL,
		},
	}, nil
}

func (p *Provider) removeEndpointAPIMode(ctx context.Context, endpointID string) error {
	endpointID = strings.TrimSpace(endpointID)
	if endpointID == "" {
		return errors.New("endpoint id is required")
	}
	cfg := p.getRuntimeConfig()
	client := newCloudflareAPIClient(cfg)
	if err := client.deleteTunnel(ctx, cfg.AccountID, endpointID); err != nil {
		return fmt.Errorf("delete cloudflare tunnel %q: %w", endpointID, err)
	}
	return nil
}

func (p *Provider) statusAPIMode(ctx context.Context, endpointID string) (tunnel.EndpointStatus, error) {
	cfg := p.getRuntimeConfig()
	client := newCloudflareAPIClient(cfg)
	tun, err := client.getTunnel(ctx, cfg.AccountID, endpointID)
	if err != nil {
		return tunnel.EndpointStatus{}, fmt.Errorf("cloudflare tunnel info for %q failed: %w", endpointID, err)
	}
	if len(tun.Connections) > 0 {
		return tunnel.EndpointStatus{
			Ready: true,
			Endpoint: tunnel.Endpoint{
				ID:       endpointID,
				Provider: providerName,
			},
			Message: fmt.Sprintf("cloudflare tunnel has %d active connector(s)", len(tun.Connections)),
		}, nil
	}
	return tunnel.EndpointStatus{
		Ready: false,
		Endpoint: tunnel.Endpoint{
			ID:       endpointID,
			Provider: providerName,
		},
		Message: "cloudflare tunnel exists but has no active connectors",
	}, nil
}

func (p *Provider) ensureAPITunnel(ctx context.Context, client *cloudflareAPIClient, cfg providerRuntimeConfig, tunnelName string) (string, error) {
	tunnels, err := client.listTunnels(ctx, cfg.AccountID)
	if err != nil {
		return "", fmt.Errorf("list cloudflare tunnels via api: %w", err)
	}
	for _, t := range tunnels {
		if strings.EqualFold(strings.TrimSpace(t.Name), tunnelName) {
			return strings.TrimSpace(t.ID), nil
		}
	}
	created, err := client.createTunnel(ctx, cfg.AccountID, tunnelName)
	if err != nil {
		return "", fmt.Errorf("create cloudflare tunnel %q via api: %w", tunnelName, err)
	}
	if strings.TrimSpace(created.ID) == "" {
		return "", fmt.Errorf("create cloudflare tunnel %q via api returned empty id", tunnelName)
	}
	return strings.TrimSpace(created.ID), nil
}

func (p *Provider) upsertAPITunnelIngress(ctx context.Context, client *cloudflareAPIClient, cfg providerRuntimeConfig, tunnelID, host, service string) error {
	if err := client.putTunnelIngressConfig(ctx, cfg.AccountID, tunnelID, host, service); err != nil {
		return fmt.Errorf("configure cloudflare tunnel ingress for %q: %w", host, err)
	}
	return nil
}

func (p *Provider) ensureAPIDNSCNAME(ctx context.Context, client *cloudflareAPIClient, cfg providerRuntimeConfig, host, target string) error {
	records, err := client.listDNSRecordsByName(ctx, cfg.ZoneID, host)
	if err != nil {
		return fmt.Errorf("query cloudflare dns for %q: %w", host, err)
	}
	switch len(records) {
	case 0:
		if err := client.createDNSRecord(ctx, cfg.ZoneID, host, target, cfg.DNSProxied); err != nil {
			return fmt.Errorf("create cloudflare dns cname for %q: %w", host, err)
		}
		return nil
	case 1:
		r := records[0]
		if strings.ToUpper(strings.TrimSpace(r.Type)) != "CNAME" {
			return &initError{
				code: errCodeAPIDNSConflict,
				what: fmt.Sprintf("dns record %q already exists but is not a CNAME", host),
				why:  "Cloudflare tunnel hostnames must map to a CNAME pointing to the tunnel target",
				checks: []string{
					fmt.Sprintf("existing record type=%s content=%s", r.Type, r.Content),
				},
				nextSteps: []string{
					fmt.Sprintf("Delete or rename existing DNS record for %s, then re-run app expose", host),
				},
			}
		}
		if !strings.EqualFold(strings.TrimSpace(r.Content), target) {
			return &initError{
				code: errCodeAPIDNSConflict,
				what: fmt.Sprintf("dns cname %q already points to %q", host, r.Content),
				why:  "switchd needs this host to point to the Cloudflare tunnel target",
				checks: []string{
					fmt.Sprintf("expected content=%s", target),
					fmt.Sprintf("actual content=%s", r.Content),
				},
				nextSteps: []string{
					fmt.Sprintf("Update DNS record %s to %s, or choose a different --public-host", host, target),
				},
			}
		}
		if r.Proxied != cfg.DNSProxied {
			if err := client.updateDNSRecord(ctx, cfg.ZoneID, r.ID, host, target, cfg.DNSProxied); err != nil {
				return fmt.Errorf("update cloudflare dns cname proxy setting for %q: %w", host, err)
			}
		}
		return nil
	default:
		return &initError{
			code: errCodeAPIDNSConflict,
			what: fmt.Sprintf("multiple DNS records already exist for %q", host),
			why:  "switchd expects exactly one DNS record per exposed tunnel host",
			checks: []string{
				fmt.Sprintf("record count=%d", len(records)),
			},
			nextSteps: []string{
				fmt.Sprintf("Clean up duplicate DNS records for %s, then re-run app expose", host),
			},
		}
	}
}

func classifyCloudflareAPIInitError(err error, cfg providerRuntimeConfig) error {
	var apiErr *cloudflareAPIError
	if errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden) {
		return &initError{
			code: errCodeAPIAuthFailed,
			what: "cloudflare API authentication failed",
			why:  "the configured token is invalid or missing required permissions",
			checks: []string{
				fmt.Sprintf("api_token_env=%s", cfg.APITokenEnv),
				fmt.Sprintf("account_id=%s", cfg.AccountID),
				fmt.Sprintf("zone_id=%s", cfg.ZoneID),
			},
			nextSteps: []string{
				"Create a Cloudflare token with Tunnel + DNS edit permissions",
				fmt.Sprintf("Export token: export %s=<token>", cfg.APITokenEnv),
				"Re-run: switchd tunnel init --provider cloudflare --mode api",
			},
			cause: err,
		}
	}
	return &initError{
		code: errCodeAPIRequestFailed,
		what: "cloudflare API preflight failed",
		why:  "switchd could not verify tunnel access using Cloudflare API",
		checks: []string{
			fmt.Sprintf("api_base_url=%s", cfg.APIBaseURL),
			fmt.Sprintf("account_id=%s", cfg.AccountID),
		},
		nextSteps: []string{
			"Verify account_id/zone_id values",
			"Verify token scope and network access to Cloudflare API",
			"Retry: switchd tunnel init --provider cloudflare --mode api",
		},
		cause: err,
	}
}

func newCloudflareAPIClient(cfg providerRuntimeConfig) *cloudflareAPIClient {
	base := strings.TrimSpace(cfg.APIBaseURL)
	if base == "" {
		base = defaultCloudflareAPIBaseURL
	}
	return &cloudflareAPIClient{
		baseURL:    strings.TrimRight(base, "/"),
		apiToken:   strings.TrimSpace(cfg.APIToken),
		httpClient: &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *cloudflareAPIClient) listTunnels(ctx context.Context, accountID string) ([]apiTunnel, error) {
	var out []apiTunnel
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel?is_deleted=false&per_page=1000", url.PathEscape(strings.TrimSpace(accountID)))
	if err := c.request(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *cloudflareAPIClient) createTunnel(ctx context.Context, accountID, name string) (apiTunnel, error) {
	var out apiTunnel
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel", url.PathEscape(strings.TrimSpace(accountID)))
	body := map[string]any{
		"name":       strings.TrimSpace(name),
		"config_src": "cloudflare",
	}
	if err := c.request(ctx, http.MethodPost, path, body, &out); err != nil {
		return apiTunnel{}, err
	}
	return out, nil
}

func (c *cloudflareAPIClient) putTunnelIngressConfig(ctx context.Context, accountID, tunnelID, host, service string) error {
	path := fmt.Sprintf(
		"/accounts/%s/cfd_tunnel/%s/configurations",
		url.PathEscape(strings.TrimSpace(accountID)),
		url.PathEscape(strings.TrimSpace(tunnelID)),
	)
	body := map[string]any{
		"config": map[string]any{
			"ingress": []map[string]any{
				{
					"hostname": strings.TrimSpace(host),
					"service":  strings.TrimSpace(service),
				},
				{
					"service": "http_status:404",
				},
			},
		},
	}
	return c.request(ctx, http.MethodPut, path, body, nil)
}

func (c *cloudflareAPIClient) getTunnelToken(ctx context.Context, accountID, tunnelID string) (string, error) {
	var out string
	path := fmt.Sprintf(
		"/accounts/%s/cfd_tunnel/%s/token",
		url.PathEscape(strings.TrimSpace(accountID)),
		url.PathEscape(strings.TrimSpace(tunnelID)),
	)
	if err := c.request(ctx, http.MethodGet, path, nil, &out); err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", errors.New("empty tunnel token in api response")
	}
	return out, nil
}

func (c *cloudflareAPIClient) getTunnel(ctx context.Context, accountID, tunnelID string) (apiTunnel, error) {
	var out apiTunnel
	path := fmt.Sprintf(
		"/accounts/%s/cfd_tunnel/%s",
		url.PathEscape(strings.TrimSpace(accountID)),
		url.PathEscape(strings.TrimSpace(tunnelID)),
	)
	if err := c.request(ctx, http.MethodGet, path, nil, &out); err != nil {
		return apiTunnel{}, err
	}
	return out, nil
}

func (c *cloudflareAPIClient) deleteTunnel(ctx context.Context, accountID, tunnelID string) error {
	path := fmt.Sprintf(
		"/accounts/%s/cfd_tunnel/%s?force=true",
		url.PathEscape(strings.TrimSpace(accountID)),
		url.PathEscape(strings.TrimSpace(tunnelID)),
	)
	return c.request(ctx, http.MethodDelete, path, nil, nil)
}

func (c *cloudflareAPIClient) listDNSRecordsByName(ctx context.Context, zoneID, name string) ([]apiDNSRecord, error) {
	var out []apiDNSRecord
	q := url.Values{}
	q.Set("name", strings.TrimSpace(name))
	q.Set("per_page", "100")
	path := fmt.Sprintf("/zones/%s/dns_records?%s", url.PathEscape(strings.TrimSpace(zoneID)), q.Encode())
	if err := c.request(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *cloudflareAPIClient) createDNSRecord(ctx context.Context, zoneID, name, content string, proxied bool) error {
	path := fmt.Sprintf("/zones/%s/dns_records", url.PathEscape(strings.TrimSpace(zoneID)))
	body := map[string]any{
		"type":    "CNAME",
		"name":    strings.TrimSpace(name),
		"content": strings.TrimSpace(content),
		"proxied": proxied,
	}
	return c.request(ctx, http.MethodPost, path, body, nil)
}

func (c *cloudflareAPIClient) updateDNSRecord(ctx context.Context, zoneID, recordID, name, content string, proxied bool) error {
	path := fmt.Sprintf(
		"/zones/%s/dns_records/%s",
		url.PathEscape(strings.TrimSpace(zoneID)),
		url.PathEscape(strings.TrimSpace(recordID)),
	)
	body := map[string]any{
		"type":    "CNAME",
		"name":    strings.TrimSpace(name),
		"content": strings.TrimSpace(content),
		"proxied": proxied,
	}
	return c.request(ctx, http.MethodPut, path, body, nil)
}

func (c *cloudflareAPIClient) request(ctx context.Context, method, path string, body any, out any) error {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode cloudflare api request: %w", err)
		}
	}
	endpoint := c.baseURL + path
	diag.LogCommand("cloudflare-api", method, endpoint)

	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var env cloudflareAPIEnvelope
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &env)
	}
	messages := make([]string, 0, len(env.Errors))
	for _, e := range env.Errors {
		msg := strings.TrimSpace(e.Message)
		if msg == "" {
			continue
		}
		messages = append(messages, msg)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !env.Success {
		if len(messages) == 0 {
			trimmed := strings.TrimSpace(string(raw))
			if trimmed != "" {
				messages = append(messages, trimmed)
			}
		}
		return &cloudflareAPIError{
			StatusCode: resp.StatusCode,
			Messages:   messages,
		}
	}
	if out == nil {
		return nil
	}
	if len(env.Result) == 0 || string(env.Result) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Result, out); err != nil {
		return fmt.Errorf("decode cloudflare api response: %w", err)
	}
	return nil
}
