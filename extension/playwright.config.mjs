import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./test",
  testMatch: "*.spec.mjs",
  timeout: 10_000,
  workers: 1,
  use: {
    headless: true,
  },
});
