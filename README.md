# KFlared

KFlared manages Cloudflare Tunnels for two paths: it publishes selected Kubernetes Gateway API hostnames through controller-owned tunnels, and it can route a Service ClusterIP privately to enrolled Cloudflare One clients. Workloads and the Traefik data plane remain on private Kubernetes networking.

The first release is deliberately an integration controller, not a Gateway API implementation:

```text
Cloudflare edge
  -> remotely managed Cloudflare Tunnel
  -> official cloudflared connector Deployment
  -> internal Traefik Service
  -> Traefik Gateway and HTTPRoutes
  -> workload Services
```

See the [KFlared documentation](https://kflared.kodeblox.com), [architecture](docs/content/docs/architecture.md), and [security model](docs/content/docs/security.md) before operating the controller.

## MVP scope

- Traefik 3.7.10+ using GatewayClass controller `traefik.io/gateway-controller`
- Gateway API v1.6.1 `Gateway` and `HTTPRoute`
- one `CloudflareTunnelBinding`, one existing Gateway, and one remotely managed tunnel
- one named HTTP listener and concrete, non-wildcard HTTPRoute hostnames
- binding, Gateway, and HTTPRoutes in the application namespace; an origin Service may be cross-namespace with a matching ReferenceGrant
- official `cloudflare/cloudflared:2026.8.3`, one or more replicas (one by default)
- per-binding ExternalDNS `DNSEndpoint` automation, enabled by default, with an explicit opt-out for externally managed DNS targets

GRPCRoute, wildcard hostnames, HTTPS origins, direct Service backend routing, shared/imported tunnels, externally managed connectors, and native GatewayClass ownership are deferred.

`CloudflareTunnelPrivateRoute` is a separate API for private CIDR routing. It targets a referenced Service's current ClusterIP as a single `/32` route and does not configure public hostnames or HTTPRoutes. See the [private route configuration](docs/content/docs/configuration.md#cloudflaretunnelprivateroute) and [architecture](docs/content/docs/architecture.md#private-network-route). Cloudflare One client enrollment/access policy and the user's Kubernetes credentials remain external prerequisites.

## Get started

Follow [Installation](https://kflared.kodeblox.com/installation) to select a release, install the controller, supply account credentials, and verify the traffic path. Then use [Configuration](https://kflared.kodeblox.com/configuration) for providers, public hostname bindings, and private Service routes. The examples in [config/samples](config/samples) must be adapted to the actual account, Gateway, Service, and namespace authorization.

Public hostname bindings need Gateway API, Traefik, and an internal origin Service; private routes need an enrolled Cloudflare One client and explicit network access policy. ExternalDNS and External Secrets Operator are optional integrations. The controller's Cloudflare token does not need DNS edit access.

Read [Architecture](https://kflared.kodeblox.com/architecture) and [Security](https://kflared.kodeblox.com/security) before authorizing tenant namespaces or exposing private Services. Root [SECURITY.md](SECURITY.md) defines supported versions and private vulnerability reporting.

For an existing deployment, [Operations](https://kflared.kodeblox.com/operations) covers status, DNS modes, grants, and cleanup. For contribution and delivery, read [Language and foundations](https://kflared.kodeblox.com/language-and-foundations), [Testing](https://kflared.kodeblox.com/testing), and [Release process](https://kflared.kodeblox.com/release-process).

## Development

The module targets Go 1.27.0, controller-runtime v0.25.0, Gateway API v1.6.1, and `cloudflare-go/v7` v7.8.0. Use the Go version declared in `go.mod` and PowerShell 7 or newer for the repository-local Task wrapper. Task is pinned in `tools/task` and needs no global installation:

```powershell
./task.ps1 --list
./task.ps1 test
./task.ps1 build
```

Generated code and manifests remain Kubebuilder-controlled. Use the pinned controller-gen version from `Taskfile.yaml` and verify the resulting diff. See [testing](docs/content/docs/testing.md) for automated coverage and manual external-service gates. The [docs README](docs/README.md) describes local website authoring and validation.

The supported Helm chart is rooted at [`charts`](charts). It combines the current Helm starter structure with the controller resources derived from Kubebuilder's Kustomize output. Its plain CRDs live in Helm's special `charts/crds/` directory; pass `--include-crds` when rendering the complete chart.

## License

Apache License 2.0. See [LICENSE](LICENSE).
