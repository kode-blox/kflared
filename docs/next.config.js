import { createMDX } from "fumadocs-mdx/next";
import { PHASE_DEVELOPMENT_SERVER } from "next/constants.js";

const withMDX = createMDX();

export default function config(phase) {
  const isDevelopmentServer = phase === PHASE_DEVELOPMENT_SERVER;

  return withMDX({
    reactStrictMode: true,
    ...(isDevelopmentServer ? {} : { output: "export" }),
  });
}
