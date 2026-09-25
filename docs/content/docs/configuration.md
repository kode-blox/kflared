---
title: Configuration
description: Configure ClusterCloudflareProvider and CloudflareTunnelBinding resources safely.
---

KFlared exposes two `kflared.kodeblox.com/v1alpha1` custom resources. A cluster-scoped `ClusterCloudflareProvider` defines an administrator-owned Cloudflare account boundary. A namespaced `CloudflareTunnelBinding` publishes one listener of one Traefik-managed Gateway.

## ClusterCloudflareProvider

```yaml
apiVersion: kflared.kodeblox.com/v1alpha1
kind: ClusterCloudflareProvider
metadata:
  name: default
spec:
  controller: kflared
  accountID: 0123456789abcdef0123456789abcdef
  apiTokenSecretRef:
    name: cloudflare-api-token
    key: api-token
  allowedDNSZones:
    - example.com
  bindingNamespaceSelector:
    matchLabels:
      kflared.kodeblox.com/cloudflare-provider: default
```

| Field                      | Purpose                                                                             |
| -------------------------- | ----------------------------------------------------------------------------------- |
| `controller`               | Required, immutable KFlared controller class that owns this provider.               |
| `accountID`                | Cloudflare account that owns tunnels. The field is immutable.                       |
| `apiTokenSecretRef`        | Secret name and key in the KFlared system namespace.                                |
| `allowedDNSZones`          | Concrete suffix allow-list for published hostnames. At least one zone is required.  |
| `bindingNamespaceSelector` | Namespaces allowed to use the provider. `{}` intentionally permits every namespace. |

The provider reports `Accepted` for configuration validity and `CredentialsValid` after checking the token with Cloudflare.

## CloudflareTunnelBinding

```yaml
apiVersion: kflared.kodeblox.com/v1alpha1
kind: CloudflareTunnelBinding
metadata:
  name: public
  namespace: my-app
spec:
  controller: kflared
  providerRef:
    name: default
  gatewayRef:
    name: traefik
    sectionName: http
  originServiceRef:
    name: traefik-cloudflare
    namespace: traefik
    port: cloudflare
  connectorReplicas: 2
  dnsAutomationEnabled: true
  deletionPolicy: Delete
```

| Field                  | Purpose                                                                                                      |
| ---------------------- | ------------------------------------------------------------------------------------------------------------ |
| `controller`           | Required, immutable KFlared controller class that owns this binding.                                         |
| `providerRef.name`     | Cluster-scoped provider to use. The reference is immutable.                                                 |
| `gatewayRef`           | Existing Gateway and HTTP listener in the binding namespace. The reference is immutable.                    |
| `originServiceRef`     | Service name and port used as the tunnel origin; namespace defaults to the binding namespace.               |
| `connectorReplicas`    | Official cloudflared connector replicas. Defaults to and cannot be lower than `2`.                          |
| `dnsAutomationEnabled` | Whether KFlared manages an ExternalDNS `DNSEndpoint`. Defaults to `true`; set `false` for external DNS.     |
| `deletionPolicy`       | `Delete` removes the remote tunnel; `Retain` leaves it and its remote configuration intact.                 |

The binding, Gateway, and eligible HTTPRoutes remain in the same application namespace. Only the origin Service may be elsewhere. A cross-namespace origin requires a matching Gateway API `ReferenceGrant` in the Service namespace; KFlared checks that grant during reconciliation because Kubernetes does not automatically enforce this custom reference. Same-namespace origins need no grant. Eligible routes must expose concrete, non-wildcard hostnames and report current `Accepted=True` and `ResolvedRefs=True` status for the selected Gateway listener.

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: ReferenceGrant
metadata:
  name: allow-my-app-kflared-origin
  namespace: traefik
spec:
  from:
    - group: kflared.kodeblox.com
      kind: CloudflareTunnelBinding
      namespace: my-app
  to:
    - group: ""
      kind: Service
      name: traefik-cloudflare
```

The origin must be a normal internal `ClusterIP` Service with the requested named or numeric port. Headless, `ExternalName`, `NodePort`, and `LoadBalancer` Services are rejected. A `ReferenceGrant` authorizes the Service reference, not an individual port, so use a dedicated single-port origin Service (for example, `traefik-cloudflare`) to make the intended exposure clear. The grant does not provide packet-level networking access; cluster networking and policy still determine reachability.

Origin selection belongs to each binding because provider identity does not imply origin identity. This allows one provider to serve multiple origins, multiple providers to use one origin, and incremental blue-green migration without changing other bindings.

DNS automation is also selected per binding. Leave `dnsAutomationEnabled` omitted or set it to `true` to publish each eligible hostname as a Cloudflare-proxied CNAME to the tunnel through ExternalDNS. Set it to `false` when the public hostname must keep an externally managed target, such as a static site reached by users while a Cloudflare Worker reaches this tunnel through Workers VPC. KFlared still programs the hostname into the tunnel, but removes any previously owned `DNSEndpoint` and does not report a replacement CNAME as required DNS intent.

This v1alpha1 revision renames `spec.gatewayServiceRef` to `spec.originServiceRef`. There is no compatibility alias. Update manifests before upgrading. Installations with stored bindings need a coordinated migration so the new CRD and the rewritten binding objects are in place before the new controller starts; the old field is not used as the tunnel origin.

The binding's `controller` must match both the running manager's `controllerClass` and its referenced provider's `controller`. A mismatch prevents reconciliation. For resources created before this field existed, install the new CRDs first while the old controller is still running, explicitly patch matching `spec.controller` values onto every provider and binding, and only then roll out the new controller with that class. Kubernetes does not evaluate the field-scoped immutability transition while the old value is absent, but later changes are rejected. Recreate a resource to change its class after migration.

## Status

Binding status records the tunnel ID and CNAME, published hostnames, exact DNS records, generated resource names, and these conditions:

- `Accepted`: configuration and exclusive ownership are valid.
- `Programmed`: the remote tunnel configuration matches eligible hostnames.
- `ConnectorReady`: every desired cloudflared replica is available.
- `DNSAutomationReady`: an owned `DNSEndpoint` exists, or automation was intentionally disabled; when enabled without the CRD, the condition reports `ManualConfigurationRequired`.
- `Ready`: tunnel programming and connector availability are ready, together with managed DNS when DNS automation is enabled.

Run `kubectl -n <namespace> get cloudflaretunnelbinding <name> -o yaml` when diagnosing a binding. The condition reason and message explain the current gate.
