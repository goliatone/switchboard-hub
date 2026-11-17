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
	"time"
)

// ownedAPI borrows the service's cleared-at-return buffer. It never records
// response bodies or provider error messages, including reflected credentials.
type ownedAPI struct {
	secret []byte
	client *http.Client
	base   string
}
type ownedAPIError int

func (e ownedAPIError) Error() string {
	return fmt.Sprintf("Cloudflare request failed (HTTP %d); check permissions or retry recorded operation", int(e))
}
func (a ownedAPI) call(ctx context.Context, method, path string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return errors.New("invalid Cloudflare request")
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, bytes.NewReader(raw))
	if err != nil {
		return errors.New("invalid Cloudflare endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+string(a.secret))
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return errors.New("cloudflare request outcome uncertain")
	}
	defer func() { _ = resp.Body.Close() }()
	var env struct {
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&env)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ownedAPIError(resp.StatusCode)
	}
	if err != nil || !env.Success {
		return errors.New("cloudflare response outcome uncertain")
	}
	if out != nil {
		if len(env.Result) == 0 || string(env.Result) == "null" || json.Unmarshal(env.Result, out) != nil {
			return errors.New("cloudflare result unavailable")
		}
	}
	return nil
}
func newOwnedAPI(secret []byte) ownedAPI {
	return ownedAPI{secret: secret, base: defaultCloudflareAPIBaseURL, client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}
func absent(err error) bool {
	var status ownedAPIError
	return errors.As(err, &status) && status == 404
}
func (p *Provider) tunnelPath(id string) string {
	return "/accounts/" + url.PathEscape(p.owned.account) + "/cfd_tunnel/" + url.PathEscape(id)
}
func (p *Provider) dnsPath(id string) string {
	return "/zones/" + url.PathEscape(p.owned.zone) + "/dns_records/" + url.PathEscape(id)
}

type ownedTunnel struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	ConfigSource string  `json:"config_src"`
	CreatedAt    string  `json:"created_at"`
	DeletedAt    *string `json:"deleted_at"`
}
type ownedDNS struct {
	ID                string         `json:"id,omitempty"`
	Type              string         `json:"type"`
	Name              string         `json:"name"`
	Content           string         `json:"content"`
	Proxied           bool           `json:"proxied"`
	TTL               int            `json:"ttl"`
	Comment           string         `json:"comment"`
	Tags              []string       `json:"tags,omitempty"`
	Settings          map[string]any `json:"settings,omitempty"`
	CreatedAt         string         `json:"created_on,omitempty"`
	ModifiedAt        string         `json:"modified_on,omitempty"`
	CommentModifiedAt string         `json:"comment_modified_on,omitempty"`
	TagsModifiedAt    string         `json:"tags_modified_on,omitempty"`
}
type ownedConfiguration struct {
	Version int64           `json:"version"`
	Config  json.RawMessage `json:"config"`
}

func jsonText(v any) string { b, _ := json.Marshal(v); return string(b) }
func configFor(host, origin string) map[string]any {
	return map[string]any{"config": map[string]any{"ingress": []map[string]string{{"hostname": host, "service": origin}, {"service": "http_status:404"}}}}
}
