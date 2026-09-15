import { readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "@playwright/test";

const chrome = process.env.CAVEMAN_BROWSE_CHROME;
if (!chrome) {
  process.stderr.write("CAVEMAN_BROWSE_CHROME must point at Chrome\n");
  process.exit(2);
}

const here = dirname(fileURLToPath(import.meta.url));
const fixture = process.argv[2] ?? "order_dashboard.html";
if (!/^[a-z0-9_-]+\.html$/.test(fixture)) {
  process.stderr.write("fixture must be a testdata HTML basename\n");
  process.exit(2);
}
const html = await readFile(join(here, "..", "testdata", fixture), "utf8");
const browser = await chromium.launch({ headless: true, executablePath: chrome });
try {
  const page = await browser.newPage();
  await page.setContent(html);
  if (fixture === "order_dashboard.html") {
    await page.waitForFunction(() => document.querySelectorAll("tbody tr").length === 200);
  }
  process.stdout.write(await page.locator("body").ariaSnapshot());
} finally {
  await browser.close();
}
