---
title: Operations
description: Observe KFlared, manage DNS, and understand deletion and failure behavior.
---

KFlared uses standard Kubernetes status conditions and owner references. Start every operational check with the provider, binding, and generated connector resources rather than with Cloudflare alone.

## Observe provider and binding health

```sh
kubectl get clustercloudflareproviders
kubectl -n my-app get cloudflaretunnelbindings
kubectl -n my-app get cloudflaretunnelbinding public -o yaml
```

A healthy provider reports `Accepted=True` and `CredentialsValid=True`. A fully automated binding reports all five conditions as true: `Accepted`, `Programmed`, `ConnectorReady`, `DNSAutomationReady`, and `Ready`.

The binding's `status.resources` lists the connector Deployment, PodDisruptionBudget, token Secret, and optional `DNSEndpoint` names without exposing credentials. `status.publishedHostnames` and `status.dnsRecords` show the exact public contract currently derived from accepted HTTPRoutes.

## DNS modes

When the ExternalDNS `DNSEndpoint` CRD is discoverable, KFlared owns a `DNSEndpoint` containing CNAMEs to `<tunnel-id>.cfargotunnel.com`. ExternalDNS remains responsible for applying those records.

Without the CRD, KFlared reports every required CNAME in `status.dnsRecords` and sets:

```text
DNSAutomationReady=False
Reason=ManualConfigurationRequired
```

Apply those records with your DNS management system. The MVP cannot acknowledge or verify manual DNS, so `Ready` remains false even when traffic works.

## Route eligibility

If a tunnel is deprogrammed or contains no hostnames, verify that:

1. the selected Gateway listener reports current `Accepted=True` and `Programmed=True`;
2. each HTTPRoute has a parent status for that Gateway and listener with current `Accepted=True` and `ResolvedRefs=True`;
3. hostnames are concrete rather than wildcard values;
4. hostnames fall within the provider's `allowedDNSZones`;
5. the binding namespace matches the provider selector;
6. no older binding already owns the Gateway or hostname.

When no eligible hostname remains, KFlared replaces remote ingress rules with the mandatory `http_status:404` catch-all and removes managed DNS automation.

## Cross-namespace origin authorization

For a cross-namespace `originServiceRef`, create a Gateway API `ReferenceGrant` in the target Service namespace. It must allow `kflared.kodeblox.com` `CloudflareTunnelBinding` objects from the binding namespace to reference core `Service` objects; when `to.name` is present, it must match the Service name. A grant without `to.name` authorizes any Service in that namespace for that source namespace and kind. KFlared checks grants during reconciliation, and grant changes trigger affected bindings to reconcile. If a grant is removed, the binding fails closed, tunnel ingress is deprogrammed, and managed DNS is removed. Check the binding's `Accepted` condition for the authorization failure reason.

Use a dedicated single-port internal `ClusterIP` origin Service. Grants cannot constrain the referenced port, and KFlared rejects headless, `ExternalName`, `NodePort`, and `LoadBalancer` Services. The grant only permits KFlared's reference; it does not configure cluster networking or allow packet flow by itself.

When upgrading from a v1alpha1 release that used `spec.gatewayServiceRef`, update manifests to `spec.originServiceRef`; the alpha rename has no legacy-field alias. If stored bindings exist, pause the old controller, install the new CRD, apply the rewritten bindings, and only then start the new controller. This avoids either controller observing a field it does not understand.

## Deletion

`spec.deletionPolicy` controls the remote tunnel only:

- `Delete` removes managed DNS and connector resources, then deletes the remote tunnel.
- `Retain` removes managed Kubernetes resources but leaves the remote tunnel and its remote configuration intact.

Manual DNS cannot be removed by KFlared. Remove surviving records deliberately; a CNAME that points to a deleted tunnel can produce Cloudflare error 1016.

## Logs

```sh
kubectl logs -n kflared \
  deployment/kflared-controller-manager \
  -c manager \
  --follow
```

Use the actual Helm release name if it changes the Deployment prefix. Reconciliation errors use controller-runtime backoff; stable validation failures are represented in conditions and periodically rechecked without error storms.
