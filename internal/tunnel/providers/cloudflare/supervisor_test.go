package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goliatone/switchboard-hub/internal/safeio"
	"github.com/goliatone/switchboard-hub/internal/tunnel/ownedstate"
	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

func TestConnectorSupervisorSubprocess(t *testing.T) {
	for i, arg := range os.Args {
		if arg == "--run" {
			os.Exit(ConnectorSupervisorMain(os.Args[i:]))
		}
	}
}
func prepareSupervisorFixture(t *testing.T, p *Provider) {
	t.Helper()
	dir, err := os.MkdirTemp("/private/tmp", "cf-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p.owned.runtime.RuntimeDir = dir
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(dir, "supervisor")
	script := "#!/bin/sh\nif [ \"$1\" = '--protocol' ]; then echo switchboard-connector-v1; exit 0; fi\nexec '" + executable + "' -test.run='^TestConnectorSupervisorSubprocess$' -- \"$@\"\n"
	if err = os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "cloudflared")
	script = "#!/bin/sh\nif [ \"$3\" = '--help' ]; then echo --token-file; exit 0; fi\nif [ -n \"${SWITCHD_CF_API_TOKEN-}${TUNNEL_TOKEN-}\" ]; then exit 30; fi\nexec /bin/sleep 600\n"
	if err = os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	p.owned.supervisor = wrapper
	p.owned.runtime.HelperPath = helper
}
func TestOwnedConnectorStopRestartAndHostLoss(t *testing.T) {
	_, srv := newOwnedFake(t)
	p, r := ownedFixture(t, srv, "a")
	prepareSupervisorFixture(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.Setenv("SWITCHD_CF_API_TOKEN", "must-not-reach-child")
	t.Setenv("TUNNEL_TOKEN", "must-not-reach-child")
	r, err := p.EnsureOwnedIngress(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		r.OperationID = string(rune('b' + i))
		r, err = p.StartOwnedIngress(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		spec, err := p.connectorSpec(r)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(spec.Credential.Path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatal("token not private", err)
		}
		if err = p.VerifyOwnedIngressSession(ctx, r); err != nil {
			t.Fatal(err)
		}
		// A new provider object has no process handle/cache, but can contact the
		// recorded supervisor and stop exactly its child.
		fresh := New()
		fresh.owned = p.owned
		if i == 0 {
			if err = fresh.StopOwnedIngress(ctx, r); err != nil {
				t.Fatal(err)
			}
		} else {
			lease, ok := connectorLeases.Load(r.Session.ID)
			if !ok {
				t.Fatal("missing host lifetime pipe")
			}
			_ = lease.(*os.File).Close()
			deadline := time.Now().Add(4 * time.Second)
			for time.Now().Before(deadline) {
				var done supervisorSpec
				if readPrivateJSON(spec.Done, &done) == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err = fresh.StopOwnedIngress(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = os.Stat(spec.Credential.Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("token survived stop", err)
		}
		r, _, err = ownedstate.Load(p.owned.runtime.StateDir, r)
		if err != nil {
			t.Fatal(err)
		}
		if r.Session.ID != "" || r.Binding.EndpointID == "" || r.ProviderState["dns_id"] == "" {
			t.Fatal("Stop lost restartable endpoint")
		}
	}
	if err = p.ReleaseOwnedIngress(ctx, r); err != nil {
		t.Fatal(err)
	}
}
func TestOwnedConnectorRecoveryNeverSignalsUnknownPID(t *testing.T) {
	_, srv := newOwnedFake(t)
	p, r := ownedFixture(t, srv, "a")
	prepareSupervisorFixture(t, p)
	r, err := p.EnsureOwnedIngress(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.Session = ingress.SessionIdentity{ID: "unknown", PID: os.Getpid(), Executable: "unrelated", StartedAt: time.Now(), Identity: map[string]string{"generation": r.Generation}}
	if err = p.save(r); err != nil {
		t.Fatal(err)
	}
	if err = p.StopOwnedIngress(context.Background(), r); err == nil {
		t.Fatal("accepted unknown process")
	}
}
func TestOwnedConnectorFailedStartAndArtifactReplacement(t *testing.T) {
	_, srv := newOwnedFake(t)
	p, r := ownedFixture(t, srv, "a")
	prepareSupervisorFixture(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := p.EnsureOwnedIngress(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	p.owned.supervisor = "/does/not/exist"
	if _, err = p.StartOwnedIngress(ctx, r); err == nil {
		t.Fatal("start unexpectedly passed")
	}
	saved, _, err := ownedstate.Load(p.owned.runtime.StateDir, r)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := p.connectorSpec(saved)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(spec.Credential.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed-start token retained")
	}
	outcome, err := p.ReconcileOwnedIngress(ctx, ingress.Progress{Step: "start", Resource: r})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Outcome != "applied" {
		t.Fatal(outcome.Outcome)
	}
	if err = p.StopOwnedIngress(ctx, saved); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(spec.Credential.Path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = spec.Credential.remove(); err == nil {
		t.Fatal("removed replacement")
	}
}
func TestOwnedInitializerRequiresExclusiveWriterAndSecureHelpers(t *testing.T) {
	_, srv := newOwnedFake(t)
	p, r := ownedFixture(t, srv, "a")
	prepareSupervisorFixture(t, p)
	settings := map[string]string{"ownership_mode": "single-writer", "authority_id": "authority", "account_id": "account", "zone_id": "zone", "coordination_dir": p.owned.coordination, "supervisor_path": p.owned.supervisor}
	rt := p.owned.runtime
	if err := p.ValidateIngress(context.Background(), r, rt); err != nil {
		t.Fatal(err)
	}
	if err := p.InitIngressPrivate(context.Background(), settings, rt, []byte("dummy-private-api")); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ownership_mode", "authority_id", "coordination_dir", "supervisor_path"} {
		v := settings[key]
		settings[key] = ""
		if err := p.InitIngressPrivate(context.Background(), settings, rt, []byte("dummy-private-api")); err == nil {
			t.Fatal("accepted missing", key)
		}
		settings[key] = v
	}
}
func TestSupervisorCompletionAuthenticatesWholeIdentity(t *testing.T) {
	dir := t.TempDir()
	spec := supervisorSpec{Socket: filepath.Join(dir, "missing.sock"), Done: filepath.Join(dir, "done"), Resource: ingress.Resource{Session: ingress.SessionIdentity{ID: "owned"}}}
	altered := spec
	altered.Resource.Session.ID = "other"
	if err := safeio.AtomicWriteFile(spec.Done, []byte(jsonText(altered)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := supervisorCall(context.Background(), spec, "stop"); err == nil {
		t.Fatal("accepted wrong completion")
	}
	raw, _ := json.Marshal(spec)
	if strings.Contains(string(raw), "dummy-connector-token") {
		t.Fatal("serialized token")
	}
}

func TestSupervisorHostLossDuringCapabilityProbe(t *testing.T) {
	dir, err := os.MkdirTemp("/private/tmp", "cf-probe-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	helper := filepath.Join(dir, "helper")
	if err = os.WriteFile(helper, []byte("#!/bin/sh\nexec /bin/sleep 600\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ref, err := createCredentialFile(dir, "probe", []byte("dummy-token"), func(credentialFile) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	spec := supervisorSpec{Helper: helper, Credential: ref, Socket: filepath.Join(dir, "s.sock"), Done: filepath.Join(dir, "s.done"), Resource: ingress.Resource{Session: ingress.SessionIdentity{ID: "probe"}}}
	path := filepath.Join(dir, "s.json")
	if err = safeio.AtomicWriteFile(path, []byte(jsonText(spec)), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	finished := make(chan error, 1)
	go func() { finished <- runConnectorSupervisor(path, reader) }()
	_ = writer.Close()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("host loss did not cancel startup")
	}
	if _, err = os.Stat(ref.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("startup token retained", err)
	}
}
