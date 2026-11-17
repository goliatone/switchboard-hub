package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/goliatone/switchboard-hub/internal/safeio"
	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

// supervisorSpec contains only non-secret identity and private artifact paths.
// A supervisor is a per-session child, never a machine-wide Switchboard service.
type supervisorSpec struct {
	Resource   ingress.Resource
	Helper     string
	Credential credentialFile
	Socket     string
	Done       string
}
type supervisorRequest struct{ ID, Action string }
type supervisorResponse struct {
	ID      string
	Running bool
	Error   bool
}

var connectorLeases sync.Map // session ID -> parent pipe; EOF ends the exposure

// ConnectorSupervisorMain is used only by cmd/switchboard-connector. Its private
// bootstrap file and inherited stdin are the authority to launch one child.
func ConnectorSupervisorMain(args []string) int {
	if len(args) == 1 && args[0] == "--protocol" {
		_, _ = io.WriteString(os.Stdout, "switchboard-connector-v1\n")
		return 0
	}
	if len(args) != 2 || args[0] != "--run" {
		return 2
	}
	if runConnectorSupervisor(args[1], os.Stdin) != nil {
		return 1
	}
	return 0
}
func runConnectorSupervisor(path string, lifetime io.Reader) error {
	var spec supervisorSpec
	if err := readPrivateJSON(path, &spec); err != nil {
		return err
	}
	dir, err := safeio.PrivateDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	if spec.Resource.Session.ID == "" || filepath.Dir(spec.Socket) != dir || filepath.Dir(spec.Done) != dir || filepath.Dir(spec.Credential.Path) != dir {
		return errors.New("invalid supervisor identity")
	}
	if _, err = os.Lstat(spec.Done); !errors.Is(err, os.ErrNotExist) {
		return errors.New("session already completed or ambiguous")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() { _, _ = io.Copy(io.Discard, lifetime); cancel() }()
	listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", spec.Socket)
	if err != nil {
		return errors.New("supervisor socket unavailable; preserve existing session")
	}
	defer func() { _ = listener.Close() }()
	cmd, err := secureConnectorCommand(ctx, spec.Helper, spec.Credential)
	if err != nil {
		return finishSupervisor(spec, err)
	}
	if err = cmd.Start(); err != nil {
		return finishSupervisor(spec, err)
	}
	// exec.Cmd owns the child process handle. Go serializes signal/wait state;
	// recovered callers never signal a PID and never receive this handle.
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	stop := make(chan struct{})
	var once sync.Once
	requestStop := func() { once.Do(func() { close(stop) }) }
	go serveSupervisor(listener, spec.Resource.Session.ID, requestStop)
	select {
	case <-ctx.Done():
		<-exited
	case <-stop:
		cancel()
		<-exited
	case <-exited:
	}
	return finishSupervisor(spec, nil)
}
func finishSupervisor(spec supervisorSpec, cause error) error {
	if err := spec.Credential.remove(); err != nil {
		return err
	}
	if err := safeio.AtomicWriteFile(spec.Done, []byte(jsonText(spec)), 0o600); err != nil {
		return err
	}
	return cause
}
func readPrivateJSON(path string, out any) error {
	f, err := safeio.OpenPrivate(path, false)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if json.NewDecoder(io.LimitReader(f, 1<<20)).Decode(out) != nil {
		return errors.New("invalid private connector state")
	}
	return nil
}
func supervisorCall(ctx context.Context, spec supervisorSpec, action string) (bool, error) {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", spec.Socket)
	if err != nil {
		var done supervisorSpec
		if readPrivateJSON(spec.Done, &done) == nil && jsonText(done) == jsonText(spec) {
			return false, nil
		}
		return false, errors.New("connector identity unavailable; preserve process/artifacts and inspect supervisor")
	}
	defer func() { _ = conn.Close() }()
	deadline := time.Now().Add(3 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if err = json.NewEncoder(conn).Encode(supervisorRequest{ID: spec.Resource.Session.ID, Action: action}); err != nil {
		return false, errors.New("connector control outcome uncertain")
	}
	var response supervisorResponse
	if json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&response) != nil || response.ID != spec.Resource.Session.ID || response.Error {
		return false, errors.New("connector supervisor identity mismatch")
	}
	return response.Running, nil
}
func supervisorPaths(rt ingress.Runtime, id string) (string, string, string) {
	// Keep socket paths below macOS's sockaddr_un limit. The directory is private
	// and the full session ID is authenticated on every request.
	stem := filepath.Join(rt.RuntimeDir, "cf-"+digest(id)[:20])
	return stem + ".json", stem + ".sock", stem + ".done"
}
func (p *Provider) connectorSpec(r ingress.Resource) (supervisorSpec, error) {
	var spec supervisorSpec
	if json.Unmarshal([]byte(r.ProviderState["connector"]), &spec) != nil || spec.Resource.Session.ID != r.Session.ID || !sameOwnedBinding(spec.Resource, r) || spec.Resource.Binding.EndpointID != r.Binding.EndpointID {
		return spec, errors.New("connector ownership missing or changed")
	}
	_, socket, done := supervisorPaths(p.owned.runtime, r.Session.ID)
	if spec.Socket != socket || spec.Done != done {
		return spec, errors.New("connector runtime location changed")
	}
	return spec, nil
}
func (p *Provider) startConnector(ctx context.Context, r *ingress.Resource) error {
	if r.Session.ID != "" {
		return errors.New("stop recorded connector before a fresh start")
	}
	var token string
	if err := p.owned.api.call(ctx, "GET", p.tunnelPath(r.Binding.EndpointID)+"/token", nil, &token); err != nil {
		return err
	}
	if token == "" {
		return errors.New("empty connector token")
	}
	var spec supervisorSpec
	id := digest(r.Generation + "\x00" + r.OperationID)
	path, socket, done := supervisorPaths(p.owned.runtime, id)
	r.Session = ingress.SessionIdentity{ID: id, Identity: map[string]string{"generation": r.Generation, "endpoint": r.Binding.EndpointID, "supervision": "private-socket-v1"}}
	_, err := createCredentialFile(p.owned.runtime.RuntimeDir, id, []byte(token), func(ref credentialFile) error {
		spec = supervisorSpec{Resource: *r, Helper: p.owned.runtime.HelperPath, Credential: ref, Socket: socket, Done: done}
		spec.Resource.ProviderState = nil
		r.ProviderState["connector"] = jsonText(spec)
		return p.save(*r)
	})
	token = ""
	if err != nil {
		if spec.Resource.Session.ID != "" {
			return finishSupervisor(spec, err)
		}
		return err
	}
	if err = safeio.AtomicWriteFile(path, []byte(jsonText(spec)), 0o600); err != nil {
		return finishSupervisor(spec, err)
	}
	// Do not attach the operation deadline to the long-lived supervisor. The
	// retained stdin pipe binds it to the host; timeout closes that pipe below.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), p.owned.supervisor, "--run", path) // #nosec G204 -- explicit supervisor, fixed arguments and non-secret private bootstrap path.
	cmd.Env = connectorEnvironment(p.owned.runtime.RuntimeDir)
	reader, writer, err := os.Pipe()
	if err != nil {
		return finishSupervisor(spec, err)
	}
	cmd.Stdin = reader
	if err = cmd.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return finishSupervisor(spec, errors.New("connector supervisor failed to start"))
	}
	_ = reader.Close()
	connectorLeases.Store(id, writer)
	go func() { _ = cmd.Wait(); connectorLeases.CompareAndDelete(id, writer); _ = writer.Close() }()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if running, err := supervisorCall(ctx, spec, "status"); err == nil {
			if running {
				return nil
			}
			return errors.New("connector exited during startup")
		}
		select {
		case <-ctx.Done():
			_ = writer.Close()
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (p *Provider) stopConnector(ctx context.Context, r *ingress.Resource) error {
	if r.Session.ID == "" {
		return nil
	}
	spec, err := p.connectorSpec(*r)
	if err != nil {
		return err
	}
	running, err := supervisorCall(ctx, spec, "stop")
	if err != nil {
		return err
	}
	if lease, ok := connectorLeases.LoadAndDelete(r.Session.ID); ok {
		_ = lease.(*os.File).Close()
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for running {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		nextRunning, callErr := supervisorCall(ctx, spec, "status")
		if callErr != nil {
			// Listener closing and durable completion are separate. Preserve state and
			// allow a bounded retry; no signal or artifact deletion is inferred.
			continue
		}
		running = nextRunning
	}
	if err = spec.Credential.remove(); err != nil {
		return err
	}
	r.Session = ingress.SessionIdentity{}
	return p.save(*r)
}

func serveSupervisor(listener net.Listener, id string, requestStop func()) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		var req supervisorRequest
		response := supervisorResponse{ID: id, Running: true}
		if json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&req) != nil || req.ID != id {
			response.Error = true
		} else {
			switch req.Action {
			case "status":
			case "stop":
				requestStop()
			case "":
				response.Error = true
			default:
				response.Error = true
			}
		}
		_ = json.NewEncoder(conn).Encode(response)
		_ = conn.Close()
	}
}
