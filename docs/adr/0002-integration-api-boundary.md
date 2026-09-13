# ADR 0002: Integration-mode API boundary

Status: accepted

The MVP introduces a cluster-scoped `CloudflareProvider` and namespaced `CloudflareTunnelBinding`. A binding references one existing Traefik Gateway/listener and its internal Service; the Gateway does not represent the tunnel.

This keeps Gateway API status and route semantics under the implementation that actually proxies requests. The controller publishes only concrete hostnames from current, accepted same-namespace HTTPRoutes and sends all traffic to Traefik. GRPCRoute, direct backend routing, cross-namespace controller references, filters, wildcard hosts, and HTTPS origins are deferred.

The API group is `kflared.kodeblox.com`, initially `v1alpha1`. Provider and Gateway identity fields are immutable because changing either would make external ownership and safe finalization ambiguous.
