//go:build provider_live

package validation

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/internal/safeio"
	sb "github.com/goliatone/switchboard-hub/pkg/switchboard"
	"github.com/goliatone/switchboard-hub/tools/validation/internal/observer"
)

type credentialEnvironment string

func (e credentialEnvironment) ResolveCredential(_ context.Context, ref string) ([]byte, error) {
	if ref != "validation-cloudflare" {
		return nil, errors.New("unexpected credential reference")
	}
	raw := os.Getenv(string(e))
	if raw == "" {
		return nil, errors.New("configured private credential environment is absent")
	}
	return []byte(raw), nil
}

type liveScope struct {
	client    *sb.Client
	spec      sb.IngressSpec
	current   sb.Ingress
	expected  string
	attempted bool
}

func (s *liveScope) ref() sb.IngressRef {
	return sb.IngressRef{Name: s.spec.Name, Owner: s.spec.Owner, ExpectedRevision: &s.current.Revision}
}

type liveReport struct {
	SupervisorSHA256 string            `json:"supervisor_sha256,omitempty"`
	SourceHashes     map[string]string `json:"working_tree_sha256"`
	ImmutablePin     string            `json:"immutable_pin,omitempty"`
	Provider         string            `json:"provider"`
	RunID            string            `json:"run_id"`
	Started          time.Time         `json:"started"`
	Go               string            `json:"go"`
	Platform         string            `json:"platform"`
	ProbeNetwork     string            `json:"probe_network"`
	HelperSHA256     string            `json:"helper_sha256"`
	ProbeSHA256      string            `json:"probe_sha256"`
	Results          []string          `json:"results"`
	Cleanup          []string          `json:"cleanup"`
	Passed           bool              `json:"lifecycle_subset_passed"`
	T08Complete      bool              `json:"t08_complete"`
	Remaining        []string          `json:"remaining_gates"`
}

func TestLiveOwnedIngress(t *testing.T) {
	c := loadLiveConfig(t)
	if err := c.checkCredentialPresence(); err != nil {
		t.Fatal(err)
	}
	// Recheck freshness atomically before preparing the run.
	// Failed runs remain intact.
	if err := os.Mkdir(c.RunDir, 0o700); err != nil {
		t.Fatal("run_dir must be a fresh directory with an existing private parent")
	}
	report := liveReport{Provider: c.Provider, RunID: rand.Text(), Started: time.Now().UTC(), Go: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH, ProbeNetwork: c.ProbeNetwork, HelperSHA256: fileHash(t, c.HelperPath), ProbeSHA256: fileHash(t, c.ProbePath), Remaining: []string{"real owner-process failure/recovery", "real helper argv/environment and token cleanup observations", "provider drift and permission-failure fixtures", "private/public collision and visibility observations", "isolated daemon and host restart observations", "immutable source/module pin and resolved dependency checksums", "exact-source ctx compatibility and final evidence reconciliation"}}
	report.SourceHashes = sourceHashes(t)
	if c.Provider == "cloudflare" {
		report.SupervisorSHA256 = fileHash(t, c.Values["supervisor_path"])
	}
	t.Cleanup(func() {
		report.Passed = !t.Failed()
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			t.Error(err)
			return
		}
		if err = safeio.AtomicWriteFile(filepath.Join(c.RunDir, "evidence.json"), raw, 0o600); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	a := newLiveScope(t, c, "a", c.HostA, c.PortA, report.RunID)
	b := newLiveScope(t, c, "b", c.HostB, c.PortB, report.RunID)
	defer cleanupLive(t, &report, a, b)
	ensureStart(t, ctx, b)
	waitProbe(t, ctx, c, b, observer.Present)
	watchCtx, stopWatch := context.WithCancel(ctx)
	var guard sync.WaitGroup
	guard.Add(1)
	failures := make(chan error, 1)
	go func() { defer guard.Done(); watchB(watchCtx, c, b, failures) }()
	defer func() {
		stopWatch()
		guard.Wait()
		select {
		case err := <-failures:
			t.Error(err)
		default:
		}
	}()
	for iteration := 1; iteration <= 3; iteration++ {
		report.Results = append(report.Results, fmt.Sprintf("cycle-%d: started", iteration))
		ensureStart(t, ctx, a)
		waitProbe(t, ctx, c, a, observer.Present)
		wrong := a.ref()
		wrong.Owner.ScopeID = "unowned"
		if err := a.client.ReleaseIngress(ctx, wrong); err == nil {
			t.Fatal("wrong owner release was accepted")
		}
		stale := a.ref()
		revision := a.current.Revision - 1
		stale.ExpectedRevision = &revision
		if _, err := a.client.StopIngress(ctx, stale); err == nil {
			t.Fatal("stale revision stop was accepted")
		}
		canceled, stop := context.WithCancel(ctx)
		stop()
		if _, err := a.client.StopIngress(canceled, a.ref()); err == nil {
			t.Fatal("cancelled operation succeeded")
		}
		if err := verifyRunning(ctx, a); err != nil {
			t.Fatal("A changed during refused operations")
		}
		waitProbe(t, ctx, c, a, observer.Present)
		updated, err := a.client.StopIngress(ctx, a.ref())
		if err != nil {
			t.Fatal(err)
		}
		a.current = updated
		waitProbe(t, ctx, c, a, observer.Absent)
		updated, err = a.client.StartIngress(ctx, a.ref())
		if err != nil {
			t.Fatal(err)
		}
		a.current = updated
		waitProbe(t, ctx, c, a, observer.Present)
		if err = a.client.ReleaseIngress(ctx, a.ref()); err != nil {
			t.Fatal(err)
		}
		a.current = sb.Ingress{}
		a.attempted = false
		waitProbe(t, ctx, c, a, observer.Absent)
		if err := checkB(ctx, c, b); err != nil {
			t.Fatal(err)
		}
		report.Results = append(report.Results, fmt.Sprintf("cycle-%d: ensure/start/external-response/owner-refusal/stale-refusal/cancellation/stop/restart/release", iteration))
	}
}

// TestLiveOwnedIngressPreflight checks a prepared configuration without
// executing helpers, connecting to either provider or reading credentials.
func TestLiveOwnedIngressPreflight(t *testing.T) {
	loadLiveConfig(t)
}

func loadLiveConfig(t *testing.T) Config {
	t.Helper()
	path := os.Getenv("SWITCHBOARD_INGRESS_VALIDATION")
	if path == "" {
		t.Fatal("blocked: set SWITCHBOARD_INGRESS_VALIDATION to a private dedicated-test configuration; no provider was contacted")
	}
	config, err := readConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.checkLocalSetup(); err != nil {
		t.Fatal(err)
	}
	return config
}

func newLiveScope(t *testing.T, c Config, label, host string, port int, runID string) *liveScope {
	t.Helper()
	expected := "switchboard-validation-" + runID + "-" + label
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, expected)
	}))
	t.Cleanup(server.Close)
	_, localPort, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(localPort)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(c.RunDir, label)
	if err = os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	setup := sb.New(sb.Options{ConfigPath: path})
	cfg, err := setup.LoadOrCreateDefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	provider := sb.TunnelProviderConfig{Enabled: true, Values: c.Values}
	if c.Provider == "cloudflare" {
		provider.CredentialRef = "validation-cloudflare"
	}
	cfg.Tunnel.Providers = map[string]sb.TunnelProviderConfig{c.Provider: provider}
	if err = setup.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	client := sb.New(sb.Options{ConfigPath: path, Ingress: &sb.IngressOptions{Mode: sb.IngressModeTunnelOnly, StateDir: filepath.Join(dir, "state"), RuntimeDir: filepath.Join(dir, "run"), Helpers: map[string]string{c.Provider: c.HelperPath}, Credentials: credentialEnvironment(c.CredentialEnv)}})
	return &liveScope{client: client, expected: expected, spec: sb.IngressSpec{Name: "validation-" + label, Provider: c.Provider, PublicHost: host, DialHost: "127.0.0.1", LocalPort: n, CallbackPath: "/", Binding: &sb.EndpointBinding{HTTPSPort: port, Path: "/"}, Owner: sb.IngressOwnership{System: "switchboard-validation", ScopeID: runID + "-" + label}}}
}
func ensureStart(t *testing.T, ctx context.Context, s *liveScope) {
	t.Helper()
	s.attempted = true
	updated, err := s.client.EnsureIngress(ctx, s.spec)
	if err != nil {
		t.Fatal(err)
	}
	s.current = updated
	updated, err = s.client.StartIngress(ctx, s.ref())
	if err != nil {
		t.Fatal(err)
	}
	s.current = updated
}

func probe(ctx context.Context, c Config, s *liveScope) observer.Outcome {
	host := s.spec.PublicHost
	if s.spec.Binding.HTTPSPort != 443 {
		host = net.JoinHostPort(host, strconv.Itoa(s.spec.Binding.HTTPSPort))
	}
	return observer.External(ctx, c.ProbePath, "https://"+host+"/", s.expected)
}

func waitProbe(t *testing.T, ctx context.Context, c Config, s *liveScope, want observer.Outcome) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		last := probe(waitCtx, c, s)
		if last == want {
			return
		}
		select {
		case <-waitCtx.Done():
			t.Fatalf("external serving-state observation timed out (wanted exit %d, last exit %d)", want, last)
		case <-ticker.C:
		}
	}
}

func verifyRunning(ctx context.Context, s *liveScope) error {
	state, err := s.client.StatusIngress(ctx, s.ref())
	if err != nil || state.State != sb.IngressStateRunning || state.EndpointID != s.current.EndpointID ||
		state.SessionID != s.current.SessionID || state.Revision != s.current.Revision {
		return errors.New("recorded ingress identity or state changed")
	}
	return nil
}

func checkB(ctx context.Context, c Config, b *liveScope) error {
	if err := verifyRunning(ctx, b); err != nil {
		return errors.New("B identity/state changed during A lifecycle")
	}
	if probe(ctx, c, b) != observer.Present {
		return errors.New("external probe did not confirm B remained available")
	}
	return nil
}

func watchB(ctx context.Context, c Config, b *liveScope, failures chan<- error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := checkB(ctx, c, b); err != nil {
			if ctx.Err() == nil {
				failures <- err
			}
			return
		}
	}
}
func cleanupLive(t *testing.T, report *liveReport, scopes ...*liveScope) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, s := range scopes {
		if s.current.Name == "" {
			if s.attempted {
				report.Cleanup = append(report.Cleanup, s.spec.Name+": uncertain ensure; retain journals and inspect exact attempt")
			}
			continue
		}
		stopped, err := s.client.StopIngress(ctx, s.ref())
		if err == nil {
			s.current = stopped
			err = s.client.ReleaseIngress(ctx, s.ref())
		}
		if err != nil {
			report.Cleanup = append(report.Cleanup, s.spec.Name+": recovery required; retain state")
			t.Error("owned cleanup incomplete; retained run directory")
		} else {
			report.Cleanup = append(report.Cleanup, s.spec.Name+": released")
		}
	}
}
func fileHash(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sourceHashes(t *testing.T) map[string]string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, dir := range []string{"cmd", "internal", "pkg", "tools"} {
		err = filepath.WalkDir(filepath.Join(root, dir), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, e := filepath.Rel(root, path)
			if e != nil {
				return e
			}
			result[rel] = fileHash(t, path)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"go.mod", "go.sum", "taskfile", ".golangci.yml"} {
		result[name] = fileHash(t, filepath.Join(root, name))
	}
	return result
}
