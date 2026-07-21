import { defineConfig } from "vite";
import preact from "@preact/preset-vite";
import tailwindcss from "@tailwindcss/vite";
import { viteSingleFile } from "vite-plugin-singlefile";

// The status server hosts the API and sign-in surface. For `npm run dev`,
// run it locally and point the proxy at it:
//
//   VITE_API_TARGET=http://127.0.0.1:8080 npm run dev
//
// The production build is a single self-contained index.html (viteSingleFile),
// which the Go binary embeds behind the `withui` build tag.
const apiTarget = process.env.VITE_API_TARGET || "http://127.0.0.1:8080";

export default defineConfig({
  plugins: [preact(), tailwindcss(), viteSingleFile()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 4096,
  },
  server: {
    proxy: {
      "/api": { target: apiTarget, changeOrigin: true },
      "/events": { target: apiTarget, changeOrigin: true },
      "/oauth2": { target: apiTarget, changeOrigin: true },
      "/logout": { target: apiTarget, changeOrigin: true },
    },
  },
});
