---
title: Architecture
description: Understand KFlared's integration boundary, traffic path, and ownership model.
---

`ClusterCloudflareProvider` describes an account trust boundary. `CloudflareTunnelBinding` binds one listener of one Traefik-managed Gateway to one controller-owned, remotely managed Cloudflare Tunnel. A Gateway does not represent a tunnel in this integration mode.

```text
Cloudflare edge
  -> remotely managed Cloudflare Tunnel
  -> official cloudflared connector Deployment
  -> internal Traefik Service
  -> Traefik Gateway and HTTPRoutes
  -> workload Services
```

The controller reads Traefik-owned Gateway and HTTPRoute status. It requires current Gateway `Accepted=True` and `Programmed=True`, and route-parent `Accepted=True` and `ResolvedRefs=True`. It never writes Gateway API status and does not reinterpret route matches, filters, or backends. Traefik remains the L7 data plane and remains responsible for `ReferenceGrant`-protected backend references.

For every eligible concrete hostname, the controller writes a complete, canonically ordered remote configuration:

```yaml
ingress:
  - hostname: app.example.com
    service: http://traefik.application.svc.cluster.local:80
  - service: http_status:404
```

It deploys at least two official cloudflared connectors in `kflared-system`. Every hostname targets `<tunnel-id>.cfargotunnel.com` through an optional binding-owned `DNSEndpoint`. Without that CRD, exact records remain visible in binding status for manual administration.

The controller continuously compares desired and observed state. Kubernetes watches trigger prompt reconciliation; controller-runtime exponential backoff handles errors, while stable validation conditions use periodic requeues without error storms.

## Ownership boundaries

| Resource                                             | Owner             | Controller action                          |
| ---------------------------------------------------- | ----------------- | ------------------------------------------ |
| Cloudflare account                                   | Administrator     | Reference only                             |
| DNS zone                                             | Administrator     | Allow-list only                            |
| Tunnel and complete ingress config                   | Binding           | Create, recover, repair, delete or retain  |
| Connector Deployment, PDB, and token Secret          | Binding           | Create, repair, delete                     |
| DNSEndpoint                                          | Binding           | Create, repair, delete when the CRD exists |
| GatewayClass, Gateway, HTTPRoute, and ReferenceGrant | Traefik and users | Read only                                  |
| Traefik Service                                      | Administrator     | Read only                                  |

Tunnel names include the cluster identity and complete binding UID. A lost create response can therefore be recovered without adopting a name belonging to another Kubernetes object. Cloudflare's tunnel API does not expose arbitrary controller ownership metadata, so exact deterministic identity, remote-management mode, immutable provider and Gateway identity, and the persisted tunnel ID form the adoption boundary.

## MVP boundary

The first release is an integration controller, not a Gateway API implementation. GRPCRoute, wildcard hostnames, HTTPS origins, direct Service backend routing, shared or imported tunnels, externally managed connectors, and native GatewayClass ownership are deferred.
