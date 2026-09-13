# MVP architecture

`CloudflareProvider` describes an account trust boundary. `CloudflareTunnelBinding` binds one listener of one Traefik-managed Gateway to one controller-owned, remotely managed Cloudflare Tunnel. A Gateway does not represent a tunnel in this integration mode.

The controller reads Traefik-owned Gateway and HTTPRoute status. It requires current Gateway `Accepted=True` and `Programmed=True`, and route-parent `Accepted=True` and `ResolvedRefs=True`. It never writes Gateway API status and does not reinterpret route matches, filters, or backends. Traefik remains the L7 data plane and therefore remains responsible for ReferenceGrant-protected backend references.

For every eligible concrete hostname, the controller writes this complete, canonically ordered remote configuration:

```yaml
ingress:
- hostname: app.example.com
  service: http://traefik.application.svc.cluster.local:80
- service: http_status:404
```

It deploys at least two official cloudflared connectors in `kflared-system`. Every hostname targets `<tunnel-id>.cfargotunnel.com` through an optional binding-owned DNSEndpoint. Without that CRD, exact records remain visible in binding status for manual administration.

The controller continuously compares desired and observed state. Kubernetes watches trigger prompt reconciliation; controller-runtime exponential backoff handles errors, while stable validation conditions use periodic requeues without error storms.

## Ownership boundaries

| Resource | Owner | Controller action |
| --- | --- | --- |
| Cloudflare account | Administrator | reference only |
| DNS zone | Administrator | allow-list only |
| Tunnel and complete ingress config | Binding | create, adopt by exact deterministic identity, repair, delete or retain |
| Connector Deployment/PDB/token Secret | Binding | create, repair, delete |
| DNSEndpoint | Binding | create, repair, delete when CRD exists |
| GatewayClass/Gateway/HTTPRoute/ReferenceGrant | Traefik/users | read only |
| Traefik Service | Administrator | read only |

Tunnel names include the cluster identity and complete binding UID. A lost create response can therefore be recovered without adopting a name belonging to another Kubernetes object. Cloudflare's first-party tunnel API does not expose arbitrary controller ownership metadata, so exact deterministic identity, remote-management mode, immutable provider/Gateway identity, and persisted tunnel ID form the adoption boundary.
