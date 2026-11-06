import { defineConfig, loadEnv, type HtmlTagDescriptor, type Plugin } from 'vite';

// Arena Sold Out organizer storefront - a static Vite + vanilla TypeScript SPA.
// Dawn's own CSS is imported from src/theme (see src/main.ts) and its custom
// elements (header drawer, search modal, quantity input, slider, gallery) are
// served untouched from public/theme and loaded by index.html.

/**
 * The API origin in index.html, so the first requests do not wait for the bundle:
 * <meta name="arena-api-base"> for public/boot.js, which asks for the shop right away,
 * and two preconnects (the CORS fetches and the product pictures, which are plain image
 * requests and use their own connection). Nothing is added without VITE_API_BASE_URL
 * (same-origin dev and tests).
 */
function apiOriginTags(apiBase: string): Plugin {
  return {
    name: 'arena-api-origin',
    transformIndexHtml() {
      const base = apiBase.replace(/\/+$/, '');
      const tags: HtmlTagDescriptor[] = [
        { tag: 'meta', attrs: { name: 'arena-api-base', content: base }, injectTo: 'head-prepend' },
      ];
      if (/^https?:\/\//.test(base)) {
        const origin = new URL(base).origin;
        tags.push(
          { tag: 'link', attrs: { rel: 'preconnect', href: origin, crossorigin: '' }, injectTo: 'head-prepend' },
          { tag: 'link', attrs: { rel: 'preconnect', href: origin }, injectTo: 'head-prepend' },
        );
      }
      return tags;
    },
  };
}

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), 'VITE_');
  return {
    plugins: [apiOriginTags(env.VITE_API_BASE_URL ?? '')],
    build: {
      outDir: 'dist',
      sourcemap: false,
    },
  };
});
