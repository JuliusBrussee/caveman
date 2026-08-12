import { chromium } from "@playwright/test";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const assets = join(root, "store-assets");
const browser = await chromium.launch({ headless: true });
try {
  for (const item of [
    ["listing.html", "screenshot-1-hero.png", 1280, 800],
    ["compare.html", "screenshot-2-compare.png", 1280, 800],
    ["promo.html", "promo-tile-440x280.png", 440, 280],
  ]) {
    const [source, output, width, height] = item;
    const page = await browser.newPage({ viewport: { width, height }, deviceScaleFactor: 1 });
    await page.addInitScript(() => {
      window.chrome = {
        runtime: { id: "store-preview" },
        storage: {
          sync: {
            get(defaults, callback) { callback(defaults); },
            set(_value, callback) { callback?.(); },
          },
        },
      };
    });
    await page.goto(pathToFileURL(join(assets, source)).href);
    await page.evaluate(() => document.fonts.ready);
    await page.screenshot({ path: join(assets, output), type: "png" });
    await page.close();
    process.stdout.write(`${output}\n`);
  }
} finally {
  await browser.close();
}
