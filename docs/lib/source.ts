import { llms, loader } from "fumadocs-core/source";
import { defineDocs } from "fumadocs-mdx/macro";
import { metaSchema, pageSchema } from "fumadocs-core/source/schema";
import { docsRoute } from "./shared";

const docs = defineDocs({
  dir: "content/docs",
  docs: {
    async: true,
    schema: pageSchema,
    postprocess: {
      includeProcessedMarkdown: true,
    },
  },
  meta: {
    schema: metaSchema,
  },
});

// See https://fumadocs.dev/docs/headless/source-api for more info
export const source = loader({
  baseUrl: docsRoute,
  source: docs.toFumadocsSource(),
  plugins: [],
});

export const docsLlms = llms(source, {
  renderPage: async (page) => `# ${page.data.title} (${page.url})

${await page.data.getText("processed")}`,
});
