# ADR 0006: Hybrid Helm chart composition

Status: accepted

The supported chart is rooted directly at `charts/`, matching the current GOLFS repository convention.

Use the current `helm create` output as the structural baseline for chart metadata, values, schema, helpers, flat template naming, and conventional overrides. Use Kubebuilder's Kustomize-derived chart as the authoritative inventory for controller resources: CRDs, manager Deployment, ServiceAccount, scoped controller and connector RBAC, leader election, metrics, and optional monitoring.

Place CRDs as plain YAML in Helm's special root `crds/` directory, not beneath `templates/`. This selects Helm's install-before-templates behavior and conservative Helm lifecycle: Helm does not template, upgrade, or delete these files. Argo CD includes them by default and owns their apply-based GitOps lifecycle unless `spec.source.helm.skipCrds` is enabled. Add the fixed `helm.sh/resource-policy: keep` annotation so both Helm and Argo CD retain CRDs during application removal.

Do not carry generic application templates such as Service, Ingress, HTTPRoute, HPA, or test Pod into this controller chart. Do not carry Kubebuilder's generated NetworkPolicy template; administrators own egress policy in the MVP.

Every adopted namespaced or RBAC controller manifest is rewritten as a first-class Helm template rather than retained as generator-shaped output. CRDs remain static, generator-shaped manifests except for their fixed retention annotation. Kustomize remains authoritative for generated CRD schemas and RBAC permissions, and tests detect CRD drift between distributions.
