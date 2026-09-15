import { expect, test } from "@playwright/test";
import { readFile } from "node:fs/promises";

const assets = new Map([
  ["/", new URL("./harness.html", import.meta.url)],
  ["/src/directive.js", new URL("../src/directive.js", import.meta.url)],
  ["/src/caveman.js", new URL("../src/caveman.js", import.meta.url)],
]);

test.beforeEach(async ({ page }) => {
  await page.route("https://chatgpt.com/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const file = assets.get(path);
    if (!file) return route.fulfill({ status: 404, body: "not found" });
    await route.fulfill({
      status: 200,
      contentType: path.endsWith(".js") ? "text/javascript" : "text/html",
      body: await readFile(file),
    });
  });
  await page.goto("https://chatgpt.com/");
  await expect(page.locator("#caveman-indicator")).toBeVisible();
});

test("first Enter sends full primer and later click sends short reminder exactly once", async ({ page }) => {
  await page.evaluate(() => {
    window.cavemanTest.type("first prompt");
    window.cavemanTest.pressEnter();
  });
  await expect.poll(() => page.evaluate(() => window.cavemanTest.transcript())).toHaveLength(1);
  let transcript = await page.evaluate(() => window.cavemanTest.transcript());
  expect(transcript[0]).toContain("[Caveman mode is ON");
  expect(transcript[0]).toContain("Intensity FULL");
  expect(transcript[0]).toMatch(/\n\nfirst prompt$/);

  await page.waitForTimeout(250);
  await page.evaluate(() => {
    window.cavemanTest.type("second prompt");
    window.cavemanTest.clickSend();
  });
  await expect.poll(() => page.evaluate(() => window.cavemanTest.transcript())).toHaveLength(2);
  transcript = await page.evaluate(() => window.cavemanTest.transcript());
  expect(transcript[1]).toMatch(/^\[stay in caveman mode — FULL\]\n{2,}second prompt$/);
  expect(transcript[1].match(/\[stay in caveman mode/g)).toHaveLength(1);
});

test("site disable and indicator toggle preserve raw outgoing prompt", async ({ page }) => {
  await page.evaluate(async () => {
    await window.cavemanTest.setStorage({ sites: { "chatgpt.com": false } });
    window.cavemanTest.type("site disabled");
    window.cavemanTest.clickSend();
  });
  await expect.poll(() => page.evaluate(() => window.cavemanTest.transcript())).toEqual(["site disabled"]);

  await page.evaluate(() => window.cavemanTest.setStorage({ sites: {}, enabled: true, level: "ultra" }));
  await expect(page.locator("#caveman-indicator")).toContainText("ultra");
  await page.locator("#caveman-indicator").click();
  await expect(page.locator("#caveman-indicator")).toBeHidden();
  await page.evaluate(() => {
    window.cavemanTest.type("master disabled");
    window.cavemanTest.clickSend();
  });
  await expect.poll(() => page.evaluate(() => window.cavemanTest.transcript())).toEqual([
    "site disabled",
    "master disabled",
  ]);
});

test("modifier Enter, blank draft, and already-prefixed prompt never get duplicate injection", async ({ page }) => {
  await page.evaluate(() => {
    window.cavemanTest.type("shift newline");
    window.cavemanTest.pressEnter({ shiftKey: true });
  });
  await page.waitForTimeout(80);
  expect(await page.evaluate(() => window.cavemanTest.transcript())).toEqual([]);

  await page.evaluate(() => {
    window.cavemanTest.type("   ");
    window.cavemanTest.clickSend();
  });
  await page.waitForTimeout(80);
  expect(await page.evaluate(() => window.cavemanTest.transcript())).toEqual([]);

  await page.evaluate(() => {
    window.cavemanTest.type("[stay in caveman mode — FULL]\n\nalready prefixed");
    window.cavemanTest.clickSend();
  });
  await expect.poll(() => page.evaluate(() => window.cavemanTest.transcript())).toEqual([
    "[stay in caveman mode — FULL]\n\nalready prefixed",
  ]);
});
