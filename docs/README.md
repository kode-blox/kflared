# KFlared documentation site

The documentation website uses the official `fumadocs-mdx` content source, Fumadocs Core and UI, and a statically exported Next.js application. Authored documentation lives in `content/docs`.

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
