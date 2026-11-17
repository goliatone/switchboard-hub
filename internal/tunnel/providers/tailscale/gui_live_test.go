//go:build provider_live

package tailscale

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

type readOnlyGUITransport struct{ next http.RoundTripper }

func (t readOnlyGUITransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet {
		return nil, fmt.Errorf("read-only GUI preflight forbids mutation")
	}
	return t.next.RoundTrip(r)
}

// This explicit opt-in probe opens no exposure and writes no daemon configuration.
// It proves transport capabilities only, not the T08 live lifecycle requirements.
func TestLiveGUIReadOnlyTransport(t *testing.T) {
	helper := os.Getenv("SWITCHBOARD_TAILSCALE_GUI_PROBE")
	if helper == "" {
		t.Skip("set SWITCHBOARD_TAILSCALE_GUI_PROBE to the explicit trusted helper; no live probe executed")
	}
	if runtime.GOOS != "darwin" {
		t.Fatal("GUI preflight requires macOS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p := New()
	if err := p.InitIngressPrivate(ctx, map[string]string{"local_api_transport": "macos-gui", "coordination_dir": filepath.Join(t.TempDir(), "coord")}, ingress.Runtime{HelperPath: helper}, nil); err != nil {
		t.Fatal(err)
	}
	original := p.owned.client.Transport.(*guiTransport)
	defer original.CloseIdleConnections()
	p.owned.client.Transport = readOnlyGUITransport{next: original}
	before, tag, err := p.getConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	id, stop, err := p.watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	for {
		if _, ok := foregroundLeases.Load(p.leaseScope() + "\x00" + id); !ok {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("probe watch did not close")
		case <-time.After(time.Millisecond):
		}
	}
	after, afterTag, err := p.getConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tag != afterTag || !jsonEqual(marshal(before), marshal(after)) {
		t.Fatal("Serve configuration changed during read-only preflight")
	}
	t.Logf("authenticated GUI transport; ETag and session identity present; configuration unchanged; normalized config SHA256 %x", sha256.Sum256(marshal(after)))
}
