# ADR 0003: Deterministic exclusive ownership

Status: accepted

Each binding exclusively owns one remotely managed tunnel, its complete ingress configuration, connector Deployment, PodDisruptionBudget, token Secret, and optional DNSEndpoint. Shared tunnels, imported tunnels, and external connectors are deferred.

Only one binding may own a Gateway or public hostname. The oldest binding wins; equal timestamps are ordered by namespace, name, and UID. Losers report `Accepted=False` and perform no creates or updates. Previously programmed losers are first reduced to the mandatory `http_status:404` rule and their managed DNS automation is removed.

The finalizer is installed before external creation. Reconciliation is compare-before-write and canonical ordering makes it idempotent. The tunnel ID is persisted in status; exact deterministic naming containing the full binding UID recovers a lost creation response. Adoption requires both the exact identity and Cloudflare remote-management mode, never a human-readable name alone.
