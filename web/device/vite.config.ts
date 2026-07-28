import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const projectDir = dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  plugins: [react()],
  base: "./",
  resolve: {
    alias: {
      "@": resolve(projectDir, "../admin/src")
    }
  },
  build: {
    outDir: "../../internal/bridge/device_dist",
    emptyOutDir: true,
    assetsDir: "device-assets"
  }
});
