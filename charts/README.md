# KFlared Helm chart

This chart installs KFlared, a Gateway controller for Cloudflare Tunnel, along with its CRDs, least-privilege RBAC, and metrics Service. Gateway API v1.6.1, Traefik 3.7.10+, and optional ExternalDNS remain cluster prerequisites.

The chart combines the current Helm starter's conventional layout and helpers with the controller resource inventory generated from Kubebuilder/Kustomize. Kustomize remains the canonical source for generated CRDs and RBAC permissions; chart tests detect drift between the two distributions.

KFlared CRDs are plain YAML in Helm's special `crds/` directory. Helm installs them before chart templates and deliberately does not upgrade or delete them. The CRDs carry `helm.sh/resource-policy: keep`, which Argo CD treats as `Delete=false` when it owns the application lifecycle.

## Install

```sh
helm upgrade --install kflared ./charts \
  --namespace kflared \
  --create-namespace
```

`controllerClass` defaults to `kflared`. Use that value in `spec.controller` on each provider and binding owned by the installation. Override it with a stable, distinct value for each additional KFlared installation in the same cluster.

> **Warning:** Never run two KFlared installations with the same controller class. A second installation must set a distinct value, for example `--set controllerClass=kflared-secondary`, and its providers and bindings must use that same value.

When upgrading resources created before controller classes existed, do not rely on `helm upgrade` to update the CRDs. Follow the ordered CRD sync, resource backfill, verification, and controller rollout procedure in the [main installation documentation](../docs/content/docs/installation.md#upgrade-from-a-release-without-controller-classes).

The chart supports another release namespace. The controller discovers it through the Pod downward API and keeps Cloudflare credentials and generated connector resources in that namespace.

Set `namespace.create=true` when a GitOps or rendered-manifest workflow should create `.Release.Namespace` from the chart. Optional `namespace.labels` and `namespace.annotations` are applied to it.

For a direct Helm install into a namespace that does not exist yet, continue to pass `--create-namespace`. Helm must create its release namespace before it can apply chart templates, so a chart-managed Namespace cannot bootstrap that Helm operation by itself.

`kubernetesComponent` sets the descriptive `app.kubernetes.io/component` metadata label. It is deliberately excluded from workload selectors, which use only the stable release name and instance labels.

By default, create the API token Secret after installation and before a `ClusterCloudflareProvider`:

```sh
kubectl -n kflared create secret generic cloudflare-api-token \
  --from-literal=api-token='<CLOUDFLARE_API_TOKEN>'
```

The chart can instead create an `ExternalSecret` when External Secrets Operator and a `ClusterSecretStore` are already installed. The target Secret remains in the release namespace and defaults to `cloudflare-api-token`:

```yaml
externalSecrets:
  enabled: true
  secretStore: production
  refreshInterval: 1h
  targetSecretName: cloudflare-api-token
  secrets:
    - secretKey: api-token
      remoteRef:
        key: kflared/cloudflare-api-token
```

The remote key is provider-specific. The resulting `api-token` key must match `ClusterCloudflareProvider.spec.apiTokenSecretRef.key`.

## Optional ClusterCloudflareProvider

The chart can create one cluster-scoped `ClusterCloudflareProvider` for this controller release. It is disabled by default because the Cloudflare account boundary and namespace access policy are cluster-specific. When enabled, `spec.controller` is always derived from `controllerClass`, and the provider name defaults to the chart fullname. Set `name` to a stable, unique value when a release name is not the desired cluster-scoped identity.

```yaml
clusterCloudflareProvider:
  enabled: true
  name: cloudflare-production
  accountID: 0123456789abcdef0123456789abcdef
  apiTokenSecretRef:
    key: api-token
  allowedDNSZones:
    - example.com
  bindingNamespaceSelector:
    matchLabels:
      kflared.kodeblox.com/cloudflare-provider: cloudflare-production
```

`apiTokenSecretRef.name` is optional and defaults to `externalSecrets.targetSecretName`, which keeps manual-Secret and External Secrets Operator installations on the same credential contract. `allowedDNSZones` must contain at least one zone. `bindingNamespaceSelector: {}` intentionally permits bindings from every namespace; use a label selector in shared clusters.

Before uninstalling a release that manages a provider, delete or migrate all `CloudflareTunnelBinding` resources that reference it. The provider finalizer correctly blocks deletion while bindings remain, but Helm also removes the controller during uninstall and cannot complete that finalization afterward.

Use `--skip-crds` with Helm, or `spec.source.helm.skipCrds: true` with Argo CD, only when cluster administrators manage these CRDs separately. Use `--include-crds` when rendering the complete chart with `helm template`.

Set `serviceMonitor.enabled=true` only when the Prometheus Operator CRDs are already installed.
