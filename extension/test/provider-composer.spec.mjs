import { expect, test } from "@playwright/test";
import { readFile } from "node:fs/promises";

const providers = [
  ["ChatGPT rich editor", "https://chatgpt.com/"],
  ["ChatGPT current textarea", "https://chatgpt.com/?textarea"],
  ["ChatGPT legacy hostname", "https://chat.openai.com/"],
  ["Claude", "https://claude.ai/"],
  ["Gemini", "https://gemini.google.com/"],
];

async function setup(page, url) {
  await page.clock.install();
  await page.route(`${new URL(url).origin}/**`, async route => {
    const path = new URL(route.request().url()).pathname;
    const file = path.startsWith("/src/")
      ? new URL(`..${path}`, import.meta.url)
      : new URL("./provider-harness.html", import.meta.url);
    await route.fulfill({ contentType: path.endsWith(".js") ? "text/javascript" : "text/html", body: await readFile(file) });
  });
  await page.goto(url);
  await expect(page.locator("#caveman-indicator")).toBeVisible();
}

for (const [name, url] of providers) {
  test(`${name}: primer and reminder use the editor input model, preserving code and newlines`, async ({ page }) => {
    await setup(page, url);
    const prompt = 'Explain this Windows path:\nconst path = "C:\\Users\\First Last";\n\nKeep this last line.';
    await page.getByRole("textbox").fill(prompt);
    const original = await page.getByRole("textbox").evaluate(el => el.tagName === "TEXTAREA" ? el.value : el.innerText);
    await page.getByRole("textbox").press("Enter");
    await page.clock.runFor(50);
    const sent = await page.evaluate(() => window.fixture.sent);
    expect(sent).toHaveLength(1);
    expect(sent[0]).toContain("[Caveman mode is ON");
    expect(sent[0]).toContain("Intensity FULL");
    expect(sent[0].slice(-original.length)).toBe(original);
    await page.getByRole("textbox").fill("second prompt");
    await page.locator("#composer button").click();
    await page.clock.runFor(50);
    const allSent = await page.evaluate(() => window.fixture.sent);
    expect(allSent).toHaveLength(2);
    expect(allSent[1]).toMatch(/^\[stay in caveman mode — FULL\]\n{2,}second prompt$/);
    expect(await page.evaluate(() => window.fixture.feedback)).toBe(0);
  });

  test(`${name}: stop command disables injection for later sends`, async ({ page }) => {
    await setup(page, url);
    await page.getByRole("textbox").fill("stop caveman");
    await page.getByRole("textbox").press("Enter");
    await expect(page.locator("#caveman-indicator")).toBeHidden();
    await page.getByRole("textbox").fill("next raw message");
    await page.locator("#composer button").click();
    expect(await page.evaluate(() => ({ sent: window.fixture.sent, enabled: window.fixture.store.enabled })))
      .toEqual({ sent: ["stop caveman", "next raw message"], enabled: false });
  });

  test(`${name}: missing send control never clicks feedback or swallows native Enter`, async ({ page }) => {
    await setup(page, url);
    await page.getByRole("textbox").fill("native send");
    await page.evaluate(() => window.fixture.send.remove());
    await page.getByRole("textbox").press("Enter");
    await page.clock.runFor(1000);
    expect(await page.evaluate(() => ({ sent: window.fixture.sent, feedback: window.fixture.feedback })))
      .toEqual({ sent: ["native send"], feedback: 0 });
  });

  test(`${name}: modifier and IME Enter never inject or suppress native input`, async ({ page }) => {
    await setup(page, url);
    await page.getByRole("textbox").fill("入力中 draft");
    const results = await page.evaluate(() => [
      { shiftKey: true }, { ctrlKey: true }, { altKey: true }, { metaKey: true },
      { isComposing: true }, { isComposing: false, keyCode: 229 },
    ].map(event => window.fixture.pressEnter(event)));
    expect(results).toEqual([true, true, true, true, true, true]);
    await page.clock.runFor(1000);
    expect(await page.evaluate(() => window.fixture.sent)).toEqual([]);
    expect(await page.getByRole("textbox").evaluate(el => el.tagName === "TEXTAREA" ? el.value : el.innerText))
      .toBe("入力中 draft");
  });
}

for (const [name, url] of providers.filter(([name]) => !name.includes("textarea"))) {
  test(`${name}: prepending preserves existing rich nodes`, async ({ page }) => {
    await setup(page, url);
    await page.evaluate(() => {
      const { editor } = window.fixture;
      editor.innerHTML = '<p>First line</p><p><span contenteditable="false" data-reference="attached-file">attachment.txt</span> last line</p>';
      editor.dispatchEvent(new Event("input", { bubbles: true }));
      window.fixture.hold = true;
      window.fixture.pressEnter();
    });
    await expect(page.locator('[data-reference="attached-file"]')).toHaveText("attachment.txt");
    await page.clock.runFor(1000);
    expect(await page.evaluate(() => window.fixture.sent)).toEqual([]);
  });
}

for (const change of ["edit", "navigate", "replace", "disable"]) {
  test(`pending send is cancelled after ${change}`, async ({ page }) => {
    await setup(page, "https://chatgpt.com/?textarea");
    await page.getByRole("textbox").fill("authorized draft");
    await page.evaluate(() => { window.fixture.hold = true; window.fixture.pressEnter(); });
    if (change === "edit") await page.getByRole("textbox").fill("new draft never requested to send");
    else if (change === "navigate") await page.evaluate(() => history.pushState({}, "", "/c/different-chat"));
    else if (change === "replace") await page.evaluate(() => {
      const old = document.getElementById("composer");
      const replacement = old.cloneNode(true);
      replacement.querySelector("textarea").value = "different conversation draft";
      replacement.querySelector("button").addEventListener("click", () => window.fixture.sent.push("WRONG COMPOSER"));
      old.replaceWith(replacement);
    });
    else await page.evaluate(() => new Promise(resolve => chrome.storage.sync.set({ enabled: false }, resolve)));
    await page.evaluate(() => { window.fixture.send.disabled = false; });
    await page.clock.runFor(1000);
    expect(await page.evaluate(() => window.fixture.sent)).toEqual([]);
  });
}

for (const change of ["same-text input", "rich reference"]) {
  test(`pending send is cancelled after ${change} changes the rich draft`, async ({ page }) => {
    await setup(page, "https://claude.ai/");
    await page.getByRole("textbox").fill("authorized draft");
    await page.evaluate(() => { window.fixture.hold = true; window.fixture.pressEnter(); });
    await page.evaluate(change => {
      const { editor, send } = window.fixture;
      if (change === "same-text input") editor.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "formatBold" }));
      else {
        // A framework can update a referenced file without changing visible text
        // or dispatching an input event. Text equality does not prove identity.
        const reference = document.createElement("span");
        reference.contentEditable = "false";
        reference.dataset.reference = "new-file";
        editor.append(reference);
      }
      send.disabled = false;
    }, change);
    await page.clock.runFor(1000);
    expect(await page.evaluate(() => window.fixture.sent)).toEqual([]);
  });
}

test("unchanged pending draft sends once when its own control enables", async ({ page }) => {
  await setup(page, "https://claude.ai/");
  await page.getByRole("textbox").fill("authorized delayed draft");
  await page.evaluate(() => { window.fixture.hold = true; window.fixture.pressEnter(); });
  await page.clock.runFor(200);
  expect(await page.evaluate(() => window.fixture.sent)).toEqual([]);
  await page.evaluate(() => { window.fixture.send.disabled = false; });
  await page.clock.runFor(1000);
  const sent = await page.evaluate(() => window.fixture.sent);
  expect(sent).toHaveLength(1);
  expect(sent[0]).toMatch(/authorized delayed draft$/);
});

test("disabled or Stop control is never replayed with synthetic Enter", async ({ page }) => {
  await setup(page, "https://chatgpt.com/?textarea");
  await page.getByRole("textbox").fill("waiting draft");
  await page.evaluate(() => { window.fixture.hold = true; window.fixture.pressEnter(); });
  await page.evaluate(() => {
    window.fixture.send.disabled = false;
    window.fixture.send.setAttribute("aria-label", "Stop generating");
    window.fixture.send.addEventListener("click", () => window.fixture.sent.push("STOP CLICKED"));
  });
  await page.clock.runFor(1000);
  expect(await page.evaluate(() => window.fixture.sent)).toEqual([]);
});

test("an explicit click replaces a pending replay without a second send", async ({ page }) => {
  await setup(page, "https://claude.ai/");
  await page.getByRole("textbox").fill("one user message");
  await page.evaluate(() => {
    window.fixture.hold = true;
    window.fixture.pressEnter();
    window.fixture.send.disabled = false;
    window.fixture.send.click();
  });
  await page.clock.runFor(1000);
  expect(await page.evaluate(() => window.fixture.sent)).toHaveLength(1);
});

test("a synchronous editor rewrite during injection cannot authorize a different draft", async ({ page }) => {
  await setup(page, "https://chatgpt.com/?textarea");
  await page.getByRole("textbox").fill("authorized original");
  await page.evaluate(() => {
    const { editor } = window.fixture;
    editor.addEventListener("input", () => {
      if (!editor.value.startsWith("[Caveman mode")) return;
      editor.value = "replacement draft never authorized";
      editor.dispatchEvent(new Event("input", { bubbles: true }));
    });
    window.fixture.pressEnter();
  });
  await page.clock.runFor(1000);
  expect(await page.evaluate(() => window.fixture.sent)).toEqual([]);
});
