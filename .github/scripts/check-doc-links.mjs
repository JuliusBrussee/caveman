#!/usr/bin/env node
// Fails on dead documentation links in the given Markdown files. Checked:
//   - docs.caveman.so pages: HTTP 200, and a #fragment must exist as an id;
//   - this repo's github.com blob/tree links on main, and relative links: the
//     path must exist in this checkout (so a PR adding the target passes).
// Other hosts (badges, videos, registries) are not checked: their outages
// are not ours to gate on.
import { existsSync } from "node:fs";
import { readFile } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..", "..");
const repoPrefix = /^https:\/\/github\.com\/JuliusBrussee\/caveman\/(?:blob|tree)\/main\/([^#?]+)/;
const pages = new Map();
const failures = [];

async function page(url) {
  if (!pages.has(url)) {
    pages.set(url, fetch(url, { redirect: "follow", signal: AbortSignal.timeout(20_000) })
      .then(async (res) => ({ status: res.status, body: res.ok ? await res.text() : "" }))
      .catch((error) => ({ status: 0, body: "", error: String(error) })));
  }
  return pages.get(url);
}

for (const file of process.argv.slice(2)) {
  const text = await readFile(join(root, file), "utf8");
  const links = new Set([...text.matchAll(/\]\(([^)\s]+)\)|(https:\/\/docs\.caveman\.so[^\s)"'<>`]*)/g)]
    .map((m) => m[1] ?? m[2]));
  for (const link of links) {
    const repo = link.match(repoPrefix);
    if (repo) {
      if (!existsSync(join(root, decodeURIComponent(repo[1])))) failures.push(`${file}: ${link} (missing in repo)`);
    } else if (link.startsWith("https://docs.caveman.so")) {
      const [url, fragment] = link.split("#");
      const res = await page(url);
      if (res.status !== 200) failures.push(`${file}: ${link} (HTTP ${res.status}${res.error ? ` ${res.error}` : ""})`);
      else if (fragment && !res.body.includes(`id="${fragment}"`)) failures.push(`${file}: ${link} (no #${fragment} on page)`);
    } else if (!/^[a-z]+:/i.test(link) && !link.startsWith("#")) {
      const path = link.split("#")[0];
      if (path && !existsSync(resolve(root, dirname(file), decodeURIComponent(path)))) failures.push(`${file}: ${link} (missing file)`);
    }
  }
}

if (failures.length) {
  console.error(`dead documentation links:\n  ${failures.join("\n  ")}`);
  process.exit(1);
}
console.log(`documentation links ok (${process.argv.length - 2} files, ${pages.size} docs pages)`);
