---
title: Language and foundations
description: How Go, Kubebuilder, controller-runtime, Gateway API, and the Cloudflare SDK support KFlared.
---

KFlared uses Go 1.27.0, Kubebuilder `go/v4`, controller-runtime, Gateway API v1.6.1, and the official `cloudflare-go/v7` SDK.

## Go and controller-runtime

Go and controller-runtime provide the standard typed API, reconciliation, status, finalizer, workqueue, leader-election, RBAC marker, envtest, and generation toolchain expected of a Kubernetes controller.

Kubebuilder owns the project scaffold and generated API artifacts. Keep scaffold markers in place, use Kubebuilder commands when adding APIs or webhooks, and regenerate manifests and DeepCopy methods through the repository-local Task wrapper after changing API types or markers.

## Gateway API and Traefik

Gateway API supplies the portable routing resource model, while Traefik remains the implementation that evaluates route semantics and proxies traffic. KFlared deliberately reads accepted Gateway and HTTPRoute status instead of becoming another GatewayClass implementation or duplicating Traefik's data plane.

This separation lets KFlared translate eligible hostnames into remote tunnel configuration without interpreting Traefik route rules. Private Service routes use a separate Cloudflare CIDR API and controller; they do not evaluate HTTPRoute semantics.

## Cloudflare client and connector

The controller uses the official `cloudflare-go/v7` SDK for Cloudflare API operations. Official cloudflared remains a separate workload deployed from its pinned image. KFlared does not fork cloudflared, embed a proxy, or place tunnel credentials in the manager process environment.

The SDK handles Cloudflare API calls; cloudflared carries traffic in separate Pods. Updating these dependencies and the connector image remains a release responsibility. See [Architecture](/architecture) for component boundaries, [Testing](/testing) for verification, and [Release process](/release-process) for delivery.
