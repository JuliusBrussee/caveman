import { chmod, readFile, writeFile } from "node:fs/promises";

const file = new URL("../dist/index.js", import.meta.url);
const body = await readFile(file, "utf8");
if (!body.startsWith("#!/usr/bin/env node")) {
  await writeFile(file, `#!/usr/bin/env node\n${body}`);
}
await chmod(file, 0o755);
