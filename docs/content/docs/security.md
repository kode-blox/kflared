---
title: Security
description: Review the credential, tenancy, workload, and disclosure boundaries enforced by KFlared.
---

## Credentials

`ClusterCloudflareProvider.spec.apiTokenSecretRef` names one Secret and key in `kflared`. The reference cannot select another namespace. The manager's ClusterRole has no Secret access; a namespace Role grants only the operations needed in the system namespace. Sensitive resource reads bypass the shared cache so the manager does not require a cluster-wide Secret informer.

The Helm chart can optionally create an External Secrets Operator `ExternalSecret`; it still targets a Secret in the release namespace and grants KFlared no access to the external secret backend.

Use one least-privilege API token per operational trust boundary. The MVP needs account-scoped Cloudflare Tunnel and Connector write access and no DNS permission. Rotate the value in place; the provider's periodic credential check and binding reconciliation will observe it.

Connector tokens are written to an operator-generated Secret, mounted read-only, and passed to official cloudflared through `--token-file`. They are never placed in arguments, environment variables, Events, or status.

Private-route reconciliation also creates a Cloudflare CIDR route. The route API accepts either `Cloudflare One Networks Write` or `Cloudflare Tunnel Write`; tunnel lifecycle operations may also require `Cloudflare Tunnel Write` or `Cloudflare One Connectors Write`. Because Cloudflare permissions can cover overlapping API operations, grant only the account permissions needed by the enabled KFlared features and do not assume each call has a narrower independent permission. This feature does not require DNS edit access. The connector token is the only credential mounted in the connector Pod; that Pod does not need a Kubernetes ServiceAccount token to forward traffic. A local `kubectl` process retains and uses the user's normal Kubernetes credentials through the WARP client. Cloudflare One enrollment, device posture, and Access policy are configured separately by the Cloudflare administrator.

## Tenancy

Providers are cluster-scoped administrator resources. Configure an explicit DNS suffix allow-list for published hostnames, or an empty list for private-route-only use, and a namespace label selector. `{}` intentionally allows every namespace. Bindings, Gateways, and eligible HTTPRoutes remain in the binding's application namespace. An origin Service can be in another namespace only when a matching Gateway API `ReferenceGrant` exists there. KFlared enforces this custom reference during reconciliation; Kubernetes does not enforce it automatically. A grant does not provide packet-level networking access.

KFlared checks authorization before reading the cross-namespace Service. If authorization is absent or revoked, it fails closed; revocation deprograms the tunnel ingress and removes managed DNS. The Service must be a normal internal `ClusterIP` Service. Because a grant cannot restrict an individual port, a dedicated single-port origin Service is recommended.

One binding owns one Gateway and every published hostname. Conflicting later bindings do not mutate Kubernetes or Cloudflare resources. Do not grant tenant users permission to edit providers or the system namespace.

Each `CloudflareTunnelPrivateRoute` claims only the current Service ClusterIP as an exact `/32` route. Cross-namespace Service reads require a matching ReferenceGrant in the Service namespace; KFlared checks the reference authorization before reading the Service. Route ownership is tracked by Cloudflare route identity and Kubernetes owner identity so one resource cannot delete another route. This route is independent of public hostname ingress and does not create public DNS records.

The route exposes the entire Service IP at the network layer; selecting a Service port does not constrain Cloudflare's CIDR route to that port. Restrict the destination port through Cloudflare One policy and cluster firewall or network policy. For a Kubernetes API Service, allow only the API port needed by clients.

Cloudflare's [CIDR routing guide](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/private-net/cloudflared/connect-cidr/) says RFC1918 addresses are excluded from WARP Split Tunnels by default and that enrolled devices can reach private networks unless Gateway policy restricts them. Operators must include the `/32` in the intended clients' Split Tunnel configuration and order Cloudflare Gateway policy to allow only intended identities and ports, then block other private-network traffic. These controls are external to KFlared and are required to keep the route's access narrow.

## Workloads and networking

Generated connector Pods:

- run the pinned, official cloudflared image as UID and GID 65532;
- disable service-account token mounting;
- use a read-only root filesystem and runtime-default seccomp;
- drop every Linux capability and disallow privilege escalation;
- mount only the tunnel token Secret.

KFlared does not install a NetworkPolicy because Cloudflare connector endpoints and cluster DNS requirements can evolve. Administrators may supply an egress-only policy after validating current Cloudflare requirements. KFlared rejects origin Services of type `LoadBalancer` or `NodePort` rather than treating them as private.

## Security policy

Please report suspected vulnerabilities privately through GitHub's security advisory feature for `kode-blox/golfs`. Do not include tokens, signed URLs, private keys, credentials, or object data in a public issue.

Only the latest released minor version receives security fixes during the v0.x period. During the initial implementation phase, provider validation and GitHub App/GCM validation are manual release responsibilities because automated integration testing is deferred.
