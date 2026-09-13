# ADR 0004: Credential and tenancy boundaries

Status: accepted

Cloudflare credentials are administrator-managed Secrets in `kflared-system`. A provider may reference only a name and key there. The ClusterRole intentionally has no Secret, Deployment, or PodDisruptionBudget access; a namespace Role holds those permissions, and sensitive reads bypass the shared cache.

Providers combine an explicit concrete DNS-zone allow-list with a required namespace selector. Binding references to Gateway and Service omit namespace fields, structurally enforcing same-namespace integration. Traefik may independently route to cross-namespace backends after validating ReferenceGrant; the controller trusts its current `ResolvedRefs` status.

Connector Pods consume tunnel identity through a read-only token file and expose no Kubernetes API identity.
