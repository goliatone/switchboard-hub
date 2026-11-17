package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/internal/safeio"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

// Fake provider persists state across subprocesses; no executable, socket,
// network provider or real process signaling is used by its operations.
type recoveryFake struct {
	dir, crash string
	cancel     context.CancelFunc
}
type fakeRemote struct {
	Resource ingress.Resource
	Step     string
	Calls    map[string]int
	Deleted  bool
}

func (p *recoveryFake) Name() string                                      { return "fake" }
func (p *recoveryFake) Capabilities() tunnel.Capabilities                 { return tunnel.Capabilities{} }
func (p *recoveryFake) Init(context.Context, tunnel.ProviderConfig) error { return nil }
func (p *recoveryFake) EnsureEndpoint(context.Context, tunnel.EndpointRequest) (tunnel.Endpoint, error) {
	panic("legacy provider used")
}
func (p *recoveryFake) Start(context.Context, tunnel.StartRequest) (tunnel.Session, error) {
	panic("legacy start used")
}
func (p *recoveryFake) Stop(context.Context, string) error           { panic("legacy stop used") }
func (p *recoveryFake) RemoveEndpoint(context.Context, string) error { panic("legacy remove used") }
func (p *recoveryFake) Status(context.Context, string) (tunnel.EndpointStatus, error) {
	panic("legacy status used")
}
func (p *recoveryFake) ValidateIngress(context.Context, ingress.Resource, ingress.Runtime) error {
	return nil
}
func (p *recoveryFake) read(name string) fakeRemote {
	raw, _ := os.ReadFile(filepath.Join(p.dir, "remote-"+name+".json"))
	var state fakeRemote
	_ = json.Unmarshal(raw, &state)
	if state.Calls == nil {
		state.Calls = map[string]int{}
	}
	return state
}
func (p *recoveryFake) mutate(r ingress.Resource, step string) error {
	state := p.read(r.Name)
	state.Resource = r
	state.Step = step
	state.Deleted = step == "release"
	state.Calls[step]++
	raw, _ := json.Marshal(state)
	if err := safeio.AtomicWriteFile(filepath.Join(p.dir, "remote-"+r.Name+".json"), raw, 0o600); err != nil {
		return err
	}
	if p.crash == "remote-"+step {
		os.Exit(71)
	}
	if p.crash == "timeout-"+step {
		return context.DeadlineExceeded
	}
	return nil
}
func (p *recoveryFake) EnsureOwnedIngress(ctx context.Context, r ingress.Resource) (ingress.Resource, error) {
	if p.crash == "wait" {
		if p.cancel != nil {
			p.cancel()
		}
		<-ctx.Done()
		return r, ctx.Err()
	}
	if r.Binding.EndpointID == "" {
		r.Binding.EndpointID = "endpoint-" + r.Generation
	}
	r.ProviderState = map[string]string{"creation": r.Generation}
	return r, p.mutate(r, "ensure")
}
func (p *recoveryFake) StartOwnedIngress(_ context.Context, r ingress.Resource) (ingress.Resource, error) {
	r.Session = ingress.SessionIdentity{ID: "session-" + r.OperationID, PID: 123, Executable: "fake-only", StartedAt: time.Unix(1, 0), Identity: map[string]string{"owner": r.Owner.ScopeID, "generation": r.Generation}}
	return r, p.mutate(r, "start")
}
func (p *recoveryFake) VerifyOwnedIngressSession(_ context.Context, r ingress.Resource) error {
	actual := p.read(r.Name).Resource
	if actual.Session.ID == "" {
		return nil
	}
	if actual.Owner != r.Owner || actual.Generation != r.Generation || actual.Session.ID != r.Session.ID || actual.Session.PID != r.Session.PID || actual.Session.Executable != r.Session.Executable || !actual.Session.StartedAt.Equal(r.Session.StartedAt) {
		return errors.New("process identity changed")
	}
	return nil
}
func (p *recoveryFake) StopOwnedIngress(ctx context.Context, r ingress.Resource) error {
	// Recheck at termination, not only in a service preflight.
	if err := p.VerifyOwnedIngressSession(ctx, r); err != nil {
		return err
	}
	r.Session = ingress.SessionIdentity{}
	return p.mutate(r, "stop")
}
func (p *recoveryFake) ReleaseOwnedIngress(_ context.Context, r ingress.Resource) error {
	return p.mutate(r, "release")
}
func (p *recoveryFake) StatusOwnedIngress(_ context.Context, r ingress.Resource) (ingress.Status, error) {
	actual := p.read(r.Name)
	return ingress.Status{Ready: actual.Resource.Session.ID != "", SessionID: actual.Resource.Session.ID}, nil
}
func (p *recoveryFake) ReconcileOwnedIngress(_ context.Context, progress ingress.Progress) (ingress.RecoveryResult, error) {
	if p.crash == "unknown" {
		return ingress.RecoveryResult{Outcome: "unknown"}, nil
	}
	actual := p.read(progress.Resource.Name)
	if actual.Resource.OperationID == progress.Resource.OperationID && actual.Step == progress.Step {
		return ingress.RecoveryResult{Outcome: "applied", Resource: actual.Resource}, nil
	}
	// In this fake, the atomic remote document is an authoritative complete log
	// of the latest attempt; absence of the ID proves this step did not execute.
	return ingress.RecoveryResult{Outcome: "not-applied"}, nil
}

type recoveryRegistry struct{ p *recoveryFake }

func (r recoveryRegistry) Providers() []string                     { return []string{"fake"} }
func (r recoveryRegistry) Resolve(string) (tunnel.Provider, error) { return r.p, nil }

type faultStore struct {
	*config.IngressStore
	crash string
}
type faultRecorder struct {
	ingress.Recorder
	crash string
}

func (r faultRecorder) Record(ctx context.Context, p ingress.Progress) error {
	if r.crash == "fail-"+p.Phase {
		return errors.New("injected durable write failure")
	}
	if err := r.Recorder.Record(ctx, p); err != nil {
		return err
	}
	if r.crash == p.Step+"-"+p.Phase {
		os.Exit(71)
	}
	return nil
}
func (s faultStore) WithIngressMutation(ctx context.Context, scope ingress.MutationScope, fn func(context.Context, ingress.Recorder) error) error {
	return s.IngressStore.WithIngressMutation(ctx, scope, func(ctx context.Context, r ingress.Recorder) error {
		err := fn(ctx, faultRecorder{r, s.crash})
		if err == nil && s.crash == "config-committed" {
			os.Exit(71)
		}
		return err
	})
}
func recoveryService(dir, crash string) (*Service, *recoveryFake) {
	p := &recoveryFake{dir: dir, crash: crash}
	state := filepath.Join(dir, "state")
	return NewService(ServiceOptions{ConfigPath: filepath.Join(dir, "config.yaml"), Ingress: &IngressOptions{Mode: IngressModeTunnelOnly, StateDir: state, RuntimeDir: filepath.Join(dir, "runtime")}, Store: faultStore{&config.IngressStore{}, crash}, ProviderRegistry: recoveryRegistry{p}}), p
}
func recoverySpec(name string) IngressSpec {
	return IngressSpec{Name: name, Owner: IngressOwnership{System: "test", ScopeID: name}, Provider: "fake", PublicHost: name + ".example.test", LocalPort: 8080, CallbackPath: "/hook"}
}
func invokeRecovery(s *Service, ctx context.Context, action, name string, revision *int64) (Ingress, error) {
	spec := recoverySpec(name)
	spec.ExpectedRevision = revision
	ref := IngressRef{Name: name, Owner: spec.Owner, ExpectedRevision: revision}
	switch action {
	case "ensure":
		return s.EnsureIngress(ctx, spec)
	case "start":
		return s.StartIngress(ctx, ref)
	case "stop":
		return s.StopIngress(ctx, ref)
	case "release":
		return Ingress{}, s.ReleaseIngress(ctx, ref)
	default:
		return Ingress{}, errors.New("unknown test action")
	}
}

func TestT05ProcessHelper(t *testing.T) {
	dir := os.Getenv("T05_TEST_DIRECTORY")
	if dir == "" {
		return
	}
	s, _ := recoveryService(dir, os.Getenv("T05_CRASH"))
	var revision *int64
	if raw := os.Getenv("T05_REVISION"); raw != "" {
		value, _ := strconv.ParseInt(raw, 10, 64)
		revision = &value
	}
	_, err := invokeRecovery(s, context.Background(), os.Getenv("T05_ACTION"), os.Getenv("T05_NAME"), revision)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(72)
	}
	os.Exit(0)
}
func recoveryCommand(t *testing.T, dir, action, name, crash, revision string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestT05ProcessHelper$")
	cmd.Env = append(os.Environ(), "T05_TEST_DIRECTORY="+dir, "T05_ACTION="+action, "T05_NAME="+name, "T05_CRASH="+crash, "T05_REVISION="+revision, "GORACE=atexit_sleep_ms=0")
	return cmd
}
func runRecoveryProcess(t *testing.T, dir, action, name, crash, revision string, want int) {
	t.Helper()
	out, err := recoveryCommand(t, dir, action, name, crash, revision).CombinedOutput()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if code != want {
		t.Fatalf("%s/%s exit=%d want=%d: %s", action, crash, code, want, out)
	}
}

func TestT05CrashRecoveryMatrix(t *testing.T) {
	for _, action := range []string{"ensure", "start", "stop", "release"} {
		points := []string{action + "-pending", "remote-" + action, action + "-confirmed", action + "-persisting", "config-committed"}
		if action == "release" {
			points = append(points, "stop-pending", "remote-stop", "stop-confirmed")
		}
		for _, point := range points {
			t.Run(action+"/"+point, func(t *testing.T) {
				dir := t.TempDir()
				runRecoveryProcess(t, dir, "ensure", "b", "", "", 0)
				bBefore, _ := os.ReadFile(filepath.Join(dir, "remote-b.json"))
				if action != "ensure" {
					runRecoveryProcess(t, dir, "ensure", "a", "", "0", 0)
				}
				if action == "stop" || action == "release" {
					runRecoveryProcess(t, dir, "start", "a", "", "", 0)
				}
				runRecoveryProcess(t, dir, action, "a", point, "", 71)
				// A different scope can still mutate while A has interrupted progress.
				runRecoveryProcess(t, dir, "ensure", "c", "", "", 0)
				runRecoveryProcess(t, dir, action, "a", "", "", 0)
				s, p := recoveryService(dir, "")
				state := p.read("a")
				if state.Calls["ensure"] != 1 {
					t.Fatalf("duplicate/missing ensure: %+v", state.Calls)
				}
				if action == "start" && state.Resource.Session.ID != "" {
					t.Fatal("recovery restored exposure")
				}
				if action == "release" && !state.Deleted {
					t.Fatal("release did not finish")
				}
				if action == "release" {
					runRecoveryProcess(t, dir, action, "a", "", "", 0)
				}
				bAfter, _ := os.ReadFile(filepath.Join(dir, "remote-b.json"))
				if string(bBefore) != string(bAfter) {
					t.Fatal("other provider scope changed")
				}
				_, cfg, err := s.LoadConfig()
				if err != nil {
					t.Fatal(err)
				}
				if findAppByName(cfg, "b") < 0 || findAppByName(cfg, "c") < 0 {
					t.Fatal("unrelated configuration lost")
				}
				if action == "ensure" && findAppByName(cfg, "a") < 0 {
					t.Fatal("ensure did not persist")
				}
				if action == "start" {
					if started, err := invokeRecovery(s, context.Background(), "start", "a", nil); err != nil || started.State != IngressStateRunning {
						t.Fatal("fresh explicit start:", err)
					}
					if p.read("a").Resource.Session.ID == "" {
						t.Fatal("fresh explicit start not honored")
					}
				}
			})
		}
	}
}

func TestT05ConcurrentProcesses(t *testing.T) {
	for _, same := range []bool{false, true} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			dir := t.TempDir()
			if same {
				runRecoveryProcess(t, dir, "ensure", "a", "", "", 0)
			}
			action, name, revision := "ensure", "b", ""
			if same {
				action, name, revision = "start", "a", "1"
			}
			first := recoveryCommand(t, dir, action, "a", "", revision)
			second := recoveryCommand(t, dir, action, name, "", revision)
			if err := first.Start(); err != nil {
				t.Fatal(err)
			}
			if err := second.Start(); err != nil {
				t.Fatal(err)
			}
			e1, e2 := first.Wait(), second.Wait()
			if same {
				if (e1 == nil) == (e2 == nil) {
					t.Fatalf("want one winner: %v %v", e1, e2)
				}
				_, p := recoveryService(dir, "")
				if p.read("a").Calls["start"] != 1 {
					t.Fatal("stale caller mutated provider")
				}
			} else {
				if e1 != nil || e2 != nil {
					t.Fatalf("%v %v", e1, e2)
				}
				s, _ := recoveryService(dir, "")
				_, cfg, err := s.LoadConfig()
				if err != nil || len(cfg.Apps) != 2 {
					t.Fatalf("lost update: %+v %v", cfg, err)
				}
			}
		})
	}
}

func TestT05CancellationUnknownAndPIDReuse(t *testing.T) {
	t.Run("timeout retains intent", func(t *testing.T) {
		dir := t.TempDir()
		s, provider := recoveryService(dir, "wait")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		provider.cancel = cancel // after durable intent, at the provider boundary
		if _, err := invokeRecovery(s, ctx, "ensure", "a", nil); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		runRecoveryProcess(t, dir, "ensure", "a", "unknown", "", 72)
		runRecoveryProcess(t, dir, "ensure", "a", "", "", 0)
	})
	t.Run("reused PID untouched", func(t *testing.T) {
		dir := t.TempDir()
		runRecoveryProcess(t, dir, "ensure", "a", "", "", 0)
		runRecoveryProcess(t, dir, "start", "a", "", "", 0)
		runRecoveryProcess(t, dir, "stop", "a", "stop-pending", "", 71)
		_, p := recoveryService(dir, "")
		remote := p.read("a")
		remote.Resource.Session.StartedAt = time.Unix(99, 0)
		raw, _ := json.Marshal(remote)
		if err := os.WriteFile(filepath.Join(dir, "remote-a.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		runRecoveryProcess(t, dir, "stop", "a", "", "", 72)
		after := p.read("a")
		if after.Calls["stop"] != 0 || after.Resource.Session.ID == "" {
			t.Fatal("mismatched process affected")
		}
	})
	t.Run("failed intent write prevents provider", func(t *testing.T) {
		dir := t.TempDir()
		s, p := recoveryService(dir, "fail-pending")
		if _, err := invokeRecovery(s, context.Background(), "ensure", "a", nil); err == nil {
			t.Fatal("expected failure")
		}
		if p.read("a").Calls["ensure"] != 0 {
			t.Fatal("effect without durable intent")
		}
		runRecoveryProcess(t, dir, "ensure", "a", "", "", 0)
	})
}

func TestT05LegacyAndChangedIntentRefused(t *testing.T) {
	dir := t.TempDir()
	runRecoveryProcess(t, dir, "ensure", "a", "remote-ensure", "", 71)
	if _, err := config.LoadOrDefault(filepath.Join(dir, "config.yaml")); !errors.Is(err, config.ErrIsolatedStore) {
		t.Fatal(err)
	}
	if err := config.Save(filepath.Join(dir, "config.yaml"), config.Default("test", "127.0.0.1")); !errors.Is(err, config.ErrIsolatedStore) {
		t.Fatal(err)
	}
	s, _ := recoveryService(dir, "")
	spec := recoverySpec("a")
	spec.LocalPort++
	if _, err := s.EnsureIngress(context.Background(), spec); err == nil || !strings.Contains(err.Error(), "intent") {
		t.Fatal(err)
	}
	spec = recoverySpec("a")
	spec.Owner.ScopeID = "intruder"
	if _, err := s.EnsureIngress(context.Background(), spec); err == nil {
		t.Fatal("foreign owner accepted")
	}
	runRecoveryProcess(t, dir, "ensure", "a", "", "", 0)
}

func TestT05RecoveryPreservesOperatorEditsAndRejectsChangedTarget(t *testing.T) {
	for _, target := range []bool{false, true} {
		t.Run(fmt.Sprint(target), func(t *testing.T) {
			dir := t.TempDir()
			runRecoveryProcess(t, dir, "ensure", "a", "", "", 0)
			runRecoveryProcess(t, dir, "start", "a", "start-persisting", "", 71)
			s, p := recoveryService(dir, "")
			_, cfg, err := s.LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			if target {
				cfg.Apps[findAppByName(cfg, "a")].LocalPort++
			} else {
				cfg.TLD = "operator"
			}
			raw, err := json.Marshal(cfg) // JSON is also valid YAML.
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			want := 0
			if target {
				want = 72
			}
			runRecoveryProcess(t, dir, "start", "a", "", "", want)
			_, after, err := s.LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			if !target && after.TLD != "operator" {
				t.Fatal("unrelated operator edit lost")
			}
			if target && p.read("a").Calls["stop"] != 0 {
				t.Fatal("conflicted recovery mutated provider")
			}
		})
	}
}

func TestT05RecoveryInterruptedAgain(t *testing.T) {
	dir := t.TempDir()
	runRecoveryProcess(t, dir, "ensure", "a", "", "", 0)
	runRecoveryProcess(t, dir, "start", "a", "remote-start", "", 71)
	runRecoveryProcess(t, dir, "start", "a", "remote-stop", "", 71)
	runRecoveryProcess(t, dir, "start", "a", "", "", 0)
	_, p := recoveryService(dir, "")
	remote := p.read("a")
	if remote.Calls["start"] != 1 || remote.Calls["stop"] != 1 || remote.Resource.Session.ID != "" {
		t.Fatalf("unsafe repeated recovery: %+v", remote)
	}
}
