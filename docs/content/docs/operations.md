---
title: Operations
description: Observe KFlared, manage DNS, and understand deletion and failure behavior.
---

KFlared uses standard Kubernetes status conditions and owner references. Start every operational check with the provider, binding, and generated connector resources rather than with Cloudflare alone.

For private network routes, inspect the route and its referenced Service:

```sh
kubectl -n kflared get cloudflaretunnelprivateroutes
kubectl -n kflared get cloudflaretunnelprivateroute kubernetes-api -o yaml
kubectl -n default get service kubernetes -o wide
```

Private-route status reports its Cloudflare route ID, resolved `/32` network, tunnel ID, generated resource names, and readiness conditions. Confirm that the network matches the Service's current ClusterIP. If a cross-namespace reference is rejected, verify the `ReferenceGrant` source kind and namespace in the Service namespace. Cloudflare One enrollment and access policy remain administrator-managed prerequisites. Follow Cloudflare's [CIDR route client setup](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/private-net/cloudflared/connect-cidr/): include the route in client Split Tunnels and apply explicit allow policies for intended identities and destination ports, followed by a catch-all private-network block. In particular, avoid leaving all enrolled devices with unrestricted access to the cluster Service IP.

For local `kubectl`, copy an authorized kubeconfig and set its cluster `server` to `https://<Service-ClusterIP>:<Service-port>`. Keep its Kubernetes user credentials and trusted certificate authority. If the API server certificate does not contain the ClusterIP as a subject alternative name, set kubeconfig `tls-server-name` to a DNS name that the certificate does contain; Kubernetes [defines that field for certificate validation](https://kubernetes.io/docs/reference/config-api/kubeconfig.v1/). Do not disable TLS verification. If the Service ClusterIP changes, update the kubeconfig server address after the route reports its new `/32`. Finally, use the enrolled WARP client and run a read-only `kubectl get --raw=/readyz` before normal commands. KFlared does not create or distribute kubeconfigs.

If the condition reason is `RouteConflict`, check whether an older route resource under the same provider already targets that `/32`. KFlared preserves that existing owner and does not take over a Cloudflare route whose route ID/comment does not match the resource's recorded ownership.

After installing a release that introduces this feature, operators must apply the updated CRD and controller image through their normal release process, then create the provider and `CloudflareTunnelPrivateRoute`. For the built-in Kubernetes API Service, adapt the Service namespace and port to the target cluster. Validate route status and client access in an isolated client session before relying on the route. These are operator-run rollout steps; KFlared does not mutate a live cluster or Cloudflare account during installation.

## Observe provider and binding health

```sh
kubectl get clustercloudflareproviders
kubectl -n my-app get cloudflaretunnelbindings
kubectl -n my-app get cloudflaretunnelbinding public -o yaml
```

A healthy provider reports `Accepted=True` and `CredentialsValid=True`. A healthy binding reports all five conditions as true: `Accepted`, `Programmed`, `ConnectorReady`, `DNSAutomationReady`, and `Ready`. `DNSAutomationReady=True` can mean either that the owned `DNSEndpoint` exists or that DNS automation was intentionally disabled.

The binding's `status.resources` lists the connector Deployment, PodDisruptionBudget, token Secret, and optional `DNSEndpoint` names without exposing credentials. `status.publishedHostnames` shows the hostnames programmed into the tunnel. When DNS automation is enabled, `status.dnsRecords` shows the exact public DNS contract derived from those HTTPRoutes.

## DNS modes

`spec.dnsAutomationEnabled` defaults to `true`. When enabled and the ExternalDNS `DNSEndpoint` CRD is discoverable, KFlared owns a `DNSEndpoint` containing Cloudflare-proxied CNAMEs to `<tunnel-id>.cfargotunnel.com`. ExternalDNS remains responsible for applying those records.

Without the CRD, KFlared reports every required CNAME in `status.dnsRecords` and sets:

```text
DNSAutomationReady=False
Reason=ManualConfigurationRequired
```

Apply those records with your DNS management system. The MVP cannot acknowledge or verify manual DNS, so `Ready` remains false even when traffic works.

Set `spec.dnsAutomationEnabled: false` when the public hostname must retain another DNS target. KFlared removes any binding-owned `DNSEndpoint`, clears `status.dnsRecords` and `status.resources.dnsEndpoint`, and reports `DNSAutomationReady=True` with reason `DNSAutomationDisabled`. Tunnel ingress and connectors continue to reconcile, so `Ready` can become true without managed public DNS. This mode is suitable when another Cloudflare edge component, such as a Worker using Workers VPC, reaches the tunnel while public DNS points elsewhere.

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
