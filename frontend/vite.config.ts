import { fileURLToPath } from "node:url";

import { loadEnv } from "vite";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// https://vitejs.dev/config/
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");

  return {
    plugins: [
      react(),
      {
        name: "release-identity",
        transformIndexHtml() {
          return [
            { tag: "meta", attrs: { name: "yatm-version", content: process.env.VITE_YATM_VERSION || "development" }, injectTo: "head" },
            { tag: "meta", attrs: { name: "yatm-commit", content: process.env.VITE_YATM_COMMIT || "unknown" }, injectTo: "head" },
          ];
        },
      },
    ],
    resolve: {
      alias: {
        "@": fileURLToPath(new URL("./src", import.meta.url)),
      },
    },
    server: {
      proxy: {
        // target http://localhost:5173
        "/services": env.DEV_SERVICE_BASE,
        "/files": env.DEV_SERVICE_BASE,
      },
    },
    test: {
      environment: "jsdom",
      setupFiles: ["./src/test/setup.ts"],
    },
  };
});
