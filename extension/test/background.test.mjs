import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import vm from "node:vm";

test("background lifecycle and storage changes keep toolbar badge truthful", async () => {
  const listeners = {};
  const calls = [];
  let enabled = true;
  const chrome = {
    action: {
      setBadgeText(value) {
        calls.push(["text", JSON.parse(JSON.stringify(value))]);
      },
      setBadgeBackgroundColor(value) {
        calls.push(["color", JSON.parse(JSON.stringify(value))]);
      },
    },
    runtime: {
      onInstalled: { addListener(fn) { listeners.installed = fn; } },
      onStartup: { addListener(fn) { listeners.startup = fn; } },
    },
    storage: {
      sync: {
        get(defaults, callback) {
          assert.equal(defaults.enabled, true);
          assert.deepEqual(Object.keys(defaults), ["enabled"]);
          callback({ enabled });
        },
      },
      onChanged: { addListener(fn) { listeners.changed = fn; } },
    },
  };
  const source = await readFile(new URL("../src/background.js", import.meta.url), "utf8");
  vm.runInNewContext(source, { chrome }, { filename: "public/extension/src/background.js" });

  listeners.installed();
  assert.deepEqual(calls.splice(0), [
    ["text", { text: "ON" }],
    ["color", { color: "#C75B39" }],
  ]);

  enabled = false;
  listeners.startup();
  assert.deepEqual(calls.splice(0), [
    ["text", { text: "" }],
    ["color", { color: "#C75B39" }],
  ]);

  listeners.changed({ enabled: { oldValue: false, newValue: true } }, "sync");
  assert.deepEqual(calls.splice(0), [
    ["text", { text: "ON" }],
    ["color", { color: "#C75B39" }],
  ]);

  listeners.changed({ enabled: { oldValue: true, newValue: false } }, "local");
  listeners.changed({ level: { oldValue: "full", newValue: "lite" } }, "sync");
  assert.deepEqual(calls, []);
});
