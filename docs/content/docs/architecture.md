---
title: Architecture
description: Understand KFlared's integration boundary, traffic path, and ownership model.
---

`ClusterCloudflareProvider` describes an account trust boundary. `CloudflareTunnelBinding` binds one listener of one Traefik-managed Gateway to one controller-owned, remotely managed Cloudflare Tunnel. `CloudflareTunnelPrivateRoute` separately connects an enrolled Cloudflare One client to one Kubernetes Service through a Cloudflare private network route. A Gateway does not represent a tunnel in either integration mode.

Each KFlared manager requires a non-empty controller class, and the Helm chart defaults it to `kflared`. Providers and bindings declare that class in `spec.controller`; the manager reconciles only exact matches, and a binding must also reference a provider with the same class. This allows separately configured KFlared instances to coexist in one cluster. Additional installations must override the default with distinct classes. The class is immutable on each resource, so migration between instances requires recreating resources.

Leader-election lease names are derived from the controller class, so different classes do not suppress each other when they share a namespace. Finalizer names remain stable rather than embedding the class. The manager checks the immutable class before any finalization work, generated Kubernetes children are identified by the binding UID, and cleanup refuses to delete a child carrying another binding's ownership label. Remote tunnel names also include the binding UID. Together these boundaries prevent one class from adopting or cleaning up another class's resources while avoiding class-derived finalizer names that could strand objects after configuration changes.

```text
Cloudflare edge
  -> remotely managed Cloudflare Tunnel
  -> official cloudflared connector Deployment
  -> internal Traefik Service
  -> Traefik Gateway and HTTPRoutes
  -> workload Services
```

The controller reads Traefik-owned Gateway and HTTPRoute status. It requires current Gateway `Accepted=True` and `Programmed=True`, and route-parent `Accepted=True` and `ResolvedRefs=True`. It never writes Gateway API status and does not reinterpret route matches, filters, or backends. Traefik remains the L7 data plane and remains responsible for `ReferenceGrant`-protected backend references.

The binding, Gateway, and eligible HTTPRoutes stay in the binding's application namespace. The origin Service is selected per binding and may reside in another namespace only when a matching Gateway API `ReferenceGrant` is present in the Service namespace. KFlared validates this custom reference during reconciliation before reading the Service; Kubernetes does not automatically enforce it. The grant authorizes a reference, not packet-level networking. Revoking it causes KFlared to deprogram tunnel ingress and remove managed DNS. Origin selection remains per binding because a provider account does not imply one origin: one provider can serve multiple origins, multiple providers can use one origin, and origins can be migrated incrementally for blue-green changes.

Only normal internal `ClusterIP` Services are accepted as origins. Use a dedicated single-port Service because Gateway API `ReferenceGrant` cannot restrict access to a particular port.

For every eligible concrete hostname, the controller writes a complete, canonically ordered remote configuration:

```yaml
ingress:
  - hostname: app.example.com
    service: http://traefik.application.svc.cluster.local:80
  - service: http_status:404
```

It deploys at least two official cloudflared connectors in `kflared`. By default, every hostname targets `<tunnel-id>.cfargotunnel.com` through a binding-owned `DNSEndpoint`. Without that CRD, exact records remain visible in binding status for manual administration. A binding can disable DNS automation when its public hostname must retain an externally managed target; the tunnel hostname remains programmed for alternate Cloudflare edge paths without asserting public CNAME intent.

The controller continuously compares desired and observed state. Kubernetes watches trigger prompt reconciliation; controller-runtime exponential backoff handles errors, while stable validation conditions use periodic requeues without error storms.

## Private network route

```text
kubectl on enrolled client (using the user's Kubernetes credentials)
  -> Cloudflare One client / WARP
  -> Cloudflare private network route (<Service ClusterIP>/32)
  -> remotely managed Cloudflare Tunnel
  -> official cloudflared connector Deployment
  -> Kubernetes Service
```

Each `CloudflareTunnelPrivateRoute` owns a distinct Cloudflare CIDR route whose network is the referenced Service's current ClusterIP with a `/32` mask. KFlared reads the Service and keeps the route synchronized when its ClusterIP changes. The controller does not choose a cluster-wide Pod or Service CIDR and does not configure published-hostname ingress; Cloudflare CIDR routes and tunnel ingress are separate Cloudflare configuration surfaces. The built-in Kubernetes API Service is a valid target, but its name, namespace, port, and address are supplied through the resource rather than compiled into KFlared.

The route resource is namespaced. A `serviceRef.namespace` outside that namespace requires a Gateway API `ReferenceGrant` in the target Service namespace, allowing `kflared.kodeblox.com` kind `CloudflareTunnelPrivateRoute` from the route namespace to reference the named core `Service`. KFlared validates this grant before reading the cross-namespace Service. This authorizes the reference, not network traffic; cluster networking must permit connector traffic to the Service.

The connector does not need a Kubernetes ServiceAccount token to proxy packets. KFlared mounts only the Cloudflare connector token in its generated Deployment. `kubectl` continues to authenticate to Kubernetes with the user's existing Kubernetes credentials over WARP. Cloudflare One administrators must enroll and authorize the client and configure access to the routed private network; creating the KFlared resource alone does not enroll clients or grant Cloudflare Access policy.

The route covers the Service IP independent of port. `serviceRef.port` confirms that the selected port exists on the Service; it cannot narrow a CIDR route to that port. Administrators must enforce port restrictions in Cloudflare One access policy and cluster firewall or network policy.

## Ownership boundaries

| Resource                                             | Owner             | Controller action                          |
| ---------------------------------------------------- | ----------------- | ------------------------------------------ |
| Cloudflare account                                   | Administrator     | Reference only                             |
| DNS zone                                             | Administrator     | Allow-list only                            |
| Tunnel and complete ingress config                   | Binding           | Create, recover, repair, delete or retain  |
| Connector Deployment, PDB, and token Secret          | Binding           | Create, repair, delete                     |
| DNSEndpoint                                          | Binding           | Create and repair when enabled; delete when disabled or the binding is deleted |
| GatewayClass, Gateway, HTTPRoute, and ReferenceGrant | Traefik and users | Read only; grants are checked for cross-namespace origins |
| Traefik Service                                      | Administrator     | Read only                                  |
| Cloudflare CIDR route (`<Service ClusterIP>/32`)      | PrivateRoute      | Create, adopt only when ownership is proven, update and delete or retain |
| PrivateRoute connector Deployment and token Secret    | PrivateRoute      | Create, repair, delete                     |

Tunnel names include the cluster identity and complete binding UID. A lost create response can therefore be recovered without adopting a name belonging to another Kubernetes object. Cloudflare's tunnel API does not expose arbitrary controller ownership metadata, so exact deterministic identity, remote-management mode, immutable provider and Gateway identity, and the persisted tunnel ID form the adoption boundary.

## MVP boundary

The first release is an integration controller, not a Gateway API implementation. GRPCRoute, wildcard hostnames, HTTPS origins, direct Service backend routing, shared or imported tunnels, externally managed connectors, and native GatewayClass ownership are deferred.
