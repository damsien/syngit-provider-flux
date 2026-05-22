# syngit-provider-flux

An addon to use Flux functionalities into Syngit.

## Feature

Convert a Helm release `v1.Secret` object into a Flux `HelmRelease` custom
resource that carries the user-supplied values along with the chart reference
needed by the Flux helm-controller.

This provider builds on
[syngit-provider-helm](https://github.com/syngit-org/syngit-provider-helm) and
reuses its `ExtractValues` function to decode the Helm release payload.

