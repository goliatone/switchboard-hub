package tailscale

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// This narrowly implements the documented LocalAPI wire protocol. It requires
// an explicit Unix socket or opt-in authenticated GUI transport; neither uses
// persistent CLI exposure as a fallback.
func (p *Provider) request(ctx context.Context, method, path string, body any, etag string, out any) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://local-tailscaled.sock/localapi/v0"+path, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	res, err := p.owned.client.Do(req)
	if err != nil {
		return "", errors.New("tailscale LocalAPI request failed")
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		return "", errors.New("tailscale LocalAPI rejected operation; configuration may have changed")
	}
	if out != nil {
		if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out); err != nil {
			return "", errors.New("invalid Tailscale LocalAPI response")
		}
	}
	return res.Header.Get("ETag"), nil
}
func (p *Provider) getConfig(ctx context.Context) (document, string, error) {
	var config document
	tag, err := p.request(ctx, "GET", "/serve-config", nil, "", &config)
	if err != nil {
		return nil, "", err
	}
	if tag == "" {
		return nil, "", errors.New("tailscale conditional configuration updates unsupported")
	}
	if config == nil {
		config = document{}
	}
	return config, tag, nil
}
func (p *Provider) watch(ctx context.Context) (string, context.CancelFunc, error) {
	// The connection, not an orphanable child PID, owns exposure lifetime. Detach
	// only after setup; owner process loss closes its socket and ends foreground.
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	setupTimer := time.AfterFunc(10*time.Second, cancel)
	defer setupTimer.Stop()
	cancelSetup := context.AfterFunc(ctx, cancel)
	defer cancelSetup()
	req, err := http.NewRequestWithContext(lifetime, "GET", "http://local-tailscaled.sock/localapi/v0/watch-ipn-bus?mask=18", nil)
	if err != nil {
		cancel()
		return "", nil, err
	}
	res, err := p.owned.client.Do(req) //nolint:bodyclose // Successful watch transfers response-body ownership to the draining goroutine; every failure closes it.
	if err != nil {
		cancel()
		return "", nil, errors.New("foreground watch unavailable")
	}
	if res.StatusCode != 200 {
		_ = res.Body.Close()
		cancel()
		return "", nil, errors.New("foreground watch unsupported")
	}
	decoder := json.NewDecoder(res.Body)
	var first struct{ SessionID string }
	if err := decoder.Decode(&first); err != nil || first.SessionID == "" || ctx.Err() != nil {
		_ = res.Body.Close()
		cancel()
		return "", nil, errors.New("foreground watch identity missing")
	}
	if !cancelSetup() || !setupTimer.Stop() || lifetime.Err() != nil {
		_ = res.Body.Close()
		cancel()
		return "", nil, context.Canceled
	}
	leaseKey := p.leaseScope() + "\x00" + first.SessionID
	foregroundLeases.Store(leaseKey, cancel)
	go func() {
		defer cancel()
		defer func() { _ = res.Body.Close() }()
		defer foregroundLeases.Delete(leaseKey)
		for {
			var notice json.RawMessage
			if decoder.Decode(&notice) != nil {
				return
			}
		}
	}()
	return first.SessionID, cancel, nil
}
