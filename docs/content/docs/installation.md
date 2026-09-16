---
title: Installation
description: Install KFlared with Helm or Kustomize and prepare its cluster prerequisites.
---

KFlared publishes hostnames from a Traefik-managed Kubernetes Gateway through a controller-owned, remotely managed Cloudflare Tunnel. Install its prerequisites before installing the controller.

## Prerequisites

- Kubernetes 1.35 through 1.37.
- Gateway API v1.6.1 CRDs.
- Traefik 3.7.10 or newer with the Kubernetes Gateway provider enabled.
- An internal, non-headless Traefik Service.
- A Cloudflare account API token with Tunnel and Connector write access for the selected account.
- Optional: ExternalDNS and its `externaldns.k8s.io/v1alpha1` `DNSEndpoint` CRD.

The Cloudflare token does not need DNS edit permission. ExternalDNS performs DNS changes when its CRD is installed.

## Install with Helm

The supported chart is rooted at `charts/`:

```sh
helm upgrade --install kflared ./charts \
  --namespace kflared-system \
  --create-namespace
```

KFlared CRDs live in Helm's special `crds/` directory. Helm installs them before templates and deliberately does not upgrade or delete them. Use `--skip-crds` only when a cluster administrator manages the CRDs separately.

The chart intentionally combines Helm's conventional values and template structure with Kubebuilder's authoritative controller resource inventory. It does not include generic application templates such as an Ingress, HTTPRoute, HPA, or test Pod. Generated CRD schemas and RBAC remain sourced from Kustomize, with tests detecting distribution drift.

To inspect the complete rendered chart, include the otherwise omitted CRDs:

```sh
helm template kflared ./charts \
  --namespace kflared-system \
  --include-crds
```

## Install with Kustomize

Kustomize is the canonical source for generated manifests and is available through the repository-local Task wrapper:

```powershell
./task.ps1 install
./task.ps1 deploy IMG=<registry>/kflared:<tag>
```

Task is pinned in `tools/task` and does not need a global installation. Run `./task.ps1 --list` to inspect all available tasks.

## Create the API token Secret

Create the administrator-managed Secret in the controller namespace:

```sh
kubectl -n kflared-system create secret generic cloudflare-api-token \
  --from-literal=api-token='<CLOUDFLARE_API_TOKEN>'
```

The controller has no cluster-wide Secret permission. A namespace-scoped Role allows it to read credentials and manage connector resources only in its system namespace.

## Create a provider and binding

Apply a provider, authorize a tenant namespace with the provider's selector, then apply a binding in that namespace:

```sh
kubectl apply -f config/samples/kflared_v1alpha1_cloudflareprovider.yaml
kubectl label namespace my-app kflared.kodeblox.com/cloudflare-provider=default
kubectl -n my-app apply -f config/samples/kflared_v1alpha1_cloudflaretunnelbinding.yaml
```

Adapt the samples to your account ID, DNS zones, Gateway listener, and internal Traefik Service before applying them. Continue with [Configuration](configuration.md) for the complete resource contract.
