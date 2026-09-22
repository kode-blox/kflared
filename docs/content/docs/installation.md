---
title: Installation
description: Install KFlared with Helm or Kustomize and prepare its cluster prerequisites.
---

KFlared publishes hostnames from a Traefik-managed Kubernetes Gateway through a controller-owned, remotely managed Cloudflare Tunnel. Install its prerequisites before installing the controller.

## Prerequisites

- Kubernetes 1.35 through 1.37.
- Gateway API v1.6.1 CRDs.
- Traefik 3.7.10 or newer with the Kubernetes Gateway provider enabled.
- An internal, non-headless Traefik Service.
- A Cloudflare account API token with Tunnel and Connector write access for the selected account.
- Optional: ExternalDNS and its `externaldns.k8s.io/v1alpha1` `DNSEndpoint` CRD.
- Optional: External Secrets Operator and a `ClusterSecretStore` when the chart should reconcile the API token Secret.

The Cloudflare token does not need DNS edit permission. ExternalDNS performs DNS changes when its CRD is installed.

## Install with Helm

The supported chart is rooted at `charts/`:

```sh
helm upgrade --install kflared ./charts \
  --namespace kflared \
  --create-namespace
```

`controllerClass` defaults to `kflared`. Use that value as `spec.controller` on every provider and binding assigned to the installation. Override it with a stable, distinct value for each additional KFlared installation in the same cluster.

> **Warning:** Never run two KFlared installations with the same controller class. They would select the same resources even when installed in different namespaces. A second installation must set a distinct value, for example `--set controllerClass=kflared-secondary`, and its providers and bindings must use that same value.

KFlared CRDs live in Helm's special `crds/` directory. Helm installs them before templates and deliberately does not upgrade or delete them. Use `--skip-crds` only when a cluster administrator manages the CRDs separately.

### Upgrade from a release without controller classes

Do not roll out the new manager before the stored resources have a class: it will deliberately ignore them. Keep the old manager running and perform these phases in order:

1. Apply both new CRDs directly from `config/crd/bases/` (or synchronize an equivalent CRD-only GitOps source). A normal `helm upgrade` does not update files from `charts/crds/`.
2. Add the chosen class to every stored `ClusterCloudflareProvider` and `CloudflareTunnelBinding`, either by synchronizing their updated GitOps manifests or by patching each object explicitly:

   ```sh
   kubectl patch clustercloudflareprovider <provider> \
     --type=merge -p '{"spec":{"controller":"kflared"}}'
   kubectl patch cloudflaretunnelbinding -n <namespace> <binding> \
     --type=merge -p '{"spec":{"controller":"kflared"}}'
   ```

3. Verify that every resource assigned to this installation reports the intended value:

   ```sh
   kubectl get clustercloudflareproviders -o custom-columns=NAME:.metadata.name,CONTROLLER:.spec.controller
   kubectl get cloudflaretunnelbindings -A -o custom-columns=NAMESPACE:.metadata.namespace,NAME:.metadata.name,CONTROLLER:.spec.controller
   ```

4. Upgrade the chart. The default class is `kflared`; if the resources were backfilled with another class, pass the matching value with `--set controllerClass=<class>`.

The initial backfill is allowed because the old field value is absent. After it is set, the CRD rejects class changes; moving a resource to another class requires recreation. In GitOps, make the CRD synchronization, custom-resource backfill, and controller rollout separate ordered syncs or waves so pruning by the old schema and early manager startup cannot race the migration.

Set `namespace.create=true` when a GitOps or rendered-manifest workflow should create the release namespace from the chart. Optional `namespace.labels` and `namespace.annotations` customize it. Direct Helm installs still need `--create-namespace` when the target does not exist because Helm initializes the release namespace before applying chart templates.

The chart intentionally combines Helm's conventional values and template structure with Kubebuilder's authoritative controller resource inventory. It does not include generic application templates such as an Ingress, HTTPRoute, HPA, or test Pod. Generated CRD schemas and RBAC remain sourced from Kustomize, with tests detecting distribution drift.

To inspect the complete rendered chart, include the otherwise omitted CRDs:

```sh
helm template kflared ./charts \
  --namespace kflared \
  --include-crds
```

## Install with Kustomize

Kustomize is the canonical source for generated manifests and is available through the repository-local Task wrapper:

```powershell
./task.ps1 install
./task.ps1 deploy IMG=<registry>/kflared:<tag>
```

Task is pinned in `tools/task` and does not need a global installation. Run `./task.ps1 --list` to inspect all available tasks.

## Create the API token Secret

By default, create the administrator-managed Secret in the controller namespace:

```sh
kubectl -n kflared create secret generic cloudflare-api-token \
  --from-literal=api-token='<CLOUDFLARE_API_TOKEN>'
```

For External Secrets Operator, opt in through Helm values instead:

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

The chart does not install External Secrets Operator or its CRDs. The remote key depends on the configured backend; the resulting `api-token` key must match `ClusterCloudflareProvider.spec.apiTokenSecretRef.key`.

## Optional chart-managed provider

The chart can optionally create one cluster-scoped `ClusterCloudflareProvider`. It is disabled by default because its account and namespace-access policy are installation-specific. When enabled, the chart derives the provider's `spec.controller` from `controllerClass`, and defaults its cluster-scoped name to the chart fullname.

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

`accountID` and one or more `allowedDNSZones` are required when enabled. `apiTokenSecretRef.name` is optional: it defaults to `externalSecrets.targetSecretName`, so the same rendered provider works with either a manually created Secret or the chart's optional `ExternalSecret`. The key defaults to `api-token` and must match the generated or manually managed Secret key.

> **Warning:** `bindingNamespaceSelector: {}` intentionally permits bindings from every namespace. Use a namespace-label selector for a shared cluster.

Before uninstalling this release, delete or migrate every `CloudflareTunnelBinding` that references the chart-managed provider. The provider finalizer blocks deletion while bindings still reference it; because Helm also removes the controller, the finalizer cannot finish after an uninstall with remaining bindings.

The controller has no cluster-wide Secret permission. A namespace-scoped Role allows it to read credentials and manage connector resources only in its system namespace.

## Create a provider and binding

Apply a provider, authorize a tenant namespace with the provider's selector, then apply a binding in that namespace:

```sh
kubectl apply -f config/samples/kflared_v1alpha1_clustercloudflareprovider.yaml
kubectl label namespace my-app kflared.kodeblox.com/cloudflare-provider=default
kubectl -n my-app apply -f config/samples/kflared_v1alpha1_cloudflaretunnelbinding.yaml
```

Adapt the samples to your account ID, DNS zones, Gateway listener, and internal Traefik Service before applying them. Continue with [Configuration](configuration.md) for the complete resource contract.
