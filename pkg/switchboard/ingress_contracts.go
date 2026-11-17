package switchboard

import "github.com/goliatone/switchboard-hub/pkg/ingress"

type EndpointBinding = ingress.Binding
type IngressOwner = ingress.Owner
type IngressResource = ingress.Resource
type IngressSessionIdentity = ingress.SessionIdentity
type IngressRuntime = ingress.Runtime
type OwnedIngressStatus = ingress.Status
type OwnedIngressProvider = ingress.OwnedProvider
type PrivateIngressInitializer = ingress.PrivateInitializer
type IngressMutationScope = ingress.MutationScope
type IngressProgress = ingress.Progress
type IngressRecorder = ingress.Recorder
type CoordinatedIngressStore = ingress.MutationCoordinator
type IngressHelperResolver = ingress.HelperResolver

type IngressRecoveryProvider = ingress.RecoveryProvider
type IngressRecoveryResult = ingress.RecoveryResult
