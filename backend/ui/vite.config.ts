/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

// Dev proxies /v1 and /config to a locally running lcatd so the SPA and the
// API share an origin in development exactly as they do embedded in the
// binary. The build lands in ./dist, which backend/ui/ui.go go:embeds.
export default defineConfig(({ mode }) => ({
  plugins: [svelte()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/v1": "http://localhost:8471",
      "/config": "http://localhost:8471",
    },
  },
  // Vitest runs Svelte's client runtime in jsdom, so module resolution must
  // pick the browser condition there.
  resolve: mode === "test" ? { conditions: ["browser"] } : undefined,
  test: {
    environment: "jsdom",
    // Vitest stubs CSS imports to "" by default, and the stub wins even over an
    // explicit `?raw` query. contrast.test.ts reads app.css as text to check the
    // palette's WCAG ratios (tasks/315), so that one file is processed for real.
    // Scoped rather than global: no component test wants its <style> evaluated.
    // (The pattern is matched against the module id, which carries the `?raw`
    // query, so it cannot be anchored at the extension.)
    css: { include: [/app\.css/] },
  },
}));
