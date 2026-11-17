package observer

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Outcome is also the exit code used by an external probe.
type Outcome int

const (
	Present      Outcome = 0
	Inconclusive Outcome = 1
	Absent       Outcome = 3
)

// Check makes a direct, authenticated HTTPS request to a dedicated fixture.
func Check(ctx context.Context, rawURL, expected string) Outcome {
	transport := &http.Transport{DisableKeepAlives: true}
	return check(ctx, transport, rawURL, expected)
}

func check(ctx context.Context, transport http.RoundTripper, rawURL, expected string) Outcome {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil || !validTarget(req.URL) || expected == "" || len(expected) > 1024 {
		return Inconclusive
	}
	req.Header.Set("Cache-Control", "no-cache, no-store")
	client := http.Client{
		Transport: transport,
		Timeout:   8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	res, err := client.Do(req)
	if err != nil {
		return Inconclusive
	}
	defer func() { _ = res.Body.Close() }()
	const maxBody = 4096
	body, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return Inconclusive
	}

	switch {
	case res.StatusCode == http.StatusOK && string(body) == expected:
		return Present
	case (res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone) && !strings.Contains(string(body), expected):
		return Absent
	default:
		return Inconclusive
	}
}

func validTarget(target *url.URL) bool {
	return target.Scheme == "https" && target.Hostname() != "" && target.User == nil &&
		target.Fragment == "" && !target.ForceQuery && target.RawQuery == ""
}
