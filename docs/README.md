# KFlared documentation site

Published readers start at [Installation](https://kflared.kodeblox.com/installation), then [Configuration](https://kflared.kodeblox.com/configuration). The authored sidebar follows three groups:

- **Get started:** Installation, Configuration.
- **Understand the system:** Architecture, Security, Language and foundations.
- **Operate and develop:** Operations, Testing, Release process.

Keep project-specific setup and upgrade details in the relevant pages, with links into the next task. Website Security documents technical boundaries and links to root [SECURITY.md](../SECURITY.md), which owns supported versions and private reporting policy.

The canonical authored files are `content/docs/security.md` (title **Security**, route `/security`) and `content/docs/release-process.md` (title **Release process**, route `/release-process`). Page filenames and canonical routes must reflect their titles; do not add duplicate pages, compatibility stubs, or redirects for older names.

Use this exact shared Security outline, retaining verified KFlared-specific content under each heading:

```markdown
## Protected assets

## Trust boundaries

## Controls

### Credentials and authorization

### Isolation and integrity

### Workloads and networking

## Accepted limitations

## Further reading
```

Protected assets is a bullet list. Trust boundaries is a table with columns `Boundary` and `Security requirement`. Controls documents enforced behavior; accepted limitations describes actual risks and external responsibilities. Further reading links to the canonical repository policy and Configuration, Operations, and Testing. Keep reporting/support policy in root `SECURITY.md` and automated coverage/manual integration discussion in Testing.

The documentation website uses the official `fumadocs-mdx` content source, Fumadocs Core and UI, and a statically exported Next.js application. Authored documentation lives in `content/docs`. Collections are defined through the MDX Macro API in `lib/source.ts`, with lazy bodies and processed Markdown enabled. No separate collection-codegen step or `collections/*` alias is needed.

`fumadocs-ui` aliases `@fumadocs/base-ui`; search uses the static Fumadocs client. Shared Markdown and Open Graph URL helpers live in `lib/shared.ts`.

The landing page and documentation pages use Fumadocs layouts, documentation pages expose copy and source-view controls, and the static export includes search, Open Graph images, and machine-readable Markdown routes.

From the repository root:

```shell
npm ci
npm run dev --workspace @kflared/docs
```

The local site is available at `http://localhost:3000`. Validate the production export with:

```shell
npm run lint --workspace @kflared/docs
npm run types:check --workspace @kflared/docs
npm run build --workspace @kflared/docs
```

The production site is designed to be served from the root of `https://kflared.kodeblox.com/`.

The production export includes these machine-readable entry points:

- `llms.txt` for the documentation index.
- `llms-full.txt` for the complete documentation corpus.
- `llms.mdx/docs/<page>/content.md` for an individual page.

When adding a page, update `content/docs/meta.json` and check landing-page links, sidebar order, generated search, and Markdown exports. See the authored [Testing](content/docs/testing.md#documentation-checks) and [Release process](content/docs/release-process.md#documentation-delivery) pages for checks and publication behavior.
