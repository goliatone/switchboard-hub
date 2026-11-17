package switchboard

import "github.com/goliatone/switchboard-hub/internal/tunnel"

type coordinatedStoreAdapter struct {
	internalStoreAdapter
	CoordinatedIngressStore
}

type ownedProviderAdapter struct {
	publicProviderAdapter
	OwnedIngressProvider
}
type privateProviderAdapter struct {
	publicProviderAdapter
	PrivateIngressInitializer
}
type ownedPrivateProviderAdapter struct {
	publicProviderAdapter
	OwnedIngressProvider
	PrivateIngressInitializer
}

// Preserve optional method sets rather than advertising a capability that the
// injected implementation does not have.
func adaptOwnedProvider(provider Provider) tunnel.Provider {
	base := publicProviderAdapter{provider: provider}
	owned, hasOwned := provider.(OwnedIngressProvider)
	private, hasPrivate := provider.(PrivateIngressInitializer)
	recovery, hasRecovery := provider.(IngressRecoveryProvider)
	if hasOwned && hasRecovery {
		if hasPrivate {
			return recoveringPrivateAdapter{ownedPrivateProviderAdapter{base, owned, private}, recovery}
		}
		return recoveringAdapter{ownedProviderAdapter{base, owned}, recovery}
	}
	switch {
	case hasOwned && hasPrivate:
		return ownedPrivateProviderAdapter{base, owned, private}
	case hasOwned:
		return ownedProviderAdapter{base, owned}
	case hasPrivate:
		return privateProviderAdapter{base, private}
	default:
		return base
	}
}

func cloneBinding(binding *EndpointBinding) *EndpointBinding {
	if binding == nil {
		return nil
	}
	out := *binding
	return &out
}

type recoveringAdapter struct {
	ownedProviderAdapter
	IngressRecoveryProvider
}
type recoveringPrivateAdapter struct {
	ownedPrivateProviderAdapter
	IngressRecoveryProvider
}
