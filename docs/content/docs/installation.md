---
title: Installation
description: Install KFlared with Helm or Kustomize and prepare its cluster prerequisites.
---

KFlared publishes Traefik Gateway hostnames and routes Service IPs privately through controller-owned Cloudflare Tunnels. Choose the path you need, review the [security boundary](/security), then install the controller and configure its resources. Commands on this page are run by the cluster administrator against the intended kubeconfig context.

## Prerequisites

- Kubernetes 1.35 through 1.37.
- Gateway API v1.6.1 CRDs.
- For public hostname bindings: Traefik 3.7.10 or newer with the Kubernetes Gateway provider enabled, an internal non-headless Traefik Service, and an accepted Gateway/HTTPRoute for the application.
- For private Service routes: Cloudflare One client enrollment and network access policy; Traefik and ExternalDNS are not required.
- A Cloudflare account API token with Tunnel and Connector write access for the selected account.
- Optional: ExternalDNS and its `externaldns.k8s.io/v1alpha1` `DNSEndpoint` CRD.
- Optional: External Secrets Operator and a `ClusterSecretStore` when the chart should reconcile the API token Secret.

The Cloudflare token does not need DNS edit permission. ExternalDNS performs DNS changes when its CRD is installed.

## Select a release

Use the source and image from the same release. Clone the repository and check out an application release tag from [GitHub Releases](https://github.com/kode-blox/kflared/releases) before running commands that use local charts or samples:

```sh
git clone https://github.com/kode-blox/kflared.git
cd kflared
git checkout <application-release-tag>
```

Application tags use `v<version>`; chart tags use `chart-v<version>`. Replace `<application-version>` below with the selected application's plain version, without the `v` prefix. The local chart's `appVersion` selects the controller image when `image.tag` is empty; the explicit override below ensures the image matches the selected application source even when the chart version advances separately. Keep the selected chart, image, and CRDs compatible; see [Release process](/release-process) for their version contracts. Helm needs Helm and kubectl. The Kustomize Task path additionally requires Go at the version in `go.mod` and PowerShell 7 or newer.

## Install with Helm

The supported chart is rooted at `charts/`:

```sh
helm upgrade --install kflared ./charts \
  --namespace kflared \
  --create-namespace \
  --set image.tag=<application-version>
```

`controllerClass` defaults to `kflared`. Use that value as `spec.controller` on every provider and binding assigned to the installation. Override it with a stable, distinct value for each additional KFlared installation in the same cluster.

> **Warning:** Never run two KFlared installations with the same controller class. They would select the same resources even when installed in different namespaces. A second installation must set a distinct value, for example `--set controllerClass=kflared-secondary`, and its providers and bindings must use that same value.

KFlared CRDs live in Helm's special `crds/` directory. Helm installs them before templates and deliberately does not upgrade or delete them. Use `--skip-crds` only when a cluster administrator manages the CRDs separately.

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

Replace `<registry>/kflared:<tag>` with a published release image, such as `ghcr.io/kode-blox/kflared:<application-version>`. The application version has no `v` prefix in the image tag. Task is pinned in `tools/task` and does not need a global installation. Run `./task.ps1 --list` to inspect all available tasks.

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

`accountID` is required when enabled. Set `allowedDNSZones` to one or more concrete zones for published hostnames, or `[]` for a private-route-only provider. `apiTokenSecretRef.name` is optional: it defaults to `externalSecrets.targetSecretName`, so the same rendered provider works with either a manually created Secret or the chart's optional `ExternalSecret`. The key defaults to `api-token` and must match the generated or manually managed Secret key.

> **Warning:** `bindingNamespaceSelector: {}` intentionally permits bindings from every namespace. Use a namespace-label selector for a shared cluster.

Save optional chart settings in a values file and add `--values kflared-values.yaml` to the installation command, keeping the selected image tag. If using the chart-managed provider, skip the separate provider sample below and match its name and namespace selector in your bindings or private routes.

Before uninstalling this release, delete or migrate every `CloudflareTunnelBinding` and `CloudflareTunnelPrivateRoute` that references the chart-managed provider. The provider finalizer blocks deletion while either resource kind still references it; because Helm also removes the controller, the finalizer cannot finish after an uninstall with remaining references.

The controller has no cluster-wide Secret permission. A namespace-scoped Role allows it to read credentials and manage connector resources only in its system namespace.

## Configure the traffic path

For a public hostname, first create the application namespace, Traefik-managed Gateway, HTTPRoute, and backend workload. Match the Gateway listener and HTTPRoute hostname to the binding and provider. The sample binding points to `traefik/traefik-cloudflare` port `cloudflare`; create that internal Service for your Traefik installation or change the reference. The samples do not install Traefik or an application.

Adapt the provider and binding files before applying them, including the account ID, allowed DNS suffixes, Gateway, listener, and origin Service. Authorize the tenant namespace with the provider selector, and apply the sample ReferenceGrant in the origin Service namespace when using the cross-namespace sample:

```sh
kubectl apply -f config/samples/kflared_v1alpha1_clustercloudflareprovider.yaml
kubectl label namespace my-app kflared.kodeblox.com/cloudflare-provider=default
kubectl apply -f config/samples/gateway_v1_referencegrant.yaml
kubectl -n my-app apply -f config/samples/kflared_v1alpha1_cloudflaretunnelbinding.yaml
```

For private access, use the [private-route configuration](/configuration#cloudflaretunnelprivateroute) and its matching ReferenceGrant instead of a hostname binding. An empty provider DNS allow-list is valid for this path. Apply the adapted private-route sample only after configuring Cloudflare One enrollment, Split Tunnels, and access policy.

## Verify the deployment

For the default Helm release, wait for the manager and provider:

```sh
kubectl -n kflared rollout status deployment/kflared
kubectl wait --for=condition=CredentialsValid clustercloudflareprovider/default --timeout=120s
kubectl -n my-app get cloudflaretunnelbinding public -o yaml
```

The Kustomize Deployment is `kflared-controller-manager`. Adjust names when overriding the Helm fullname or provider name. A public binding should report `Accepted`, `Programmed`, `ConnectorReady`, `DNSAutomationReady`, and `Ready` as true. Ensure ExternalDNS has applied the reported CNAMEs, then request the application through its public hostname. Without ExternalDNS's CRD, use `status.dnsRecords` for manual DNS; readiness stays false because KFlared cannot verify that manual step. Use `dnsAutomationEnabled: false` only when an external DNS path is intentional.

For a private route, check its `network` matches the current Service ClusterIP, wait for `Ready=True`, and test the destination from an authorized enrolled client. Confirm that an unauthorized client or disallowed port is blocked by your access policy. Controller readiness alone does not establish client authorization or destination reachability.

Continue with [Configuration](/configuration) for field contracts and [Operations](/operations) for condition-based diagnosis and lifecycle management.

## Upgrading existing installations

Plan CRD, controller, and custom-resource changes together. Helm's CRD lifecycle is described in its [CRD documentation](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/). The following migration notes apply only when upgrading from the corresponding older schema; new installations can use the current configuration directly.

### Upgrade to one connector

To set `spec.connectorReplicas: 1` on an existing installation, apply the updated `CloudflareTunnelBinding` and `CloudflareTunnelPrivateRoute` CRDs from `config/crd/bases/` before updating the resources. Helm does not upgrade CRDs in `charts/crds/`; if the old CRD remains, Kubernetes still rejects one replica. Roll out the updated controller after the CRDs. The new default of one applies to newly defaulted resources; existing resources with `connectorReplicas: 2` must be changed explicitly.

KFlared no longer creates or manages connector PodDisruptionBudgets. Existing budgets remain in the controller namespace until an administrator deletes them. Inspect the budget and its labels before deleting it; the controller does not perform this cleanup.

### Upgrade to per-binding DNS automation control

Before using `spec.dnsAutomationEnabled`, apply the updated CRD from `config/crd/bases/` or synchronize the equivalent CRD-only GitOps source. An ordinary `helm upgrade` does **not** update `charts/crds/`, so it cannot add this field to an existing cluster.

Use ordered rollout phases: first synchronize the CRD, then roll out the controller that understands the field, and only then synchronize bindings that set `dnsAutomationEnabled: false`. Keep these as separate GitOps sync waves (or equivalent ordered releases). An older controller ignores the new field and can continue creating the binding-owned `DNSEndpoint` until its rollout completes.

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
