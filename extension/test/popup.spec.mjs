import { expect, test } from "@playwright/test";
import { readFile } from "node:fs/promises";

const assets = new Map([
  ["/popup.html", new URL("../popup.html", import.meta.url)],
  ["/popup.css", new URL("../popup.css", import.meta.url)],
  ["/popup.js", new URL("../popup.js", import.meta.url)],
]);

async function routePopup(page) {
  await page.route("https://extension.fixture/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const file = assets.get(path);
    if (!file) return route.fulfill({ status: 404, body: "not found" });
    const contentType = path.endsWith(".js")
      ? "text/javascript"
      : path.endsWith(".css")
        ? "text/css"
        : "text/html";
    await route.fulfill({ status: 200, contentType, body: await readFile(file) });
  });
}

test("popup loads Chrome storage and persists master, level, and site controls", async ({ page }) => {
  await page.addInitScript(() => {
    const store = { enabled: true, level: "full", sites: {} };
    window.__cavemanPopupStore = store;
    window.chrome = {
      runtime: { id: "fixture-extension-id" },
      storage: {
        sync: {
          get(defaults, callback) {
            const value = {};
            for (const key in defaults) value[key] = key in store ? store[key] : defaults[key];
            queueMicrotask(() => callback(value));
          },
          set(value, callback) {
            Object.assign(store, value);
            queueMicrotask(() => callback?.());
          },
        },
      },
    };
  });
  await routePopup(page);
  await page.goto("https://extension.fixture/popup.html");

  await expect(page.locator("#master")).toBeChecked();
  await expect(page.locator('input[name="level"][value="full"]')).toBeChecked();
  await expect(page.locator("#levelHint")).toContainText("Classic caveman");
  await expect(page.locator("#review")).toHaveAttribute(
    "href",
    "https://chromewebstore.google.com/detail/fixture-extension-id/reviews",
  );

  await page.locator('label:has(input[name="level"][value="ultra"]) span').click();
  await expect(page.locator("#levelHint")).toContainText("Max compression");
  await expect(page.locator("#levelHint")).toContainText("No invented abbreviations");
  await page.locator('label:has(input[data-site="claude.ai"]) span').click();
  await page.locator("label.switch .slider").click();
  await expect(page.locator("body")).toHaveAttribute("data-enabled", "0");
  await expect.poll(() => page.evaluate(() => window.__cavemanPopupStore)).toEqual({
    enabled: false,
    level: "ultra",
    sites: { "claude.ai": false },
  });
});

test("popup file-preview fallback uses localStorage and safe generic review URL", async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("caveman", JSON.stringify({
      enabled: false,
      level: "lite",
      sites: { "gemini.google.com": false },
    }));
  });
  await routePopup(page);
  await page.goto("https://extension.fixture/popup.html");

  await expect(page.locator("#master")).not.toBeChecked();
  await expect(page.locator('input[name="level"][value="lite"]')).toBeChecked();
  await expect(page.locator('input[data-site="gemini.google.com"]')).not.toBeChecked();
  await expect(page.locator("#review")).toHaveAttribute("href", "https://chromewebstore.google.com/");

  await page.locator("label.switch .slider").click();
  await expect.poll(() => page.evaluate(() => JSON.parse(localStorage.getItem("caveman")))).toMatchObject({
    enabled: true,
    level: "lite",
  });
});
