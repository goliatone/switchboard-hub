package observer

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestObserverAcrossFixtureFailures(t *testing.T) {
	const markerA = "fixture-a"
	const markerB = "fixture-b"
	var phase atomic.Int32
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/b":
			_, _ = io.WriteString(w, markerB)
			return
		case "/redirected-a":
			_, _ = io.WriteString(w, markerA)
			return
		}
		switch phase.Load() {
		case 0:
			_, _ = io.WriteString(w, markerA)
		case 1:
			http.Error(w, "fixture unavailable", http.StatusNotFound)
		case 2:
			http.Error(w, "upstream failure", http.StatusBadGateway)
		case 3:
			http.Redirect(w, r, "/redirected-a", http.StatusFound)
		case 4:
			_, _ = io.WriteString(w, markerA+" with trailing bytes")
		case 5:
			w.Header().Set("Content-Length", "20")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, "short")
		case 6:
			w.WriteHeader(http.StatusGone)
			_, _ = io.WriteString(w, "unexpected "+markerA+" response")
		case 7:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, strings.Repeat("x", 5000))
		}
	}))
	defer fixture.Close()
	transport := fixture.Client().Transport
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		phase int32
		want  Outcome
	}{
		{"healthy A", 0, Present},
		{"confirmed HTTP absence", 1, Absent},
		{"upstream failure", 2, Inconclusive},
		{"redirect to expected body", 3, Inconclusive},
		{"different successful response", 4, Inconclusive},
		{"interrupted absence response", 5, Inconclusive},
		{"absence response still contains fixture marker", 6, Inconclusive},
		{"oversized absence response", 7, Inconclusive},
		{"A recovers", 0, Present},
	} {
		t.Run(tc.name, func(t *testing.T) {
			phase.Store(tc.phase)
			if got := check(ctx, transport, fixture.URL+"/a", markerA); got != tc.want {
				t.Errorf("A outcome = %d, want %d", got, tc.want)
			}
			if got := check(ctx, transport, fixture.URL+"/b", markerB); got != Present {
				t.Errorf("unrelated B outcome = %d, want present", got)
			}
		})
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got := check(canceled, transport, fixture.URL+"/a", markerA); got != Inconclusive {
		t.Errorf("cancellation outcome = %d, want inconclusive", got)
	}
	if got := Check(ctx, fixture.URL+"/a", markerA); got != Inconclusive {
		t.Errorf("untrusted TLS outcome = %d, want inconclusive", got)
	}
	fixture.Close()
	if got := check(ctx, transport, fixture.URL+"/a", markerA); got != Inconclusive {
		t.Errorf("unreachable fixture outcome = %d, want inconclusive", got)
	}
}

func TestObserverRejectsInvalidInputBeforeRequest(t *testing.T) {
	for _, url := range []string{
		"not-a-url",
		"http://example.test/",
		"https://user@example.test/",
		"https://example.test/?token=value",
		"https://example.test/#fragment",
	} {
		if got := check(context.Background(), rejectRequest{t}, url, "fixture-a"); got != Inconclusive {
			t.Errorf("invalid input outcome = %d, want inconclusive", got)
		}
	}
}

type rejectRequest struct{ t *testing.T }

func (r rejectRequest) RoundTrip(*http.Request) (*http.Response, error) {
	r.t.Fatal("unexpected HTTP request")
	return nil, nil
}
