---
title: Testing
description: Run KFlared's unit, controller, envtest, chart, and Kind test layers.
---

## Test layers

1. Planner unit tests cover hostname normalization and deterministic tunnel naming.
2. Controller tests use fake Kubernetes and Cloudflare clients to cover provider credential validation, idempotent tunnel and hardened connector creation, deprogramming after eligibility loss, and the minimum connector replica count.
3. A chart synchronization test verifies that Helm CRDs exactly match the generated Kustomize CRDs.
4. Envtest verifies API defaults and immutable ownership fields against the generated KFlared and Gateway API CRDs. On Windows this test is skipped because controller-runtime cannot reliably terminate envtest control-plane processes; CI runs it on Linux.
5. The Kubebuilder-scaffolded Kind E2E suite verifies that the manager starts under the restricted Pod Security profile and serves authenticated metrics. It does not yet exercise reconciliation with Traefik, ExternalDNS, or the live Cloudflare API.

## Local verification

Use the repository-local Task wrapper for the supported build and test workflow:

```powershell
./task.ps1 lint-fix
./task.ps1 test
```

On the development workstation, the installed Go 1.27.0 executable can also run focused tests directly:

```sh
go1.27.0 test ./internal/planner ./internal/controller
go1.27.0 test -race ./...
```

After editing API types or Kubebuilder markers, regenerate and inspect the generated diff:

```powershell
./task.ps1 manifests generate
```

Do not edit generated CRDs, RBAC, or `zz_generated` files by hand.

## End-to-end isolation

Run E2E tests only against a dedicated Kind cluster. The suite installs and deletes cluster-scoped resources and is designed to validate the deployment in an isolated environment similar to CI, not in a development or production cluster.

CI runs the test task, checks generated-file cleanliness, runs the configured linter, validates and renders the Helm chart, and runs the Kind E2E suite.

KFlared makes no Gateway API conformance claim in integration mode. Applicable upstream conformance begins only after a native GatewayClass mode exists with an unmodified stock data plane.
