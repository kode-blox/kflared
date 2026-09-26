# KFlared Helm chart

This chart installs KFlared, a controller for public Gateway hostname bindings and private Cloudflare network routes, along with its CRDs, least-privilege RBAC, and metrics Service. Gateway API v1.6.1, Traefik 3.7.10+, and optional ExternalDNS remain prerequisites for public hostname bindings.

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

`apiTokenSecretRef.name` is optional and defaults to `externalSecrets.targetSecretName`, which keeps manual-Secret and External Secrets Operator installations on the same credential contract. `allowedDNSZones` may be empty when the provider serves only private routes; in that case it cannot publish hostnames through `CloudflareTunnelBinding`. `bindingNamespaceSelector: {}` intentionally permits bindings and private routes from every namespace; use a label selector in shared clusters.

Before uninstalling a release that manages a provider, delete or migrate all `CloudflareTunnelBinding` and `CloudflareTunnelPrivateRoute` resources that reference it. Provider and route finalizers need the controller to complete cleanup; Helm removes the controller during uninstall and cannot complete finalization afterward. Review each private route's `deletionPolicy` and Cloudflare route ownership before removal. A `Retain` policy intentionally leaves the remote private route and tunnel for administrator-managed cleanup.

The chart grants the manager access to namespaced `CloudflareTunnelPrivateRoute` objects and to the referenced Services and ReferenceGrants. Users can bind the built-in Kubernetes API Service using a private-route resource without exposing a public API hostname. Cloudflare One enrollment and WARP access policy, applying the updated release, and end-to-end validation are operator-managed rollout steps.

A private route addresses the Service IP as `/32`; Cloudflare's CIDR route does not limit traffic to the selected Service port. Apply Cloudflare One policy and cluster firewall/network policy to constrain client access to the required port (typically TCP 443 for the Kubernetes API). KFlared checks that the requested Service port exists, but that check does not enforce the port on routed traffic.

Cloudflare excludes RFC1918 destinations from WARP Split Tunnels by default. Operators must include the route in the intended clients' Split Tunnel configuration and apply Cloudflare Gateway policies that allow the intended identities and port, then block other private-network traffic. See Cloudflare's [CIDR routing guide](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/private-net/cloudflared/connect-cidr/). These Cloudflare settings and release rollout steps are performed by the cluster and Cloudflare administrators.

Use `--skip-crds` with Helm, or `spec.source.helm.skipCrds: true` with Argo CD, only when cluster administrators manage these CRDs separately. Use `--include-crds` when rendering the complete chart with `helm template`.

Set `serviceMonitor.enabled=true` only when the Prometheus Operator CRDs are already installed.
