// @ts-check
import { defineConfig } from "astro/config";

// Deployment target is still open. A GitHub project-pages deploy needs
// `base: "/flowbench"` here and the same path on SITE.url in src/content/site.ts.
export default defineConfig({
  site: "https://blackprince001.github.io",
});
