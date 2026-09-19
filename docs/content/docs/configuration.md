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
  gatewayServiceRef:
    name: traefik
    port: web
  connectorReplicas: 2
  deletionPolicy: Delete
```

| Field               | Purpose                                                                                     |
| ------------------- | ------------------------------------------------------------------------------------------- |
| `controller`        | Required, immutable KFlared controller class that owns this binding.                         |
| `providerRef.name`  | Cluster-scoped provider to use. The reference is immutable.                                 |
| `gatewayRef`        | Existing Gateway and HTTP listener in the binding namespace. The reference is immutable.    |
| `gatewayServiceRef` | Internal, non-headless Traefik Service and port used as the tunnel origin.                  |
| `connectorReplicas` | Official cloudflared connector replicas. Defaults to and cannot be lower than `2`.          |
| `deletionPolicy`    | `Delete` removes the remote tunnel; `Retain` leaves it and its remote configuration intact. |

The binding, Gateway, HTTPRoutes, and origin Service must share a namespace. Eligible routes must expose concrete, non-wildcard hostnames and report current `Accepted=True` and `ResolvedRefs=True` status for the selected Gateway listener.

The binding's `controller` must match both the running manager's `controllerClass` and its referenced provider's `controller`. A mismatch prevents reconciliation. For resources created before this field existed, install the new CRDs first while the old controller is still running, explicitly patch matching `spec.controller` values onto every provider and binding, and only then roll out the new controller with that class. Kubernetes does not evaluate the field-scoped immutability transition while the old value is absent, but later changes are rejected. Recreate a resource to change its class after migration.

## Status

Binding status records the tunnel ID and CNAME, published hostnames, exact DNS records, generated resource names, and these conditions:

- `Accepted`: configuration and exclusive ownership are valid.
- `Programmed`: the remote tunnel configuration matches eligible hostnames.
- `ConnectorReady`: every desired cloudflared replica is available.
- `DNSAutomationReady`: an owned `DNSEndpoint` exists; without the CRD, the condition reports `ManualConfigurationRequired`.
- `Ready`: tunnel programming, connector availability, and managed DNS are all ready.

Run `kubectl -n <namespace> get cloudflaretunnelbinding <name> -o yaml` when diagnosing a binding. The condition reason and message explain the current gate.
