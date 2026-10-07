---
title: Release process
description: Follow application and chart version contracts, staged delivery, and documentation publication.
---

KFlared's repository-owned [pipeline](https://github.com/kode-blox/kflared/blob/main/.github/workflows/pipeline.yaml) validates changes, delivers development artifacts, and promotes requested versions to production. Application, chart, and documentation delivery are selected independently from the changed files. The workflows call shared actions for mechanics; the repository defines their ordering and inputs.

## Version contracts

| Authority           | Purpose                        | Published identity                                                                                 |
| ------------------- | ------------------------------ | -------------------------------------------------------------------------------------------------- |
| Root `VERSION`      | Controller application version | Image tag `<version>` and Git tag/release `v<version>`                                             |
| `charts/VERSION`    | Helm chart version             | Chart version and Git tag/release `chart-v<version>`                                               |
| `charts/Chart.yaml` | Chart metadata                 | `version` matches the chart authority; `appVersion` identifies the intended controller application |

Application and chart versions can advance independently. CI invokes version validation before building; keep the version authorities and chart `version` consistent and review `appVersion` for the application you intend to deploy. An application-only release need not publish a new chart. The chart uses `appVersion` as its controller image tag unless `image.tag` overrides it, so set that override when deploying a newer application with a retained chart. A digest-qualified `image.repository` is used without appending a tag. Docker builds embed the root application version and commit in the executable. The documentation workspace's npm version is not the controller release authority.

Development images use `build-<full-commit-SHA>`. Development chart packaging uses the shared action's development mode and sets `appVersion` to that build tag when delivering an application image alongside it. A chart-only delivery retains the committed application metadata.

## Automatic delivery

The pipeline runs for pushes to `main`, pull requests targeting `main`, and manual dispatch. Its normal delivery sequence is:

1. **Validate.** CI checks module metadata, formatting, lint, vet, non-E2E tests, generated artifacts, compilation, workflow syntax, CodeQL, Kind E2E, and chart packaging/rendering. Pull requests also run dependency review. Pull-request and manual pipeline events validate container construction. See [Testing](/testing) for what these gates actually cover.
2. **Deliver development.** A `main` push with application or chart changes publishes the selected development artifacts after CI and updates the development GitOps manifests. A documentation-only change does not request application delivery.
3. **Check release eligibility.** A change to root `VERSION` requests an application release; a change to `charts/VERSION` requests a chart release. After development delivery, the workflow checks the corresponding `v<version>` or `chart-v<version>` tag. A tag already pointing at this commit needs no new automatic release; reuse of a version at another commit fails.
4. **Deliver production.** The application image is promoted from the development image digest to the plain version tag without rebuilding. Production charts are packaged from the committed metadata. Selected production artifacts then update production GitOps manifests.
5. **Create records.** After production delivery succeeds, the workflow creates pending Git tags and GitHub Releases. Application release notes exclude chart changes and the application release becomes latest; chart release notes cover chart changes and do not become latest.

Changes to an API type or marker also require regenerated CRDs, DeepCopy methods, and matching chart CRDs/RBAC as appropriate. Generation and chart synchronization tests detect drift. Publishing a new controller image does not upgrade a cluster's stored CRDs: follow the ordered [installation migrations](/installation#upgrading-existing-installations).

## Deployment boundary

The [deployment workflow](https://github.com/kode-blox/kflared/blob/main/.github/workflows/deploy.yaml) selects the development or production GitHub Environment and uses a scoped GitHub App token to update `SayakMukhopadhyay/k8s-landscape-charts` through the shared chart-update action. Delivery ends at the GitOps update. These workflows do not wait for Argo CD synchronization or Kubernetes rollout health. Environment approval requirements are repository settings, not established by workflow source.

Operators must verify the resulting controller image, CRD compatibility, resource conditions, and real traffic after their GitOps system reconciles. Use [Operations](/operations) and the [manual integration gates](/testing#manual-integration-gates); successful artifact publication does not establish Cloudflare permissions, DNS propagation, or client-policy correctness.

## Manual version and delivery workflows

- **Bump Version** runs on `main` and accepts application, chart, or both, with patch/minor/major increments. It updates version metadata through the shared version action; the normal pipeline handles subsequent delivery.
- **Manual Development Delivery** runs CI and lets operators select application/chart publication and whether to update GitOps manifests.
- **Manual Production Delivery** validates versions and tag compatibility, then promotes selected artifacts and creates release records. Application promotion requires an existing `build-<selected-commit-SHA>` image. It does not rerun CI, so choose a commit already validated and delivered to development. Skipping deployment skips the GitOps update, while release records are still created.

These dispatches publish artifacts and may update external GitOps state. Select the intended commit, version, and deployment options deliberately; a missing development image must be delivered before application promotion can proceed.

## Documentation delivery

Changes under `docs/`, the root npm manifests/lockfile, `.nvmrc`, or the documentation workflow select the separate [documentation delivery workflow](https://github.com/kode-blox/kflared/blob/main/.github/workflows/deliver-documentation.yaml). It runs documentation lint, type checks, a production export, and static-site validation independently of application CI. Pull requests validate the export; pushes and manual pipeline dispatches on `main` upload it and deploy GitHub Pages at `https://kflared.kodeblox.com`.

Run the same lint, type, and build commands from the repository root before submitting authored changes; they are listed in [Testing](/testing#documentation-checks). Review sidebar order, internal links, search, and generated Markdown along with the visible page content.
