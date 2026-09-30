import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./tests",
  testMatch: "**/*.spec.ts",
  timeout: 30_000,
  use: { baseURL: "http://127.0.0.1:4187", trace: "retain-on-failure" },
  webServer: {
    command: "npm run dev -- --port 4187",
    url: "http://127.0.0.1:4187",
    reuseExistingServer: true
  },
  projects: [
    { name: "desktop", use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 1000 } } },
    { name: "mobile", use: { ...devices["Pixel 7"] } }
  ]
});
