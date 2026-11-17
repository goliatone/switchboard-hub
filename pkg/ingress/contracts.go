// Package ingress contains the transport-independent contracts for isolated,
// owned tunnel operations. It does not create processes, files or listeners.
package ingress

import (
	"context"
	"encoding/json"
	"time"
)

// Binding identifies one exposure. EndpointID is a resume assertion, never
// authority to adopt an existing remote endpoint. Zero port/path mean 443/"/".
type Binding struct {
	EndpointID     string `json:"endpoint_id,omitempty" yaml:"endpoint_id,omitempty"`
	HTTPSPort      int    `json:"https_port,omitempty" yaml:"https_port,omitempty"`
	Path           string `json:"path,omitempty" yaml:"path,omitempty"`
	ExpectedOrigin string `json:"expected_origin,omitempty" yaml:"expected_origin,omitempty"`
}

type Owner struct {
	System  string
	ScopeID string
}

// SessionIdentity must be verified against the current resource/process before
// signaling it. A PID alone never establishes ownership. Identity contains only
// non-secret provider-specific start/node/profile identifiers.
type SessionIdentity struct {
	ID         string
	PID        int
	Executable string
	StartedAt  time.Time
	Identity   map[string]string
}

// Resource is private durable ownership evidence, not a public status payload.
// ProviderState contains non-secret resource IDs/provenance only. Providers
// must preserve owner, generation, target and binding on every operation.
type Resource struct {
	Name          string
	Provider      string
	PublicHost    string
	LocalURL      string
	Owner         Owner
	Generation    string
	OperationID   string
	Binding       Binding
	Session       SessionIdentity
	ProviderState map[string]string
}

type Runtime struct {
	StateDir   string
	RuntimeDir string
	HelperPath string
}

type Status struct {
	Ready     bool
	SessionID string
}

// OwnedProvider is optional: legacy Provider implementations remain valid.
// ValidateIngress checks binding/helper support without changing provider state.
// Each mutation must verify ownership and current identity; cleanup is scoped,
// retries reconcile recorded progress, and recovery never starts an exposure.
// Stop retains endpoint/DNS configuration. Release removes only owned resources.
// Returning an error must not discard recovery authority. T05-T07 implement
// these obligations for concrete stores and providers.
type OwnedProvider interface {
	ValidateIngress(context.Context, Resource, Runtime) error
	EnsureOwnedIngress(context.Context, Resource) (Resource, error)
	StartOwnedIngress(context.Context, Resource) (Resource, error)
	StopOwnedIngress(context.Context, Resource) error
	StatusOwnedIngress(context.Context, Resource) (Status, error)
	ReleaseOwnedIngress(context.Context, Resource) error
}

// PrivateInitializer receives transient credential bytes for this operation.
// It may borrow the buffer only during this operation; never copy or retain
// credentials beyond it, serialize/log them, or pass the API key to a child.
// Initialization must not create or change remote resources.
// The service clears the supplied buffer when the operation returns. Runtime
// configuration and credential resolution never change process-global env.
type PrivateInitializer interface {
	InitIngressPrivate(context.Context, map[string]string, Runtime, []byte) error
}

type MutationScope struct {
	ConfigPath       string
	StateDir         string
	Operation        string
	Name             string
	Owner            Owner
	ExpectedRevision *int64
}

// Desired is one ingress record, never a snapshot of the whole config. BaseHash
// protects the target against operator edits while allowing unrelated edits.
type Progress struct {
	Operation string
	Step      string
	BaseHash  string
	Desired   json.RawMessage
	Phase     string // pending, confirmed, uncertain, persisting
	Resource  Resource
}

// Recorder durably records progress before an external mutation and after its
// result. OperationID survives retries of an uncertain operation. Failed writes
// retain the last durable ownership evidence and must never permit blind retry.
type Recorder interface {
	OperationID() string
	// PendingProgress reports an interrupted prior mutation for reconciliation.
	PendingProgress(context.Context) (Progress, bool, error)
	Record(context.Context, Progress) error
}

// MutationCoordinator is an optional ConfigStore capability. Hold a cancellable
// cross-process lock for the canonical store around the entire callback. Supply
// a durable recorder and retain uncertain progress on error; commit completion
// only after the callback's provider operation and config Save both succeed.
// It must invoke the callback exactly once, synchronously. Other writers of the
// same store must use the same coordination. No implementation is supplied here.
type MutationCoordinator interface {
	WithIngressMutation(context.Context, MutationScope, func(context.Context, Recorder) error) error
}

// HelperResolver resolves an explicitly configured executable or a provider's
// standard helper. Discovery alone does not certify version-specific safety.
type HelperResolver interface {
	ResolveIngressHelper(context.Context, string, string) (string, error)
}

// RecoveryProvider is an optional read-only reconciliation capability. It must
// inspect the exact attempt by operation ID, owner, generation and endpoint.
// "applied" and "not-applied" require authoritative evidence; ambiguity returns
// "unknown". It must never create, start, adopt or delete resources.
// VerifyOwnedIngressSession checks current executable/start and resource
// association; StopOwnedIngress MUST repeat verification at termination time.
// If safe identity cannot be established (including PID reuse), leave it alone.
type RecoveryProvider interface {
	ReconcileOwnedIngress(context.Context, Progress) (RecoveryResult, error)
	VerifyOwnedIngressSession(context.Context, Resource) error
}

type RecoveryResult struct {
	Outcome  string // applied, not-applied, unknown
	Resource Resource
}
