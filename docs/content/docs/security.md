---
title: Security
description: Protected assets, trust boundaries, controls, and accepted security limitations.
---

KFlared's security model describes the assets it protects, the trust boundaries it crosses, its controls, and its accepted limitations.

## Protected assets

- Cloudflare account API credentials.
- Tunnel connector tokens.
- Ownership and integrity of remote tunnels, private routes, ingress configuration, and managed DNS declarations.
- Authorized tenant and client access to cluster workloads.
- Controller and connector availability.

## Trust boundaries

| Boundary                                  | Security requirement                                                                                                                                                  |
| ----------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Tenant namespace to Cloudflare account    | Administrators define permitted tenants and public DNS suffixes. Tenant users must not edit providers, namespace authorization labels, or the system namespace.       |
| Resource to another namespace's Service   | Target-namespace owners authorize Service references separately from packet-level networking access.                                                                  |
| Manager to Kubernetes and Cloudflare APIs | Keep account credentials in the controller namespace and scope them to the intended Cloudflare account. The manager still reads routing resources across the cluster. |
| Connector to origin workloads             | Protect the connector's tunnel credentials, origin reachability, and application authorization.                                                                       |
| Enrolled client to private Service IP     | Administrators constrain enrolled identities, device posture, network destinations, and ports; the destination retains its own authentication.                        |

## Controls

### Credentials and authorization

`ClusterCloudflareProvider.spec.apiTokenSecretRef` names one Secret and key in the manager's system namespace (`kflared` in the standard installation, or the Helm release namespace). The reference cannot select another namespace. The manager's ClusterRole has no Secret access; a namespace Role grants only the operations needed in the system namespace. Secret reads bypass the shared cache so the manager does not require a cluster-wide Secret informer. That Role can read Secrets throughout the system namespace; it is not restricted to one Secret name. Keep tenant workloads and unrelated credentials outside that namespace.

The Helm chart can optionally create an External Secrets Operator `ExternalSecret`; it still targets a Secret in the release namespace and grants KFlared no access to the external secret backend.

Use one least-privilege API token per operational trust boundary. The MVP needs account-scoped Cloudflare Tunnel and Connector write access and no DNS permission. Rotate the value in place; the provider's periodic credential check and binding reconciliation will observe it.

Connector tokens are written to an operator-generated Secret, mounted read-only, and passed to official cloudflared through `--token-file`. They are never placed in arguments, environment variables, Events, or status.

Private-route reconciliation also creates a Cloudflare CIDR route. The route API accepts either `Cloudflare One Networks Write` or `Cloudflare Tunnel Write`; tunnel lifecycle operations may also require `Cloudflare Tunnel Write` or `Cloudflare One Connectors Write`. Because Cloudflare permissions can cover overlapping API operations, grant only the account permissions needed by the enabled KFlared features and do not assume each call has a narrower independent permission. This feature does not require DNS edit access. The connector token is the only credential mounted in the connector Pod; that Pod does not need a Kubernetes ServiceAccount token to forward traffic. A local `kubectl` process retains and uses the user's normal Kubernetes credentials through the WARP client. Cloudflare One enrollment, device posture, and Access policy are configured separately by the Cloudflare administrator.

### Isolation and integrity

Providers are cluster-scoped administrator resources. Configure an explicit DNS suffix allow-list for published hostnames, or an empty list for private-route-only use, and a namespace label selector. `{}` intentionally allows every namespace. Bindings, Gateways, and eligible HTTPRoutes remain in the binding's application namespace. An origin Service can be in another namespace only when a matching Gateway API `ReferenceGrant` exists there. KFlared enforces this custom reference during reconciliation; Kubernetes does not enforce it automatically. A grant does not provide packet-level networking access.

KFlared checks authorization before reading the cross-namespace Service. If authorization is absent or revoked, it fails closed; revocation deprograms the tunnel ingress and removes managed DNS.

One binding owns one Gateway and every published hostname. Conflicting later bindings do not mutate Kubernetes or Cloudflare resources. Do not grant tenant users permission to edit providers or the system namespace.

Each `CloudflareTunnelPrivateRoute` claims only the current Service ClusterIP as an exact `/32` route. Cross-namespace Service reads require a matching ReferenceGrant in the Service namespace; KFlared checks the reference authorization before reading the Service. Route ownership is tracked by Cloudflare route identity and Kubernetes owner identity so one resource cannot delete another route. This route is independent of public hostname ingress and does not create public DNS records.

### Workloads and networking

Generated connector Pods:

- run the pinned, official cloudflared image as UID and GID 65532;
- disable service-account token mounting;
- use a read-only root filesystem and runtime-default seccomp;
- drop every Linux capability and disallow privilege escalation;
- mount only the tunnel token Secret.

The default installation does not install a connector NetworkPolicy. Administrators may supply an egress-only policy after validating current Cloudflare requirements. Origins must be normal internal `ClusterIP` Services; KFlared rejects headless, `ExternalName`, `NodePort`, and `LoadBalancer` origins. A dedicated single-port origin Service is recommended because a ReferenceGrant cannot restrict an individual port.

## Accepted limitations

Controller classes select resource ownership; they do not isolate Kubernetes RBAC or Cloudflare account permissions. Instances sharing a system namespace also share its Secret boundary. Use separate namespaces and appropriately scoped account credentials when independent administrative boundaries are required.

Public tunnel origins use HTTP inside the cluster. A private origin Service avoids a public load balancer, but does not itself authenticate users or encrypt the connector-to-origin HTTP hop. Configure application authentication or Cloudflare Access separately and protect cluster networking. Cloudflare API access and tunnel connectivity remain external dependencies.

Readiness describes reconciliation and connector availability. It does not prove that ExternalDNS applied records, that a public application is authorized correctly, or that WARP client access is constrained. Manual DNS cannot be verified by the controller. A `Retain` deletion policy intentionally leaves remote state; administrators must review and remove surviving tunnels, routes, and manual DNS when appropriate.

The route exposes the entire Service IP at the network layer; selecting a Service port does not constrain Cloudflare's CIDR route to that port. Restrict the destination port through Cloudflare One policy and cluster firewall or network policy. For a Kubernetes API Service, allow only the API port needed by clients.

Cloudflare's [CIDR routing guide](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/private-net/cloudflared/connect-cidr/) says RFC1918 addresses are excluded from WARP Split Tunnels by default and that enrolled devices can reach private networks unless Gateway policy restricts them. Operators must include the `/32` in the intended clients' Split Tunnel configuration and order Cloudflare Gateway policy to allow only intended identities and ports, then block other private-network traffic. These controls are external to KFlared and are required to keep the route's access narrow.

## Further reading

The repository's [security policy](https://github.com/kode-blox/kflared/blob/main/SECURITY.md) is the canonical source for supported versions and private vulnerability reporting.

Use [Configuration](/configuration) to configure these boundaries, [Operations](/operations) for deployment and troubleshooting, and [Testing](/testing) for automated coverage and manual integration checks.
