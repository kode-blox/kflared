# Security model

## Credentials

`CloudflareProvider.spec.apiTokenSecretRef` names one Secret and key in `kflared-system`. The reference cannot select another namespace. The manager's ClusterRole has no Secret access; a namespace Role grants only the operations needed in the system namespace. Sensitive resource reads bypass the shared cache so the manager does not require a cluster-wide Secret informer.

Use one least-privilege API token per operational trust boundary. The MVP needs account-scoped Cloudflare Tunnel/Connector write access and no DNS permission. Rotate the value in place; the provider's periodic credential check and binding reconciliation will observe it.

Connector tokens are written to an operator-generated Secret, mounted read-only, and passed to official cloudflared through `--token-file`. They are never placed in arguments, environment variables, Events, or status.

## Tenancy

Providers are cluster-scoped administrator resources. Every provider requires an explicit DNS suffix allow-list and a namespace label selector. `{}` intentionally allows every namespace. Bindings, Gateways, routes, and origin Services are same-namespace in the MVP.

One binding owns one Gateway and every published hostname. Conflicting later bindings do not mutate Kubernetes or Cloudflare resources. Do not grant tenant users permission to edit providers or the system namespace.

## Workloads and networking

Generated connector Pods:

- run the pinned, official cloudflared image as UID/GID 65532;
- disable service-account token mounting;
- use a read-only root filesystem and runtime-default seccomp;
- drop every Linux capability and disallow privilege escalation;
- mount only the tunnel token Secret.

The project does not install a NetworkPolicy because Cloudflare connector endpoints and cluster DNS requirements can evolve. Administrators may supply an egress-only policy after validating current Cloudflare requirements. The operator cannot make an existing LoadBalancer or NodePort Traefik Service private and emits a warning when one is selected.

## Security policy

Please report suspected vulnerabilities privately through GitHub's security advisory feature for `kode-blox/golfs`. Do not include tokens, signed URLs, private keys, credentials, or object data in a public issue.

Only the latest released minor version receives security fixes during the v0.x period. During the initial implementation phase, provider validation and GitHub App/GCM validation are manual release responsibilities because automated integration testing is deferred.
