# ADR 0005: Optional DNS automation and explicit deletion

Status: accepted

The Cloudflare API token has no DNS permission. When ExternalDNS's DNSEndpoint CRD is discoverable, the binding owns CNAME endpoints targeting `<tunnel-id>.cfargotunnel.com`. Otherwise, the controller reports exact manual records without treating the absent CRD as a retryable failure.

`Delete` removes managed DNS and connector resources, then deletes the remote tunnel. `Retain` removes managed Kubernetes resources but leaves the tunnel and its remote configuration intact. Manual DNS cannot be removed and receives a warning because records surviving tunnel deletion can produce Cloudflare error 1016.

The MVP does not infer DNS propagation and has no manual-DNS acknowledgement. Consequently `Ready=True` requires managed DNSEndpoint creation as well as connector availability.
