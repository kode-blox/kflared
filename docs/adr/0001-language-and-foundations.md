# ADR 0001: Go and upstream controller foundations

Status: accepted

Use Go 1.27.0, Kubebuilder v4.15.0 `go/v4`, controller-runtime v0.24.1, Gateway API v1.6.1, and the official `cloudflare-go/v7` v7.8.0 SDK.

Go/controller-runtime provides the standard typed API, reconciliation, status, finalizer, workqueue, leader-election, RBAC marker, envtest, and generation toolchain expected of a Kubernetes controller. Rust or another language would require rebuilding substantial operator infrastructure without improving the MVP's traffic or security model.

The existing `go1.27.0` executable is used directly during local scaffolding and verification. The project does not install Go, change the default `go`, set `GOTOOLCHAIN`, or add a `toolchain` directive.

Official cloudflared remains a separate workload. The project will not fork it or embed a proxy implementation.
