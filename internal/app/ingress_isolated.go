package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/internal/tunnel"
	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

const ingressResourceKey = ingressMetadataPrefix + "resource_v1"

var ErrIngressProviderFailure = errors.New("isolated ingress provider operation failed")

func (s *Service) isolatedCoordinator() (ingress.MutationCoordinator, error) {
	if err := validateIngressOptions(s.configPath, s.ingress); err != nil {
		return nil, err
	}
	coordinator, ok := s.store.(ingress.MutationCoordinator)
	if !ok {
		return nil, fmt.Errorf("%w: ConfigStore must implement WithIngressMutation", ErrIngressSafetyUnavailable)
	}
	return coordinator, nil
}

func validateIsolatedSpec(spec IngressSpec) (IngressSpec, error) {
	spec, err := validateIngressSpec(spec)
	if err != nil {
		return spec, err
	}
	if spec.Provider == "" {
		return spec, errors.New("isolated ingress requires an explicit provider")
	}
	spec.PublicHost, err = isolatedPublicHost(spec.PublicHost)
	if err != nil {
		return spec, err
	}
	spec.DialHost, err = isolatedDialHost(spec.DialHost)
	if err != nil {
		return spec, err
	}
	binding, err := isolatedBinding(spec)
	if err != nil {
		return spec, err
	}
	spec.Binding = &binding
	return spec, nil
}

func isolatedPublicHost(value string) (string, error) {
	host, err := normalizePublicHost(value)
	if err != nil || len(host) > 253 {
		return "", errors.New("isolated ingress requires a valid public hostname")
	}
	for label := range strings.SplitSeq(host, ".") {
		if len(label) > 63 || !dialHostLabelPattern.MatchString(label) {
			return "", errors.New("invalid public hostname")
		}
	}
	return host, nil
}

func isolatedDialHost(value string) (string, error) {
	dial, err := NormalizeDialHost(value)
	if err != nil {
		return "", errors.New("invalid isolated ingress target host")
	}
	if dial == "" {
		dial = "127.0.0.1"
	}
	if ip := net.ParseIP(dial); ip != nil && ip.IsUnspecified() {
		return "", errors.New("isolated ingress target cannot be an unspecified address")
	}
	return dial, nil
}

func isolatedBinding(spec IngressSpec) (ingress.Binding, error) {
	binding := ingress.Binding{HTTPSPort: 443, Path: "/"}
	if spec.Binding != nil {
		binding = *spec.Binding
	}
	if binding.HTTPSPort == 0 {
		binding.HTTPSPort = 443
	}
	if binding.Path == "" {
		binding.Path = "/"
	}
	if binding.HTTPSPort < 1 || binding.HTTPSPort > 65535 {
		return binding, errors.New("invalid ingress HTTPS port")
	}
	if _, err := validateCallbackPath(binding.Path); err != nil || strings.ContainsAny(binding.Path, "%\\") {
		return binding, errors.New("invalid ingress exposure path")
	}
	if strings.ContainsAny(spec.CallbackPath, "%\\") {
		return binding, errors.New("encoded or ambiguous callback paths are unsupported")
	}
	if binding.Path != "/" && spec.CallbackPath != binding.Path && !strings.HasPrefix(spec.CallbackPath, binding.Path+"/") {
		return binding, errors.New("callback path is outside the exposure binding")
	}
	origin := ingressOrigin(spec.PublicHost, binding.HTTPSPort)
	if binding.ExpectedOrigin != "" && binding.ExpectedOrigin != origin {
		return binding, errors.New("expected origin does not match the exposure binding")
	}
	binding.ExpectedOrigin = origin
	return binding, nil
}

func ingressOrigin(host string, port int) string {
	if port != 443 {
		host = net.JoinHostPort(host, strconv.Itoa(port))
	}
	return "https://" + host
}

func isolatedSpecHash(spec IngressSpec) string {
	// The legacy hash remains unchanged for existing consumers. Binding is part
	// of the isolated fingerprint; provider-returned IDs do not rewrite intent.
	raw, _ := json.Marshal(spec.Binding)
	return ingressSpecHash(spec) + ":" + string(raw)
}

func (s *Service) ensureIsolatedIngress(ctx context.Context, spec IngressSpec) (result Ingress, err error) {
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = validateIngressOptions(s.configPath, s.ingress); err != nil {
		return result, err
	}
	spec, err = validateIsolatedSpec(spec)
	if err != nil {
		return result, err
	}
	coordinator, err := s.isolatedCoordinator()
	if err != nil {
		return result, err
	}
	provider, owned, err := s.ownedProvider(spec.Provider)
	if err != nil {
		return result, err
	}
	scope := s.mutationScope("ensure", IngressRef{Name: spec.Name, Owner: spec.Owner, ExpectedRevision: spec.ExpectedRevision})
	err = coordinator.WithIngressMutation(ctx, scope, func(ctx context.Context, rec ingress.Recorder) error {
		result, err = s.mutationService(ctx).ensureOwnedMutation(ctx, rec, spec, provider, owned)
		return err
	})
	return result, err
}

func (s *Service) ensureOwnedMutation(ctx context.Context, rec ingress.Recorder, spec IngressSpec, provider tunnel.Provider, owned ingress.OwnedProvider) (Ingress, error) {
	if pending, found, err := pendingMutation(ctx, rec); err != nil {
		return Ingress{}, err
	} else if found {
		return s.recoverOwnedMutation(ctx, rec, pending, "ensure", IngressRef{Name: spec.Name, Owner: spec.Owner, ExpectedRevision: spec.ExpectedRevision}, &spec)
	}
	configPath, cfg, err := s.LoadOrDefaultConfig()
	if err != nil {
		return Ingress{}, err
	}
	if cfg == nil {
		return Ingress{}, errors.New("isolated config store returned no config")
	}
	resource, idx, revision, current, err := prepareEnsureResource(cfg, spec, rec.OperationID())
	if err != nil {
		return Ingress{}, err
	}
	if current != nil {
		return *current, nil
	}
	callCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	secret, err := s.prepareOwnedProvider(callCtx, cfg, provider, owned, resource)
	defer clear(secret)
	if err != nil {
		return Ingress{}, err
	}
	candidate := isolatedCandidate(spec)
	if err := setIsolatedResource(&candidate, resource, IngressStateConfigured, revision); err != nil {
		return Ingress{}, err
	}
	tracked, err := newOperationRecorder(rec, "ensure", cfg, idx, candidate, secret)
	if err != nil {
		return Ingress{}, err
	}
	rec = tracked
	if err := recordIngressProgress(ctx, rec, "pending", resource, secret); err != nil {
		return Ingress{}, err
	}
	updated, callErr := owned.EnsureOwnedIngress(callCtx, cloneResource(resource))
	if callErr != nil {
		return Ingress{}, uncertainIngress(ctx, rec, resource, callCtx, "ensure")
	}
	if err := validateResourceResult(resource, updated, false, secret); err != nil {
		_ = recordIngressProgress(ctx, rec, "uncertain", resource, nil)
		return Ingress{}, err
	}
	if err := recordIngressProgress(ctx, rec, "confirmed", updated, secret); err != nil {
		return Ingress{}, err
	}
	if err := setIsolatedResource(&candidate, updated, IngressStateConfigured, revision); err != nil {
		return Ingress{}, err
	}
	if idx < 0 {
		cfg.Apps = append(cfg.Apps, candidate)
	} else {
		cfg.Apps[idx] = candidate
	}
	if err := recordIngressProgress(ctx, rec, "persisting", updated, secret); err != nil {
		return Ingress{}, err
	}
	if err := s.store.Save(configPath, cfg); err != nil {
		return Ingress{}, err
	}
	return ingressFromApp(candidate)
}

func prepareEnsureResource(cfg *config.Config, spec IngressSpec, operationID string) (ingress.Resource, int, int64, *Ingress, error) {
	resource := ingress.Resource{Name: spec.Name, Provider: spec.Provider, PublicHost: spec.PublicHost, LocalURL: (&url.URL{Scheme: "http", Host: net.JoinHostPort(spec.DialHost, strconv.Itoa(spec.LocalPort))}).String(), Owner: ingress.Owner{System: spec.Owner.System, ScopeID: spec.Owner.ScopeID}, Binding: *spec.Binding, OperationID: operationID}
	idx := findAppByName(cfg, spec.Name)
	if idx >= 0 {
		resource, revision, current, err := reconcileEnsureResource(cfg.Apps[idx], spec, resource)
		return resource, idx, revision, current, err
	}
	if spec.Binding.EndpointID != "" {
		return resource, idx, 0, nil, errors.New("endpoint ID cannot adopt an unowned endpoint")
	}
	if spec.ExpectedRevision != nil && *spec.ExpectedRevision != 0 {
		return resource, idx, 0, nil, errors.New("ingress revision conflict: record does not exist")
	}
	generation := make([]byte, 16)
	if _, err := rand.Read(generation); err != nil {
		return resource, idx, 0, nil, err
	}
	resource.Generation = hex.EncodeToString(generation)
	return resource, idx, 1, nil, nil
}

func reconcileEnsureResource(candidate config.App, spec IngressSpec, resource ingress.Resource) (ingress.Resource, int64, *Ingress, error) {
	current, err := ingressFromApp(candidate)
	if err != nil {
		return resource, 0, nil, errors.New("ingress conflicts with an unmanaged app")
	}
	if err := checkIngressRef(current, IngressRef{Name: spec.Name, Owner: spec.Owner, ExpectedRevision: spec.ExpectedRevision}); err != nil {
		return resource, 0, nil, err
	}
	previous, err := isolatedResource(candidate)
	if err != nil {
		return resource, 0, nil, err
	}
	if previous.Provider != spec.Provider {
		return resource, 0, nil, errors.New("release owned ingress before changing its provider")
	}
	if spec.Binding.EndpointID != "" && !matchesResumeBinding(spec, previous) {
		return resource, 0, nil, errors.New("endpoint resume requires matching owner, revision, provider and binding")
	}
	if candidate.Metadata[ingressMetadataSpecHash] == isolatedSpecHash(spec) {
		return resource, current.Revision, &current, nil
	}
	if previous.Session.ID != "" {
		return resource, 0, nil, errors.New("stop owned ingress before changing its target or binding")
	}
	resource.Generation = previous.Generation
	resource.Binding.EndpointID = previous.Binding.EndpointID
	resource.ProviderState = previous.ProviderState
	return resource, current.Revision + 1, nil, nil
}

func matchesResumeBinding(spec IngressSpec, previous ingress.Resource) bool {
	return spec.ExpectedRevision != nil && spec.Binding.EndpointID == previous.Binding.EndpointID && spec.Binding.HTTPSPort == previous.Binding.HTTPSPort && spec.Binding.Path == previous.Binding.Path && spec.PublicHost == previous.PublicHost
}

func isolatedCandidate(spec IngressSpec) config.App {
	candidate := config.App{Name: spec.Name, LocalHost: spec.Name, LocalPort: spec.LocalPort, DialHost: spec.DialHost, Metadata: copyConsumerMetadata(spec.Metadata)}
	candidate.Metadata[ingressMetadataVersionKey] = ingressMetadataVersion
	candidate.Metadata[ingressMetadataOwnerSystem] = spec.Owner.System
	candidate.Metadata[ingressMetadataOwnerScope] = spec.Owner.ScopeID
	candidate.Metadata[ingressMetadataCallbackPath] = spec.CallbackPath
	candidate.Metadata[ingressMetadataSpecHash] = isolatedSpecHash(spec)
	return candidate
}

func pendingMutation(ctx context.Context, rec ingress.Recorder) (ingress.Progress, bool, error) {
	if err := ctx.Err(); err != nil {
		return ingress.Progress{}, false, err
	}
	if rec == nil || rec.OperationID() == "" {
		return ingress.Progress{}, false, ErrIngressSafetyUnavailable
	}
	return rec.PendingProgress(ctx)
}

func (s *Service) runIsolatedIngress(ctx context.Context, action string, ref IngressRef) (result Ingress, err error) {
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = validateIngressOptions(s.configPath, s.ingress); err != nil {
		return result, err
	}
	ref.Name, err = normalizeAppNameInput(ref.Name)
	if err != nil {
		return result, err
	}
	ref.Owner = IngressOwnership{System: strings.TrimSpace(ref.Owner.System), ScopeID: strings.TrimSpace(ref.Owner.ScopeID)}
	if ref.Owner.System == "" || ref.Owner.ScopeID == "" {
		return result, errors.New("ingress owner is required")
	}
	coordinator, err := s.isolatedCoordinator()
	if err != nil {
		return result, err
	}
	err = coordinator.WithIngressMutation(ctx, s.mutationScope(action, ref), func(ctx context.Context, rec ingress.Recorder) error {
		result, err = s.mutationService(ctx).runOwnedMutation(ctx, rec, action, ref)
		return err
	})
	return result, err
}

type isolatedOperation struct {
	path     string
	cfg      *config.Config
	idx      int
	current  Ingress
	resource ingress.Resource
	provider tunnel.Provider
	owned    ingress.OwnedProvider
}

func (s *Service) loadIsolatedOperation(ref IngressRef) (isolatedOperation, error) {
	var op isolatedOperation
	var err error
	op.path, op.cfg, err = s.LoadOrDefaultConfig()
	if err != nil {
		return op, err
	}
	if op.cfg == nil {
		return op, errors.New("isolated config store returned no config")
	}
	op.idx = findAppByName(op.cfg, ref.Name)
	if op.idx < 0 {
		return op, nil
	}
	op.current, err = ingressFromApp(op.cfg.Apps[op.idx])
	if err != nil {
		return op, err
	}
	if err := checkIngressRef(op.current, ref); err != nil {
		return op, err
	}
	op.resource, err = isolatedResource(op.cfg.Apps[op.idx])
	if err != nil {
		return op, err
	}
	op.provider, op.owned, err = s.ownedProvider(op.resource.Provider)
	return op, err
}

func (s *Service) runOwnedMutation(ctx context.Context, rec ingress.Recorder, action string, ref IngressRef) (Ingress, error) {
	if pending, found, err := pendingMutation(ctx, rec); err != nil {
		return Ingress{}, err
	} else if found {
		return s.recoverOwnedMutation(ctx, rec, pending, action, ref, nil)
	}
	op, err := s.loadIsolatedOperation(ref)
	if err != nil {
		return Ingress{}, err
	}
	if op.idx < 0 {
		if action == "release" {
			return Ingress{}, nil
		}
		return Ingress{}, errors.New("ingress not found")
	}
	if action == "stop" && op.current.State == IngressStateStopped && op.resource.Session.ID == "" {
		return op.current, nil
	}
	op.resource.OperationID = rec.OperationID()
	callCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	secret, err := s.prepareOwnedProvider(callCtx, op.cfg, op.provider, op.owned, op.resource)
	defer clear(secret)
	if err != nil {
		return Ingress{}, err
	}
	if action == "status" || (action == "start" && op.current.State == IngressStateRunning) {
		return observedOwnedStatus(callCtx, action, op, secret)
	}
	desired := cloneApp(op.cfg.Apps[op.idx])
	if err := setIsolatedResource(&desired, op.resource, op.current.State, op.current.Revision+1); err != nil {
		return Ingress{}, err
	}
	tracked, err := newOperationRecorder(rec, action, op.cfg, op.idx, desired, secret)
	if err != nil {
		return Ingress{}, err
	}
	rec = tracked
	if err := recordIngressProgress(ctx, rec, "pending", op.resource, secret); err != nil {
		return Ingress{}, err
	}
	updated, state, err := executeOwnedAction(callCtx, rec, action, op, secret)
	if err != nil {
		return Ingress{}, err
	}
	if err := recordIngressProgress(ctx, rec, "confirmed", updated, secret); err != nil {
		return Ingress{}, err
	}
	return s.persistOwnedAction(ctx, rec, action, op, updated, state, secret)
}

func observedOwnedStatus(ctx context.Context, action string, op isolatedOperation, secret []byte) (Ingress, error) {
	status, err := ownedStatus(ctx, op, secret)
	if err != nil {
		return status, err
	}
	if action == "start" && status.State != IngressStateRunning {
		return status, errors.New("owned session is unavailable; stop and reconcile it before starting again")
	}
	return status, nil
}

func ownedStatus(ctx context.Context, op isolatedOperation, secret []byte) (Ingress, error) {
	status, err := op.owned.StatusOwnedIngress(ctx, cloneResource(op.resource))
	if err != nil {
		return Ingress{}, privateProviderError(ctx, "status")
	}
	if containsCredential(status, secret) {
		return Ingress{}, errors.New("provider returned credential material in status")
	}
	result := op.current
	if status.Ready {
		if op.resource.Session.ID == "" || status.SessionID != op.resource.Session.ID {
			return Ingress{}, errors.New("provider status does not match the owned session")
		}
		result.State = IngressStateRunning
	} else if result.State == IngressStateRunning {
		result.State = IngressStateStopped
	}
	return result, nil
}

func executeOwnedAction(ctx context.Context, rec ingress.Recorder, action string, op isolatedOperation, secret []byte) (ingress.Resource, IngressState, error) {
	updated := cloneResource(op.resource)
	state := op.current.State
	switch action {
	case "start":
		var err error
		updated, err = op.owned.StartOwnedIngress(ctx, cloneResource(op.resource))
		if err == nil {
			err = validateResourceResult(op.resource, updated, true, secret)
		}
		if err == nil && !validSessionIdentity(updated.Session) {
			err = errors.New("owned session requires verifiable identity")
		}
		if err != nil {
			return updated, state, uncertainIngress(ctx, rec, op.resource, ctx, "start or session identity validation")
		}
		state = IngressStateRunning
	case "stop", "release":
		if err := stopVerifiedOwned(ctx, op.owned, op.resource, false); err != nil {
			return updated, state, uncertainIngress(ctx, rec, op.resource, ctx, "stop")
		}
		updated.Session = ingress.SessionIdentity{}
		state = IngressStateStopped
		if action == "release" {
			if err := releaseOwnedEndpoint(ctx, rec, op.owned, updated, secret); err != nil {
				return updated, state, err
			}
		}
	default:
		return updated, state, errors.New("unknown isolated ingress operation")
	}
	return updated, state, nil
}

func validSessionIdentity(session ingress.SessionIdentity) bool {
	return session.ID != "" && len(session.Identity) > 0 && session.PID >= 0 && (session.PID == 0 || (session.Executable != "" && !session.StartedAt.IsZero()))
}

func releaseOwnedEndpoint(ctx context.Context, rec ingress.Recorder, owned ingress.OwnedProvider, resource ingress.Resource, secret []byte) error {
	if err := recordIngressProgress(ctx, rec, "confirmed", resource, secret); err != nil {
		return err
	}
	if tracked, ok := rec.(*operationRecorder); ok {
		tracked.intent.Step = "release"
	}
	if err := recordIngressProgress(ctx, rec, "pending", resource, secret); err != nil {
		return err
	}
	if err := owned.ReleaseOwnedIngress(ctx, cloneResource(resource)); err != nil {
		return uncertainIngress(ctx, rec, resource, ctx, "release")
	}
	return nil
}

func (s *Service) persistOwnedAction(ctx context.Context, rec ingress.Recorder, action string, op isolatedOperation, updated ingress.Resource, state IngressState, secret []byte) (Ingress, error) {
	var result Ingress
	if action == "release" {
		op.cfg.Apps = append(op.cfg.Apps[:op.idx], op.cfg.Apps[op.idx+1:]...)
	} else {
		if err := setIsolatedResource(&op.cfg.Apps[op.idx], updated, state, op.current.Revision+1); err != nil {
			return result, err
		}
		var err error
		result, err = ingressFromApp(op.cfg.Apps[op.idx])
		if err != nil {
			return result, err
		}
	}
	if err := recordIngressProgress(ctx, rec, "persisting", updated, secret); err != nil {
		return result, err
	}
	return result, s.store.Save(op.path, op.cfg)
}

func (s *Service) mutationScope(action string, ref IngressRef) ingress.MutationScope {
	return ingress.MutationScope{ConfigPath: filepath.Clean(s.configPath), StateDir: filepath.Clean(s.ingress.StateDir), Operation: action, Name: ref.Name, Owner: ingress.Owner{System: ref.Owner.System, ScopeID: ref.Owner.ScopeID}, ExpectedRevision: ref.ExpectedRevision}
}

func (s *Service) ownedProvider(name string) (tunnel.Provider, ingress.OwnedProvider, error) {
	provider, err := s.providerRegistry.Resolve(name)
	if err != nil {
		return nil, nil, errors.New("isolated ingress provider is unavailable")
	}
	owned, ok := provider.(ingress.OwnedProvider)
	if !ok {
		return nil, nil, fmt.Errorf("%w: provider must implement the owned ingress lifecycle", ErrIngressSafetyUnavailable)
	}
	return provider, owned, nil
}

func (s *Service) prepareOwnedProvider(ctx context.Context, cfg *config.Config, provider tunnel.Provider, owned ingress.OwnedProvider, resource ingress.Resource) ([]byte, error) {
	private, privateOK := provider.(ingress.PrivateInitializer)
	ref := cfg.Tunnel.Providers[resource.Provider].CredentialRef
	if ref != "" && (!privateOK || s.ingress.Credentials == nil) {
		return nil, fmt.Errorf("%w: private credential resolver and initializer are required", ErrIngressSafetyUnavailable)
	}
	helper, err := s.resolveIngressHelper(ctx, resource.Provider)
	if err != nil {
		return nil, err
	}
	runtime := ingress.Runtime{StateDir: filepath.Clean(s.ingress.StateDir), RuntimeDir: filepath.Clean(s.ingress.RuntimeDir), HelperPath: helper}
	if err := owned.ValidateIngress(ctx, cloneResource(resource), runtime); err != nil {
		return nil, privateProviderError(ctx, "binding/helper validation")
	}
	var secret []byte
	if ref != "" {
		secret, err = s.ingress.Credentials.ResolveCredential(ctx, ref)
		if err != nil || len(secret) == 0 {
			clear(secret)
			return nil, privateProviderError(ctx, "credential resolution")
		}
	}
	settings := tunnelProviderConfig(cfg, resource.Provider)
	if privateOK {
		err = private.InitIngressPrivate(ctx, settings.Values, runtime, secret)
	} else {
		err = provider.Init(ctx, settings)
	}
	if err != nil {
		return secret, privateProviderError(ctx, "initialization")
	}
	return secret, nil
}

func (s *Service) resolveIngressHelper(ctx context.Context, provider string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	configured := s.ingress.Helpers[provider]
	if s.ingress.HelperResolver != nil {
		helper, err := s.ingress.HelperResolver.ResolveIngressHelper(ctx, provider, configured)
		if err != nil {
			return "", privateProviderError(ctx, "helper discovery")
		}
		if helper == "" && (provider == "cloudflare" || provider == "tailscale") {
			return "", errors.New("helper resolver did not resolve the required provider executable")
		}
		if helper != "" && (!filepath.IsAbs(helper) || strings.ContainsRune(helper, 0)) {
			return "", errors.New("helper resolver must return an absolute executable path")
		}
		return helper, nil
	}
	if configured != "" && !filepath.IsAbs(configured) {
		return "", errors.New("configured ingress helper path must be absolute")
	}
	helper := configured
	if helper == "" {
		switch provider {
		case "cloudflare":
			helper = "cloudflared"
		case "tailscale":
			helper = "tailscale"
		default:
			return "", nil
		}
	}
	resolved, err := exec.LookPath(helper)
	if err != nil {
		return "", errors.New("ingress helper unavailable; configure an installed executable")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("ingress helper must be a regular executable")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", errors.New("cannot resolve ingress helper path")
	}
	return resolved, nil
}

func privateProviderError(ctx context.Context, operation string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrIngressProviderFailure, operation)
}

func uncertainIngress(ctx context.Context, rec ingress.Recorder, resource ingress.Resource, callCtx context.Context, operation string) error {
	// Retain the pending record if cancellation prevents the uncertain write.
	if err := recordIngressProgress(ctx, rec, "uncertain", resource, nil); err != nil {
		return err
	}
	return privateProviderError(callCtx, operation)
}

func recordIngressProgress(ctx context.Context, rec ingress.Recorder, phase string, resource ingress.Resource, secret []byte) error {
	if _, err := json.Marshal(resource); err != nil {
		return errors.New("provider returned unserializable ownership state")
	}
	if containsCredential(resource, secret) {
		return errors.New("provider returned credential material in ownership state")
	}
	return rec.Record(ctx, ingress.Progress{Phase: phase, Resource: cloneResource(resource)})
}

func containsCredential(value any, secret []byte) bool {
	if len(secret) == 0 {
		return false
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return true
	}
	encoded, _ := json.Marshal(string(secret))
	return bytes.Contains(raw, secret) || (len(encoded) > 2 && bytes.Contains(raw, encoded[1:len(encoded)-1]))
}

func cloneResource(resource ingress.Resource) ingress.Resource {
	out := resource
	out.ProviderState = maps.Clone(resource.ProviderState)
	out.Session.Identity = maps.Clone(resource.Session.Identity)
	return out
}

func validateResourceResult(before, after ingress.Resource, session bool, secret []byte) error {
	if containsCredential(after, secret) {
		return errors.New("provider returned credential material in ownership state")
	}
	if after.Name != before.Name || after.Provider != before.Provider || after.PublicHost != before.PublicHost || after.LocalURL != before.LocalURL || after.Owner != before.Owner || after.Generation != before.Generation || after.OperationID != before.OperationID || after.Binding.HTTPSPort != before.Binding.HTTPSPort || after.Binding.Path != before.Binding.Path || after.Binding.ExpectedOrigin != before.Binding.ExpectedOrigin || after.Binding.EndpointID == "" || (before.Binding.EndpointID != "" && after.Binding.EndpointID != before.Binding.EndpointID) {
		return errors.New("provider changed the owned ingress identity")
	}
	if !session && !reflect.DeepEqual(before.Session, after.Session) {
		return errors.New("ensure cannot change session identity")
	}
	return nil
}

func setIsolatedResource(candidate *config.App, resource ingress.Resource, state IngressState, revision int64) error {
	raw, err := json.Marshal(resource)
	if err != nil {
		return err
	}
	candidate.Metadata[ingressResourceKey] = string(raw)
	candidate.Metadata[ingressMetadataRevision] = strconv.FormatInt(revision, 10)
	candidate.Metadata[ingressMetadataState] = string(state)
	candidate.PublicEndpoint = config.AppPublicEndpoint{Provider: resource.Provider, Host: resource.PublicHost, EndpointID: resource.Binding.EndpointID, ActiveSessionID: resource.Session.ID, ActiveSessionPID: resource.Session.PID}
	if !resource.Session.StartedAt.IsZero() {
		candidate.PublicEndpoint.ActiveSessionStarted = resource.Session.StartedAt.UTC().Format(time.RFC3339)
	}
	return nil
}

func isolatedResource(candidate config.App) (ingress.Resource, error) {
	var resource ingress.Resource
	if err := json.Unmarshal([]byte(candidate.Metadata[ingressResourceKey]), &resource); err != nil || resource.Generation == "" || resource.Binding.EndpointID == "" {
		return resource, fmt.Errorf("%w: legacy or incomplete ownership evidence requires explicit recovery", ErrIngressSafetyUnavailable)
	}
	if resource.Name != candidate.Name || resource.Provider != candidate.PublicEndpoint.Provider || resource.PublicHost != candidate.PublicEndpoint.Host || resource.Binding.EndpointID != candidate.PublicEndpoint.EndpointID || resource.Session.ID != candidate.PublicEndpoint.ActiveSessionID || resource.Owner.System != candidate.Metadata[ingressMetadataOwnerSystem] || resource.Owner.ScopeID != candidate.Metadata[ingressMetadataOwnerScope] {
		return resource, errors.New("persisted ingress ownership identity conflicts")
	}
	target := (&url.URL{Scheme: "http", Host: net.JoinHostPort(candidate.DialHost, strconv.Itoa(candidate.LocalPort))}).String()
	if resource.LocalURL != target || resource.Binding.HTTPSPort < 1 || resource.Binding.HTTPSPort > 65535 || resource.Binding.ExpectedOrigin != ingressOrigin(resource.PublicHost, resource.Binding.HTTPSPort) || resource.Owner.System == "" || resource.Owner.ScopeID == "" || resource.Session.PID != candidate.PublicEndpoint.ActiveSessionPID {
		return resource, errors.New("persisted ingress binding conflicts")
	}
	if _, err := validateCallbackPath(resource.Binding.Path); err != nil {
		return resource, errors.New("persisted ingress path is invalid")
	}
	return resource, nil
}

func (s *Service) mutationService(ctx context.Context) *Service {
	if tx := config.Transaction(ctx); tx != nil {
		copy := *s
		copy.store = tx
		return &copy
	}
	return s
}
