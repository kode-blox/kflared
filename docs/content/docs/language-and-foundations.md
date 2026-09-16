---
title: Language and foundations
description: Why KFlared uses Go, Kubebuilder, controller-runtime, Gateway API, and the official Cloudflare SDK.
---

KFlared uses Go 1.27.0, Kubebuilder `go/v4`, controller-runtime, Gateway API v1.6.1, and the official `cloudflare-go/v7` SDK.

## Go and controller-runtime

Go and controller-runtime provide the standard typed API, reconciliation, status, finalizer, workqueue, leader-election, RBAC marker, envtest, and generation toolchain expected of a Kubernetes controller. Using another language would require rebuilding substantial operator infrastructure without improving the controller's traffic or security model.

Kubebuilder owns the project scaffold and generated API artifacts. Keep scaffold markers in place, use Kubebuilder commands when adding APIs or webhooks, and regenerate manifests and DeepCopy methods through the repository-local Task wrapper after changing API types or markers.

## Gateway API and Traefik

Gateway API supplies the portable routing resource model, while Traefik remains the implementation that evaluates route semantics and proxies traffic. KFlared deliberately reads accepted Gateway and HTTPRoute status instead of becoming another GatewayClass implementation or duplicating Traefik's data plane.

This separation lets KFlared focus on its integration responsibility: translating eligible hostnames into a remote Cloudflare Tunnel configuration and operating the corresponding connectors.

## Cloudflare client and connector

The controller uses the official `cloudflare-go/v7` SDK for Cloudflare API operations. Official cloudflared remains a separate workload deployed from its pinned image. KFlared does not fork cloudflared, embed a proxy, or place tunnel credentials in the manager process environment.

The combination preserves upstream behavior and security fixes while keeping the controller's responsibilities narrow and testable.
