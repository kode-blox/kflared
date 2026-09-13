# KFlared Helm chart

This chart installs KFlared, a Gateway controller for Cloudflare Tunnel, along with its CRDs, least-privilege RBAC, and metrics Service. Gateway API v1.6.1, Traefik 3.7.10+, and optional ExternalDNS remain cluster prerequisites.

The chart combines the current Helm starter's conventional layout and helpers with the controller resource inventory generated from Kubebuilder/Kustomize. Kustomize remains the canonical source for generated CRDs and RBAC permissions; chart tests detect drift between the two distributions.

KFlared CRDs are plain YAML in Helm's special `crds/` directory. Helm installs them before chart templates and deliberately does not upgrade or delete them. The CRDs carry `helm.sh/resource-policy: keep`, which Argo CD treats as `Delete=false` when it owns the application lifecycle.

## Install

```sh
helm upgrade --install kflared ./charts \
  --namespace kflared-system \
  --create-namespace
```

The chart supports another release namespace. The controller discovers it through the Pod downward API and keeps Cloudflare credentials and generated connector resources in that namespace.

Create the API token Secret after installation and before a `CloudflareProvider`:

```sh
kubectl -n kflared-system create secret generic cloudflare-api-token \
  --from-literal=api-token='<CLOUDFLARE_API_TOKEN>'
```

Use `--skip-crds` with Helm, or `spec.source.helm.skipCrds: true` with Argo CD, only when cluster administrators manage these CRDs separately. Use `--include-crds` when rendering the complete chart with `helm template`.

Set `serviceMonitor.enabled=true` only when the Prometheus Operator CRDs are already installed.
