package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/goliatone/switchboard-hub/internal/config"
	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

type operationRecorder struct {
	ingress.Recorder
	intent ingress.Progress
}

func (r *operationRecorder) Record(ctx context.Context, p ingress.Progress) error {
	p.Operation, p.Step, p.BaseHash, p.Desired = r.intent.Operation, r.intent.Step, r.intent.BaseHash, r.intent.Desired
	return r.Recorder.Record(ctx, p)
}
func cloneApp(app config.App) config.App {
	raw, _ := json.Marshal(app)
	var out config.App
	_ = json.Unmarshal(raw, &out)
	return out
}
func appHash(cfg *config.Config, idx int) string {
	var raw []byte
	if idx >= 0 {
		raw, _ = json.Marshal(cfg.Apps[idx])
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}
func newOperationRecorder(rec ingress.Recorder, action string, cfg *config.Config, idx int, desired config.App, secret []byte) (*operationRecorder, error) {
	if containsCredential(desired, secret) {
		return nil, errors.New("credential material in desired ingress configuration")
	}
	raw, err := json.Marshal(desired)
	if err != nil {
		return nil, err
	}
	step := action
	if action == "release" {
		step = "stop"
	}
	return &operationRecorder{Recorder: rec, intent: ingress.Progress{Operation: action, Step: step, BaseHash: appHash(cfg, idx), Desired: raw}}, nil
}

func stopVerifiedOwned(ctx context.Context, owned ingress.OwnedProvider, resource ingress.Resource, recovered bool) error {
	if resource.Session.ID == "" && (resource.Session.PID != 0 || resource.Session.Executable != "" || !resource.Session.StartedAt.IsZero() || len(resource.Session.Identity) > 0) {
		return errors.New("incomplete session identity; leave process untouched")
	}
	if resource.Session.ID != "" {
		if !validSessionIdentity(resource.Session) {
			return errors.New("session identity is incomplete; explicit recovery required")
		}
		verifier, ok := owned.(ingress.RecoveryProvider)
		if !ok && (recovered || resource.Session.PID > 0) {
			return ErrIngressSafetyUnavailable
		}
		if ok {
			if err := verifier.VerifyOwnedIngressSession(ctx, cloneResource(resource)); err != nil {
				return privateProviderError(ctx, "session identity verification; leave process untouched")
			}
		}
	}
	return owned.StopOwnedIngress(ctx, cloneResource(resource))
}

type recoveryOperation struct {
	path     string
	cfg      *config.Config
	idx      int
	desired  config.App
	progress ingress.Progress
	owned    ingress.OwnedProvider
	recorder *operationRecorder
	secret   []byte
}

func (s *Service) recoverOwnedMutation(ctx context.Context, rec ingress.Recorder, p ingress.Progress, action string, ref IngressRef, spec *IngressSpec) (Ingress, error) {
	op, done, result, err := s.prepareRecovery(rec, p, action, ref, spec)
	if err != nil || done {
		return result, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	provider, owned, err := s.ownedProvider(p.Resource.Provider)
	if err != nil {
		return Ingress{}, err
	}
	op.owned = owned
	op.secret, err = s.prepareOwnedProvider(callCtx, op.cfg, provider, owned, p.Resource)
	defer clear(op.secret)
	if err != nil {
		return Ingress{}, err
	}
	if containsCredential(p, op.secret) {
		return Ingress{}, errors.New("credential material in recovery state")
	}
	if err := op.reconcile(callCtx); err != nil {
		return Ingress{}, err
	}
	return s.persistRecovery(ctx, op)
}

func (s *Service) prepareRecovery(rec ingress.Recorder, p ingress.Progress, action string, ref IngressRef, spec *IngressSpec) (recoveryOperation, bool, Ingress, error) {
	var op recoveryOperation
	if err := validateRecoveryIntent(rec, p, action, ref, spec, &op.desired); err != nil {
		return op, false, Ingress{}, err
	}
	var err error
	op.path, op.cfg, err = s.LoadOrDefaultConfig()
	if err != nil {
		return op, false, Ingress{}, err
	}
	if op.cfg == nil {
		return op, false, Ingress{}, errors.New("recovery store returned no config")
	}
	op.idx = findAppByName(op.cfg, ref.Name)
	op.progress = p
	op.recorder = &operationRecorder{Recorder: rec, intent: p}
	committed, current := recoveryCommitted(op, rec.OperationID())
	if committed && action != "start" {
		return op, true, current, nil
	}
	if action == "release" && op.idx < 0 && p.Step == "release" && (p.Phase == "confirmed" || p.Phase == "persisting") {
		return op, true, Ingress{}, nil
	}
	if !committed && appHash(op.cfg, op.idx) != p.BaseHash {
		return op, false, Ingress{}, errors.New("ingress changed since interrupted mutation; preserve progress and resolve conflict")
	}
	if committed && action == "start" {
		op.desired = cloneApp(op.cfg.Apps[op.idx])
	}
	return op, false, Ingress{}, nil
}

func ingressRevision(candidate config.App) int64 {
	revision, _ := strconv.ParseInt(candidate.Metadata[ingressMetadataRevision], 10, 64)
	return revision
}

func (op *recoveryOperation) reconcile(ctx context.Context) error {
	p := op.progress
	applied := p.Phase == "confirmed" || p.Phase == "persisting"
	if !applied {
		inspector, ok := op.owned.(ingress.RecoveryProvider)
		if !ok {
			return fmt.Errorf("%w: provider cannot reconcile the recorded attempt", ErrIngressSafetyUnavailable)
		}
		outcome, err := inspector.ReconcileOwnedIngress(ctx, p)
		if err != nil || containsCredential(outcome, op.secret) {
			return privateProviderError(ctx, "reconciliation")
		}
		switch outcome.Outcome {
		case "applied":
			if err := validateRecoveryResult(p, outcome.Resource, op.secret); err != nil {
				return err
			}
			p.Resource = outcome.Resource
			applied = true
		case "not-applied":
			// Use only durable intent; an absence result carries no adoption authority.
		default:
			return errors.New("provider outcome remains uncertain; inspect recorded operation before retry")
		}
	}
	if err := op.finishRecoveryStep(ctx, &p, applied); err != nil {
		return err
	}
	p.Phase = "confirmed"
	if err := op.recorder.Record(ctx, p); err != nil {
		return err
	}
	op.progress = p
	return nil
}

func validateRecoveryResult(p ingress.Progress, r ingress.Resource, secret []byte) error {
	before := p.Resource
	// Cleanup may clear a session, but may not change a surviving identity.
	if p.Step == "stop" || p.Step == "release" {
		before.Session = r.Session
		if r.Session.ID != "" {
			return errors.New("cleanup reconciliation still reports a session")
		}
	}
	if err := validateResourceResult(before, r, p.Step == "start", secret); err != nil {
		return err
	}
	if p.Step == "start" && !validSessionIdentity(r.Session) {
		return errors.New("recovered start has no verifiable session identity")
	}
	return nil
}

func (op *recoveryOperation) finishRecoveryStep(ctx context.Context, p *ingress.Progress, applied bool) error {
	switch p.Step {
	case "ensure":
		if !applied {
			if err := op.recorder.Record(ctx, *p); err != nil {
				return err
			}
			updated, err := op.owned.EnsureOwnedIngress(ctx, cloneResource(p.Resource))
			if err != nil {
				return privateProviderError(ctx, "reconciled ensure")
			}
			if err := validateResourceResult(p.Resource, updated, false, op.secret); err != nil {
				return err
			}
			p.Resource = updated
		}
	case "start":
		// Never replay Start, even if authoritative inspection proves it did not run.
		// A recovered live session is stopped before settling the saved configuration.
		if applied {
			op.recorder.intent.Step = "stop"
			p.Step = "stop"
			p.Phase = "pending"
			if err := op.recorder.Record(ctx, *p); err != nil {
				return err
			}
			if err := stopVerifiedOwned(ctx, op.owned, p.Resource, true); err != nil {
				return err
			}
		}
		p.Resource.Session = ingress.SessionIdentity{}
	case "stop":
		if !applied {
			if err := stopVerifiedOwned(ctx, op.owned, p.Resource, true); err != nil {
				return err
			}
		}
		p.Resource.Session = ingress.SessionIdentity{}
		if p.Operation == "release" {
			op.recorder.intent.Step = "release"
			p.Step = "release"
			p.Phase = "pending"
			if err := op.recorder.Record(ctx, *p); err != nil {
				return err
			}
			if err := op.owned.ReleaseOwnedIngress(ctx, cloneResource(p.Resource)); err != nil {
				return privateProviderError(ctx, "reconciled release")
			}
		}
	case "release":
		if !applied {
			if err := op.owned.ReleaseOwnedIngress(ctx, cloneResource(p.Resource)); err != nil {
				return privateProviderError(ctx, "reconciled release")
			}
		}
	default:
		return errors.New("unknown recovery step")
	}
	return nil
}

func (s *Service) persistRecovery(ctx context.Context, op recoveryOperation) (Ingress, error) {
	p := op.progress
	var result Ingress
	if p.Operation == "release" {
		if op.idx >= 0 {
			op.cfg.Apps = append(op.cfg.Apps[:op.idx], op.cfg.Apps[op.idx+1:]...)
		}
	} else {
		state := IngressStateStopped
		if p.Operation == "ensure" {
			state = IngressStateConfigured
		}
		if err := setIsolatedResource(&op.desired, p.Resource, state, ingressRevision(op.desired)); err != nil {
			return result, err
		}
		if op.idx < 0 {
			op.cfg.Apps = append(op.cfg.Apps, op.desired)
		} else {
			op.cfg.Apps[op.idx] = op.desired
		}
		var err error
		result, err = ingressFromApp(op.desired)
		if err != nil {
			return result, err
		}
	}
	p.Phase = "persisting"
	if err := op.recorder.Record(ctx, p); err != nil {
		return result, err
	}
	return result, s.store.Save(op.path, op.cfg)
}

func validateRecoveryIntent(rec ingress.Recorder, p ingress.Progress, action string, ref IngressRef, spec *IngressSpec, desired *config.App) error {
	if p.Operation != action || p.Resource.OperationID != rec.OperationID() || p.Resource.Name != ref.Name || p.Resource.Owner != (ingress.Owner{System: ref.Owner.System, ScopeID: ref.Owner.ScopeID}) || p.Resource.Generation == "" || p.BaseHash == "" {
		return fmt.Errorf("%w: interrupted mutation requires reconciliation with matching intent and ownership", ErrIngressSafetyUnavailable)
	}
	validStep := p.Step == action || ((action == "start" || action == "release") && p.Step == "stop")
	validPhase := p.Phase == "pending" || p.Phase == "confirmed" || p.Phase == "uncertain" || p.Phase == "persisting"
	if !validStep || !validPhase {
		return errors.New("invalid recovery step or phase")
	}
	if err := json.Unmarshal(p.Desired, desired); err != nil {
		return errors.New("invalid recovery intent")
	}
	return validateDesiredIntent(*desired, ref, spec)
}
func validateDesiredIntent(desired config.App, ref IngressRef, spec *IngressSpec) error {
	if desired.Name != ref.Name || desired.Metadata == nil || desired.Metadata[ingressMetadataOwnerSystem] != ref.Owner.System || desired.Metadata[ingressMetadataOwnerScope] != ref.Owner.ScopeID || ingressRevision(desired) < 1 {
		return errors.New("recovery intent has invalid ownership or revision")
	}
	if spec != nil && desired.Metadata[ingressMetadataSpecHash] != isolatedSpecHash(*spec) {
		return errors.New("retry must match the interrupted ensure intent")
	}
	return nil
}
func recoveryCommitted(op recoveryOperation, id string) (bool, Ingress) {
	if op.idx < 0 || op.progress.Phase != "persisting" {
		return false, Ingress{}
	}
	candidate := op.cfg.Apps[op.idx]
	current, err := ingressFromApp(candidate)
	if err != nil {
		return false, Ingress{}
	}
	resource, err := isolatedResource(candidate)
	p := op.progress
	if err != nil || resource.Owner != p.Resource.Owner || resource.Generation != p.Resource.Generation || resource.OperationID != id || current.Revision != ingressRevision(op.desired) {
		return false, Ingress{}
	}
	expected := cloneApp(op.desired)
	state := IngressStateStopped
	if p.Operation == "ensure" {
		state = IngressStateConfigured
	}
	if p.Operation == "start" && p.Step == "start" && p.Resource.Session.ID != "" {
		state = IngressStateRunning
	}
	if err := setIsolatedResource(&expected, p.Resource, state, current.Revision); err != nil {
		return false, Ingress{}
	}
	raw, _ := json.Marshal(expected)
	actual, _ := json.Marshal(candidate)
	return string(raw) == string(actual), current
}
