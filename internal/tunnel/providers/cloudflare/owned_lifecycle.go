package cloudflare

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/goliatone/switchboard-hub/pkg/ingress"
)

func (p *Provider) StartOwnedIngress(ctx context.Context, r ingress.Resource) (ingress.Resource, error) {
	unlock, err := p.lockOwned(ctx)
	if err != nil {
		return r, err
	}
	defer unlock()
	s, found, err := p.load(r)
	if err != nil {
		return r, err
	}
	if !found {
		return r, errors.New("cloudflare ownership receipt missing")
	}
	s.OperationID = r.OperationID
	r = s
	if err = p.checkReady(ctx, r); err != nil {
		return r, err
	}
	if err = p.startConnector(ctx, &r); err != nil {
		return r, err
	}
	return r, p.save(r)
}
func (p *Provider) StopOwnedIngress(ctx context.Context, r ingress.Resource) error {
	unlock, err := p.lockOwned(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	s, found, err := p.load(r)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("cloudflare ownership receipt missing")
	}
	if r.Session.ID != "" && s.Session.ID != "" && r.Session.ID != s.Session.ID {
		return errors.New("connector session changed")
	}
	s.OperationID = r.OperationID
	if err = p.stopConnector(ctx, &s); err != nil {
		return err
	}
	return p.save(s)
}
func (p *Provider) VerifyOwnedIngressSession(ctx context.Context, r ingress.Resource) error {
	s, found, err := p.load(r)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("cloudflare ownership receipt missing")
	}
	if s.Session.ID == "" {
		return nil
	}
	if r.Session.ID != s.Session.ID {
		return errors.New("connector session changed")
	}
	spec, err := p.connectorSpec(s)
	if err != nil {
		return err
	}
	_, err = supervisorCall(ctx, spec, "status")
	return err
}
func (p *Provider) StatusOwnedIngress(ctx context.Context, r ingress.Resource) (ingress.Status, error) {
	s, found, err := p.load(r)
	if err != nil {
		return ingress.Status{}, err
	}
	if !found {
		return ingress.Status{}, errors.New("cloudflare ownership receipt missing")
	}
	if s.Session.ID == "" {
		return ingress.Status{}, nil
	}
	if err = p.checkReady(ctx, s); err != nil {
		return ingress.Status{}, err
	}
	spec, err := p.connectorSpec(s)
	if err != nil {
		return ingress.Status{}, err
	}
	running, err := supervisorCall(ctx, spec, "status")
	if err != nil || !running {
		return ingress.Status{}, err
	}
	var connections []json.RawMessage
	if err = p.owned.api.call(ctx, "GET", p.tunnelPath(s.Binding.EndpointID)+"/connections", nil, &connections); err != nil {
		return ingress.Status{}, err
	}
	return ingress.Status{Ready: len(connections) > 0, SessionID: s.Session.ID}, nil
}
func (p *Provider) ReconcileOwnedIngress(ctx context.Context, progress ingress.Progress) (ingress.RecoveryResult, error) {
	unlock, err := p.lockOwned(ctx)
	if err != nil {
		return ingress.RecoveryResult{}, err
	}
	defer unlock()
	r, found, err := p.load(progress.Resource)
	if err != nil {
		return ingress.RecoveryResult{}, err
	}
	if !found {
		return ingress.RecoveryResult{Outcome: "not-applied"}, nil
	}
	result := ingress.RecoveryResult{Outcome: "unknown", Resource: r}
	switch progress.Step {
	case "ensure":
		if r.ProviderState["ready"] == "true" {
			if err = p.checkReady(ctx, r); err != nil {
				return result, err
			}
			result.Outcome = "applied"
			return result, nil
		}
		// Resume the provider's durable substeps. Ensure never retries an uncertain
		// creation blindly; its recorded exclusive-writer provenance is mandatory.
		result.Outcome = "not-applied"
	case "start":
		if r.OperationID != progress.Resource.OperationID {
			return result, nil
		}
		if r.Session.ID == "" {
			result.Outcome = "not-applied"
			return result, nil
		}
		spec, e := p.connectorSpec(r)
		if e != nil {
			return result, e
		}
		_, e = supervisorCall(ctx, spec, "status")
		if e != nil {
			return result, e
		}
		// Even an exited session is returned as applied so recovery runs Stop and
		// clears the receipt, instead of accidentally permitting automatic restart.
		result.Outcome = "applied"
	case "stop":
		if r.Session.ID == "" {
			result.Outcome = "applied"
			result.Resource.Session = ingress.SessionIdentity{}
		} else {
			result.Outcome = "not-applied"
		}
	case "release":
		if r.ProviderState["released"] == "true" {
			result.Outcome = "applied"
			result.Resource.Session = ingress.SessionIdentity{}
		} else {
			result.Outcome = "not-applied"
		}
	}
	return result, nil
}
