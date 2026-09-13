# Testing strategy

## Local layers

1. Planner unit tests cover hostname normalization and deterministic tunnel naming.
2. Controller tests use fake Kubernetes and Cloudflare clients to cover provider credential validation, idempotent tunnel and hardened connector creation, deprogramming after eligibility loss, and the minimum connector replica count.
3. A chart synchronization test verifies that the Helm CRDs exactly match the generated Kustomize CRDs.
4. Envtest verifies API defaults and immutable ownership fields against the generated KFlared and Gateway API CRDs. On Windows this test is skipped because controller-runtime cannot reliably terminate envtest control-plane processes there; CI runs it on Linux.
5. The Kubebuilder-scaffolded Kind E2E suite verifies that the manager starts under the restricted Pod Security profile and serves authenticated metrics. It does not yet exercise KFlared reconciliation with Traefik, ExternalDNS, or the live Cloudflare API.

Local commands must use the already installed Go 1.27.0 executable on the development workstation:

```sh
go1.27.0 test ./internal/planner ./internal/controller
go1.27.0 test -race ./...
```

CI runs `make test`, verifies generated-file cleanliness, runs the configured linter, validates and renders the Helm chart, and runs the scaffolded Kind E2E suite.

No Gateway API conformance claim is made for integration mode. Applicable upstream conformance begins only after a native GatewayClass mode exists with an unmodified stock data plane.
