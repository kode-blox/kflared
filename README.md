# KFlared

KFlared is a Kode Blox Gateway controller for Cloudflare Tunnel. It publishes selected Kubernetes Gateway API hostnames through controller-owned tunnels while workloads and the Traefik data plane remain on private Kubernetes networking.

The first release is deliberately an integration controller, not a Gateway API implementation:

```text
Cloudflare edge
  -> remotely managed Cloudflare Tunnel
  -> official cloudflared connector Deployment
  -> internal Traefik Service
  -> Traefik Gateway and HTTPRoutes
  -> workload Services
```

See the [KFlared documentation](https://kflared.kodeblox.com), [architecture](docs/content/docs/architecture.md), and [security model](SECURITY.md) before operating the controller.

## MVP scope

- Traefik 3.7.10+ using GatewayClass controller `traefik.io/gateway-controller`
- Gateway API v1.6.1 `Gateway` and `HTTPRoute`
- one `CloudflareTunnelBinding`, one existing Gateway, and one remotely managed tunnel
- one named HTTP listener and concrete, non-wildcard HTTPRoute hostnames
- same-namespace binding, Gateway, HTTPRoute, and Traefik Service
- official `cloudflare/cloudflared:2026.8.3`, two or more replicas
- optional ExternalDNS `DNSEndpoint` automation, with exact manual CNAMEs in status otherwise

GRPCRoute, wildcard hostnames, HTTPS origins, direct Service backend routing, shared/imported tunnels, externally managed connectors, and native GatewayClass ownership are deferred.

## Prerequisites

- Kubernetes 1.35-1.37
- Gateway API v1.6.1 CRDs
- Traefik 3.7.10+ with Kubernetes Gateway provider enabled
- an internal, non-headless Traefik Service
- a Cloudflare account API token with only Cloudflare Tunnel/Connector write access required for the selected account
- optional ExternalDNS and its `externaldns.k8s.io/v1alpha1` DNSEndpoint CRD
- optional External Secrets Operator and a `ClusterSecretStore` when using the chart's ExternalSecret integration

The token does not need DNS edit permission. Put it only in `kflared-system`; the controller has no cluster-wide Secret permission:

```sh
kubectl -n kflared-system create secret generic cloudflare-api-token \
  --from-literal=api-token='<CLOUDFLARE_API_TOKEN>'
```

Helm users can instead opt in to the chart's `externalSecrets` values. The chart then creates an `ExternalSecret` targeting `cloudflare-api-token`; External Secrets Operator and the referenced `ClusterSecretStore` must already exist.

## Install and configure

Kustomize is the canonical manifest source. Install prerequisites first, then build and deploy an image:

Project automation requires Go and PowerShell 7 or newer. Task is pinned in the isolated `tools/task` module and does not need to be installed globally. Run tasks through the repository-local wrapper, for example `./task.ps1 --list` (PowerShell: `.\\task.ps1 --list`).

```powershell
./task.ps1 docker-build docker-push IMG=<registry>/kflared:<tag>
./task.ps1 install
./task.ps1 deploy IMG=<registry>/kflared:<tag>
```

Create a provider, label an allowed tenant namespace, and create a binding. Adapt the examples under [`config/samples`](config/samples) to the actual account ID, DNS zones, Gateway, listener, and Traefik Service.

```sh
kubectl apply -f config/samples/kflared_v1alpha1_clustercloudflareprovider.yaml
kubectl label namespace my-app kflared.kodeblox.com/cloudflare-provider=default
kubectl -n my-app apply -f config/samples/kflared_v1alpha1_cloudflaretunnelbinding.yaml
```

Inspect conditions and DNS requirements:

```sh
kubectl get clustercloudflareproviders
kubectl -n my-app get cloudflaretunnelbindings -o yaml
```

When the DNSEndpoint CRD is unavailable, `status.dnsRecords` is authoritative and `DNSAutomationReady=False` reports `ManualConfigurationRequired`. The MVP intentionally cannot acknowledge or verify manually managed DNS, so `Ready` remains false.

## Development

The module targets Go 1.27.0, controller-runtime v0.24.1, Gateway API v1.6.1, and `cloudflare-go/v7` v7.8.0. On this workstation, invoke the existing versioned executable and do not alter the default Go installation:

```sh
go1.27.0 test ./...
go1.27.0 build ./cmd
```

Generated code and manifests remain Kubebuilder-controlled. Use the pinned controller-gen version from `Taskfile.yaml` and verify the resulting diff. See [testing](docs/content/docs/testing.md).

The supported Helm chart is rooted at [`charts`](charts). It combines the current Helm starter structure with the controller resources derived from Kubebuilder's Kustomize output. Its plain CRDs live in Helm's special `charts/crds/` directory; pass `--include-crds` when rendering the complete chart.

## License

Apache License 2.0. See [LICENSE](LICENSE).
