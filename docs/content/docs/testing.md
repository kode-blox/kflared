---
title: Testing
description: Run KFlared's unit, controller, envtest, chart, and Kind test layers.
---

## Test layers

1. Planner unit tests cover hostname normalization and deterministic tunnel naming.
2. Controller tests use fake Kubernetes and Cloudflare clients to cover provider credential validation, idempotent tunnel and hardened connector creation, deprogramming after eligibility loss, namespace/grant authorization, DNS automation modes, private-route ownership and deletion, and single connector operation.
3. Cloudflare SDK tests use local HTTP test servers to check credential-error classification and private-route API request/response behavior without a live Cloudflare account.
4. Chart synchronization tests verify Helm CRDs and manager RBAC against the authoritative Kustomize distribution.
5. Envtest verifies API defaults, immutable ownership fields, and schema migrations against generated KFlared and Gateway API CRDs. On Windows this test is skipped because controller-runtime cannot reliably terminate envtest control-plane processes; CI runs it on Linux.
6. The Kubebuilder-scaffolded Kind E2E suite verifies that the manager starts under the restricted Pod Security profile and serves authenticated metrics. It does not exercise reconciliation with Traefik, ExternalDNS, or the live Cloudflare API.

## Local verification

Use the repository-local Task wrapper for the supported build and test workflow:

```powershell
./task.ps1 lint-fix
./task.ps1 test
```

Use the Go version declared in `go.mod`. Focused tests can run directly without the Task validation/generation steps:

```sh
go test ./internal/planner ./internal/cloudflare ./internal/controller
go test -race ./...
```

After editing API types or Kubebuilder markers, regenerate and inspect the generated diff:

```powershell
./task.ps1 manifests generate
```

Do not edit generated CRDs, RBAC, or `zz_generated` files by hand.

## End-to-end isolation

Run E2E tests only against a dedicated Kind cluster. The suite installs and deletes cluster-scoped resources and is designed to validate the deployment in an isolated environment similar to CI, not in a development or production cluster.

With Docker available, prepare a dedicated Kind cluster and explicitly select its kubeconfig context before running E2E:

```powershell
./task.ps1 setup-test-e2e KIND_CLUSTER=kflared-test-e2e
kubectl config use-context kind-kflared-test-e2e
kubectl config current-context
./task.ps1 test-e2e KIND_CLUSTER=kflared-test-e2e
```

Confirm the reported context is `kind-kflared-test-e2e`. Setup reuses an existing named cluster without switching context, and the suite's kubectl/install/deploy commands use the current context. The test task deletes the named Kind cluster afterward. Do not use a cluster containing work you need to keep. If choosing another dedicated name, change both `KIND_CLUSTER` arguments and the `kind-<name>` context together.

CI runs `test-unit`, checks formatting, vetting and generated-file cleanliness separately, runs the configured linter, validates and renders the Helm chart, and runs the Kind E2E suite. It also compiles the manager, validates workflow syntax, analyzes Go with CodeQL, and reviews dependency changes on pull requests.

KFlared makes no Gateway API conformance claim in integration mode. Applicable upstream conformance begins only after a native GatewayClass mode exists with an unmodified stock data plane.

## Manual integration gates

Fake clients, envtest, and manager-startup E2E do not prove live-service integration. Before relying on a deployment, operators validate the enabled path with a dedicated account and isolated cluster/client session:

- Confirm the real account token permits the selected tunnel and private-route API operations. Exercise creation, recovery, credential rotation/rejection, and the intended `Delete` or `Retain` behavior.
- For public hostnames, confirm Traefik accepts the Gateway/HTTPRoute, the connector reaches the origin, and ExternalDNS applies the expected records (or the intentional external DNS path works). Verify eligibility or grant revocation removes publication as intended.
- For private routes, confirm the resolved `/32` matches the Service, an authorized enrolled client reaches the destination, and an unauthorized identity or disallowed port is blocked. Kubernetes API access still requires the user's Kubernetes credentials and certificate validation.

These are operator-run checks, not automated release workflow gates. Follow [Installation](/installation), [Operations](/operations), and [Security](/security) for setup and interpretation. Never point destructive cleanup tests at a shared production account or cluster.

## Documentation checks

Use the existing root npm workspace installation. From the repository root:

```sh
npm run lint --workspace @kflared/docs
npm run types:check --workspace @kflared/docs
npm run build --workspace @kflared/docs
```

The static export is in `docs/out`. Check the landing-page entry points and sidebar, page links, `/api/search`, `/llms.txt`, `/llms-full.txt`, and `/llms.mdx/docs/<page>/content.md` after adding or reorganizing pages. [Release process](/release-process) explains which changes select documentation publication.
