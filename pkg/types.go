package fluxprovider

import helmv2 "github.com/fluxcd/helm-controller/api/v2"

// FluxHelmRelease is the result of converting a Helm release secret.
type FluxHelmRelease struct {
	// HelmRelease is the upstream Flux v2 HelmRelease custom resource.
	HelmRelease *helmv2.HelmRelease
	// RawYAML is the serialized HelmRelease prefixed with the
	// syngit resource-finder comment header.
	RawYAML string
}

const HelmReleaseAnnotation = "flux.syngit.io/helm-release"
