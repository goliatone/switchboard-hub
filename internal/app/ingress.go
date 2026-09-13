package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goliatone/switchboard-hub/internal/config"
)

const (
	ingressMetadataPrefix       = "switchboard.ingress."
	ingressMetadataVersion      = "1"
	ingressMetadataOwnerSystem  = ingressMetadataPrefix + "owner_system"
	ingressMetadataOwnerScope   = ingressMetadataPrefix + "owner_scope"
	ingressMetadataCallbackPath = ingressMetadataPrefix + "callback_path"
	ingressMetadataRevision     = ingressMetadataPrefix + "revision"
	ingressMetadataState        = ingressMetadataPrefix + "state"
	ingressMetadataSpecHash     = ingressMetadataPrefix + "spec_hash"
	ingressMetadataVersionKey   = ingressMetadataPrefix + "version"
)

type IngressState string

const (
	IngressStateConfigured IngressState = "configured"
	IngressStateRunning    IngressState = "running"
	IngressStateStopped    IngressState = "stopped"
)

type IngressOwnership struct {
	System  string
	ScopeID string
}

type IngressSpec struct {
	Name             string
	LocalPort        int
	DialHost         string
	Provider         string
	PublicHost       string
	CallbackPath     string
	Owner            IngressOwnership
	ExpectedRevision *int64
	Metadata         map[string]string
}

type IngressRef struct {
	Name             string
	Owner            IngressOwnership
	ExpectedRevision *int64
}

type Ingress struct {
	Name         string
	LocalHost    string
	LocalPort    int
	DialHost     string
	Provider     string
	PublicHost   string
	CallbackPath string
	CallbackURL  string
	EndpointID   string
	SessionID    string
	Owner        IngressOwnership
	State        IngressState
	Revision     int64
	Metadata     map[string]string
}

func (s *Service) EnsureIngress(ctx context.Context, spec IngressSpec) (Ingress, error) {
	normalized, err := validateIngressSpec(spec)
	if err != nil {
		return Ingress{}, err
	}
	configPath, cfg, err := s.LoadOrCreateDefaultConfig()
	if err != nil {
		return Ingress{}, err
	}
	idx := findAppByName(cfg, normalized.Name)
	if idx >= 0 {
		current, managedErr := ingressFromApp(cfg.Apps[idx])
		if managedErr != nil {
			return Ingress{}, fmt.Errorf("ingress %q conflicts with an unmanaged app", normalized.Name)
		}
		if err := checkIngressRef(current, IngressRef{Name: normalized.Name, Owner: normalized.Owner, ExpectedRevision: normalized.ExpectedRevision}); err != nil {
			return Ingress{}, err
		}
		if cfg.Apps[idx].Metadata[ingressMetadataSpecHash] == ingressSpecHash(normalized) {
			return current, nil
		}
	} else {
		if normalized.ExpectedRevision != nil && *normalized.ExpectedRevision != 0 {
			return Ingress{}, fmt.Errorf("ingress %q revision conflict: expected %d, record does not exist", normalized.Name, *normalized.ExpectedRevision)
		}
		created, createErr := upsertAppContext(ctx, cfg, normalized.Name, normalized.LocalPort, &CreateAppOptions{DialHost: normalized.DialHost})
		if createErr != nil {
			return Ingress{}, createErr
		}
		idx = findAppByName(cfg, created.Name)
	}

	dialHost, err := NormalizeDialHost(normalized.DialHost)
	if err != nil {
		return Ingress{}, err
	}
	cfg.Apps[idx].LocalPort = normalized.LocalPort
	cfg.Apps[idx].DialHost = dialHost
	if dialHost != "" {
		cfg.Apps[idx].ResolvedDialHost = ""
	}
	if _, err := s.EnsurePublicEndpointContext(ctx, cfg, normalized.Name, normalized.Provider, normalized.PublicHost); err != nil {
		return Ingress{}, err
	}

	revision := int64(1)
	if current, currentErr := ingressFromApp(cfg.Apps[idx]); currentErr == nil {
		revision = current.Revision + 1
	}
	metadata := copyConsumerMetadata(normalized.Metadata)
	metadata[ingressMetadataVersionKey] = ingressMetadataVersion
	metadata[ingressMetadataOwnerSystem] = normalized.Owner.System
	metadata[ingressMetadataOwnerScope] = normalized.Owner.ScopeID
	metadata[ingressMetadataCallbackPath] = normalized.CallbackPath
	metadata[ingressMetadataRevision] = strconv.FormatInt(revision, 10)
	metadata[ingressMetadataState] = string(IngressStateConfigured)
	metadata[ingressMetadataSpecHash] = ingressSpecHash(normalized)
	cfg.Apps[idx].Metadata = metadata
	upsertLegacyRoute(cfg, cfg.Apps[idx].LocalHost, cfg.Apps[idx].LocalPort, ConfiguredDialHost(cfg.Apps[idx]))
	if err := s.store.Save(configPath, cfg); err != nil {
		return Ingress{}, err
	}
	return ingressFromApp(cfg.Apps[idx])
}

func (s *Service) ListIngress(_ context.Context, owner *IngressOwnership) ([]Ingress, error) {
	_, cfg, err := s.LoadOrDefaultConfig()
	if err != nil {
		return nil, err
	}
	out := make([]Ingress, 0)
	for _, candidate := range cfg.Apps {
		ingress, ingressErr := ingressFromApp(candidate)
		if ingressErr != nil {
			continue
		}
		if owner != nil && (ingress.Owner.System != strings.TrimSpace(owner.System) || ingress.Owner.ScopeID != strings.TrimSpace(owner.ScopeID)) {
			continue
		}
		out = append(out, ingress)
	}
	return out, nil
}

func (s *Service) StartIngress(ctx context.Context, ref IngressRef) (Ingress, error) {
	current, err := s.loadIngress(ref)
	if err != nil {
		return Ingress{}, err
	}
	if current.State == IngressStateRunning {
		return s.StatusIngress(ctx, ref)
	}
	if err := s.AppUpContext(ctx, current.Name); err != nil {
		return Ingress{}, err
	}
	return s.updateIngressState(current.Name, current.Owner, IngressStateRunning)
}

func (s *Service) StopIngress(ctx context.Context, ref IngressRef) (Ingress, error) {
	current, err := s.loadIngress(ref)
	if err != nil {
		return Ingress{}, err
	}
	if current.State == IngressStateStopped && current.SessionID == "" {
		return current, nil
	}
	if err := s.AppDownContext(ctx, current.Name); err != nil {
		return Ingress{}, err
	}
	return s.updateIngressState(current.Name, current.Owner, IngressStateStopped)
}

func (s *Service) StatusIngress(ctx context.Context, ref IngressRef) (Ingress, error) {
	current, err := s.loadIngress(ref)
	if err != nil {
		return Ingress{}, err
	}
	statuses, err := s.AppTunnelHealthStatusContext(ctx)
	if err != nil {
		return Ingress{}, err
	}
	for _, status := range statuses {
		if status.AppName != current.Name {
			continue
		}
		current.SessionID = status.SessionID
		if status.Ready {
			current.State = IngressStateRunning
		} else if current.State == IngressStateRunning {
			current.State = IngressStateStopped
		}
		break
	}
	return current, nil
}

func (s *Service) ReleaseIngress(ctx context.Context, ref IngressRef) error {
	current, err := s.loadIngress(ref)
	if err != nil {
		return err
	}
	configPath, cfg, err := s.LoadOrDefaultConfig()
	if err != nil {
		return err
	}
	idx := findAppByName(cfg, current.Name)
	if idx < 0 {
		return fmt.Errorf("ingress not found: %s", current.Name)
	}
	if _, err := s.StopAppRuntimeContext(ctx, cfg, current.Name); err != nil {
		return err
	}
	endpoint := cfg.Apps[idx].PublicEndpoint
	if strings.TrimSpace(endpoint.EndpointID) != "" && strings.TrimSpace(endpoint.Provider) != "" {
		provider, err := s.providerRegistry.Resolve(endpoint.Provider)
		if err != nil {
			return err
		}
		if err := initProviderWithConfigContext(ctx, provider, cfg, endpoint.Provider, 20*time.Second); err != nil {
			return err
		}
		removeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = provider.RemoveEndpoint(removeCtx, endpoint.EndpointID)
		cancel()
		if err != nil {
			return err
		}
	}
	host := cfg.Apps[idx].LocalHost
	cfg.Apps = append(cfg.Apps[:idx], cfg.Apps[idx+1:]...)
	removeLegacyRouteByHost(cfg, host)
	return s.store.Save(configPath, cfg)
}

func (s *Service) loadIngress(ref IngressRef) (Ingress, error) {
	name, err := normalizeAppNameInput(ref.Name)
	if err != nil {
		return Ingress{}, err
	}
	_, cfg, err := s.LoadOrDefaultConfig()
	if err != nil {
		return Ingress{}, err
	}
	idx := findAppByName(cfg, name)
	if idx < 0 {
		return Ingress{}, fmt.Errorf("ingress not found: %s", name)
	}
	current, err := ingressFromApp(cfg.Apps[idx])
	if err != nil {
		return Ingress{}, fmt.Errorf("app %q is not a managed ingress", name)
	}
	if err := checkIngressRef(current, ref); err != nil {
		return Ingress{}, err
	}
	return current, nil
}

func (s *Service) updateIngressState(name string, owner IngressOwnership, state IngressState) (Ingress, error) {
	configPath, cfg, err := s.LoadOrDefaultConfig()
	if err != nil {
		return Ingress{}, err
	}
	idx := findAppByName(cfg, name)
	if idx < 0 {
		return Ingress{}, fmt.Errorf("ingress not found: %s", name)
	}
	current, err := ingressFromApp(cfg.Apps[idx])
	if err != nil {
		return Ingress{}, err
	}
	if current.Owner != owner {
		return Ingress{}, fmt.Errorf("ingress %q ownership conflict", name)
	}
	cfg.Apps[idx].Metadata[ingressMetadataState] = string(state)
	cfg.Apps[idx].Metadata[ingressMetadataRevision] = strconv.FormatInt(current.Revision+1, 10)
	if err := s.store.Save(configPath, cfg); err != nil {
		return Ingress{}, err
	}
	return ingressFromApp(cfg.Apps[idx])
}

func validateIngressSpec(spec IngressSpec) (IngressSpec, error) {
	name, err := normalizeAppNameInput(spec.Name)
	if err != nil {
		return IngressSpec{}, err
	}
	if err := validateAppPort(spec.LocalPort); err != nil {
		return IngressSpec{}, err
	}
	owner := IngressOwnership{System: strings.TrimSpace(spec.Owner.System), ScopeID: strings.TrimSpace(spec.Owner.ScopeID)}
	if owner.System == "" || owner.ScopeID == "" {
		return IngressSpec{}, fmt.Errorf("ingress owner system and scope id are required")
	}
	callbackPath, err := validateCallbackPath(spec.CallbackPath)
	if err != nil {
		return IngressSpec{}, err
	}
	for key := range spec.Metadata {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), ingressMetadataPrefix) {
			return IngressSpec{}, fmt.Errorf("ingress metadata key %q is reserved", key)
		}
	}
	spec.Name = name
	spec.DialHost = strings.TrimSpace(spec.DialHost)
	spec.Provider = strings.ToLower(strings.TrimSpace(spec.Provider))
	spec.PublicHost = strings.ToLower(strings.TrimSpace(spec.PublicHost))
	spec.CallbackPath = callbackPath
	spec.Owner = owner
	return spec, nil
}

func validateCallbackPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || path.Clean(value) != value {
		return "", fmt.Errorf("invalid ingress callback path %q", value)
	}
	return value, nil
}

func ingressFromApp(candidate config.App) (Ingress, error) {
	if candidate.Metadata[ingressMetadataVersionKey] != ingressMetadataVersion {
		return Ingress{}, fmt.Errorf("app is not a managed ingress")
	}
	revision, err := strconv.ParseInt(candidate.Metadata[ingressMetadataRevision], 10, 64)
	if err != nil || revision < 1 {
		return Ingress{}, fmt.Errorf("ingress revision is invalid")
	}
	callbackPath, err := validateCallbackPath(candidate.Metadata[ingressMetadataCallbackPath])
	if err != nil {
		return Ingress{}, err
	}
	publicHost := strings.TrimSpace(candidate.PublicEndpoint.Host)
	callbackURL := ""
	if publicHost != "" {
		callbackURL = (&url.URL{Scheme: "https", Host: publicHost, Path: callbackPath}).String()
	}
	metadata := copyConsumerMetadata(candidate.Metadata)
	return Ingress{Name: candidate.Name, LocalHost: candidate.LocalHost, LocalPort: candidate.LocalPort, DialHost: candidate.DialHost, Provider: candidate.PublicEndpoint.Provider, PublicHost: publicHost, CallbackPath: callbackPath, CallbackURL: callbackURL, EndpointID: candidate.PublicEndpoint.EndpointID, SessionID: candidate.PublicEndpoint.ActiveSessionID, Owner: IngressOwnership{System: candidate.Metadata[ingressMetadataOwnerSystem], ScopeID: candidate.Metadata[ingressMetadataOwnerScope]}, State: IngressState(candidate.Metadata[ingressMetadataState]), Revision: revision, Metadata: metadata}, nil
}

func checkIngressRef(current Ingress, ref IngressRef) error {
	owner := IngressOwnership{System: strings.TrimSpace(ref.Owner.System), ScopeID: strings.TrimSpace(ref.Owner.ScopeID)}
	if owner.System == "" || owner.ScopeID == "" || current.Owner != owner {
		return fmt.Errorf("ingress %q ownership conflict", current.Name)
	}
	if ref.ExpectedRevision != nil && current.Revision != *ref.ExpectedRevision {
		return fmt.Errorf("ingress %q revision conflict: current %d, expected %d", current.Name, current.Revision, *ref.ExpectedRevision)
	}
	return nil
}

func ingressSpecHash(spec IngressSpec) string {
	metadata := copyConsumerMetadata(spec.Metadata)
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	// Hashing map entries through a stable textual representation keeps the
	// persisted format language-neutral without exposing values as identifiers.
	sort.Strings(keys)
	builder := strings.Builder{}
	fmt.Fprintf(&builder, "%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s", spec.Name, spec.LocalPort, spec.DialHost, spec.Provider, spec.PublicHost, spec.CallbackPath, spec.Owner.System, spec.Owner.ScopeID)
	for _, key := range keys {
		fmt.Fprintf(&builder, "\x00%s=%s", key, metadata[key])
	}
	digest := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(digest[:])
}

func copyConsumerMetadata(input map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range input {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), ingressMetadataPrefix) {
			out[key] = value
		}
	}
	return out
}
