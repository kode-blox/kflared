---
title: Configuration
description: Configure ClusterCloudflareProvider and CloudflareTunnelBinding resources safely.
---

KFlared exposes three `kflared.kodeblox.com/v1alpha1` custom resources. A cluster-scoped `ClusterCloudflareProvider` defines an administrator-owned Cloudflare account boundary. A namespaced `CloudflareTunnelBinding` publishes one listener of one Traefik-managed Gateway. A namespaced `CloudflareTunnelPrivateRoute` routes one Service ClusterIP to enrolled Cloudflare One clients.

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
| `allowedDNSZones`          | Concrete suffix allow-list for published hostnames. May be empty for a private-route-only provider; then hostname bindings are not allowed. |
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

## CloudflareTunnelPrivateRoute

```yaml
apiVersion: kflared.kodeblox.com/v1alpha1
kind: CloudflareTunnelPrivateRoute
metadata:
  name: kubernetes-api
  namespace: kflared
spec:
  controller: kflared
  providerRef:
    name: default
  serviceRef:
    name: kubernetes
    namespace: default
    port: https
  connectorReplicas: 2
  deletionPolicy: Delete
```

| Field | Purpose |
| --- | --- |
| `controller` | KFlared controller class that owns this route. |
| `providerRef.name` | Cluster-scoped provider supplying account credentials and tunnel ownership. |
| `serviceRef` | Destination Service name, optional namespace (defaults to the route namespace), and port. The Service must have a ClusterIP. |
| `connectorReplicas` | Number of official cloudflared connector replicas. |
| `deletionPolicy` | `Delete` removes the Cloudflare CIDR route and tunnel; `Retain` retains their Cloudflare state while deleting generated Kubernetes resources. |

KFlared resolves the Service's current ClusterIP and creates an exact single-address `/32` Cloudflare private network route. Do not enter or hard-code a ClusterIP in the resource. A cross-namespace Service reference requires this grant in the Service namespace:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: ReferenceGrant
metadata:
  name: allow-kflared-api-route
  namespace: default
spec:
  from:
    - group: kflared.kodeblox.com
      kind: CloudflareTunnelPrivateRoute
      namespace: kflared
  to:
    - group: ""
      kind: Service
      name: kubernetes
```

The account token must have the Cloudflare API permissions required to create, configure, retrieve, and delete the tunnel and private route. The private-route API accepts either `Cloudflare One Networks Write` or `Cloudflare Tunnel Write` for route operations; tunnel lifecycle operations may also require `Cloudflare Tunnel Write` or `Cloudflare One Connectors Write`. These permission labels may cover overlapping operations, so grant only the account permissions needed by the enabled KFlared features rather than assuming each API call has a narrower independent permission. DNS edit permission is not required for private routes. The Cloudflare route is not a published hostname and does not use `HTTPRoute`, `DNSEndpoint`, or the binding's DNS automation setting. Users continue to use their own Kubernetes credentials with `kubectl`.

Cloudflare One setup is a separate, user-executed prerequisite. Follow Cloudflare's [connect a CIDR through cloudflared guide](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/private-net/cloudflared/connect-cidr/): add this `/32` to the WARP client's Split Tunnel include list because RFC1918 destinations are excluded by default, and create Gateway network policies that explicitly allow intended users/devices to reach the route on the intended port, followed by a catch-all block for the private network. Cloudflare notes that enrolled devices can otherwise reach the private network by default when Gateway policies do not restrict it. For Kubernetes API access, the intended destination is typically TCP 443. KFlared only confirms the port exists on the Service; it does not constrain the CIDR route by port.

The `/32` route covers the Service IP regardless of port. Although `serviceRef.port` must identify a port on the Service, it does not restrict routed traffic to that port. Apply Cloudflare One access policy and cluster firewall/network policy to allow only intended ports, such as TCP 443 for the Kubernetes API.

Only one route resource per provider/controller class may own a given network. If multiple KFlared resources resolve to the same `/32`, the earliest created resource (then namespace/name as a stable tie-breaker) wins and later resources report `RouteConflict`. A CIDR already routed by another Cloudflare route is also rejected. KFlared only adopts a remote route when its stored route ID and ownership comment identify this resource; it refuses to delete a route whose identity no longer matches.

Private-route status includes `routeID`, `network`, `tunnelID`, generated resource names, and the `Accepted`, `Programmed`, `ConnectorReady`, and `Ready` conditions. `Ready=True` indicates the private route and connector are programmed and available.

## Status

Binding status records the tunnel ID and CNAME, published hostnames, exact DNS records, generated resource names, and these conditions:

- `Accepted`: configuration and exclusive ownership are valid.
- `Programmed`: the remote tunnel configuration matches eligible hostnames.
- `ConnectorReady`: every desired cloudflared replica is available.
- `DNSAutomationReady`: an owned `DNSEndpoint` exists, or automation was intentionally disabled; when enabled without the CRD, the condition reports `ManualConfigurationRequired`.
- `Ready`: tunnel programming and connector availability are ready, together with managed DNS when DNS automation is enabled.

Run `kubectl -n <namespace> get cloudflaretunnelbinding <name> -o yaml` when diagnosing a binding. The condition reason and message explain the current gate.
