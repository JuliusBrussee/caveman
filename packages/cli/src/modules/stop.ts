// `caveman stop`: end the local runtime `caveman start` or `caveman <agent>`
// started. Only a proxy whose run state answers for its own listener is
// signalled; anything else on the port is left alone. Idempotent.
import { request } from "node:http";
import { moduleHost, type LocalRuntime } from "./apply.js";

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return (error as NodeJS.ErrnoException).code === "EPERM";
  }
}

function signal(pids: number[]): void {
  for (const pid of pids) {
    try { process.kill(pid, "SIGTERM"); } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ESRCH") throw error;
    }
  }
}

async function exited(pids: number[], ms: number): Promise<void> {
  const deadline = Date.now() + ms;
  while (pids.some(alive) && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 100));
}

// POST /caveman/shutdown with the run state's instance token: the proxy drains
// and exits as it does on SIGTERM. Plain loopback HTTP, never the CLI's
// proxy-aware fetch. False when the proxy does not take it (older ones 404).
function askToStop(runtime: LocalRuntime): Promise<boolean> {
  return new Promise((resolve) => {
    if (!runtime.token) return resolve(false);
    const req = request({ host: runtime.host, port: runtime.port, path: "/caveman/shutdown", method: "POST", headers: { "x-caveman-instance": runtime.token }, timeout: 2000 }, (res) => {
      res.resume();
      resolve(res.statusCode === 202);
    });
    req.on("timeout", () => req.destroy());
    req.on("error", () => resolve(false));
    req.end();
  });
}

// Ends the given runtimes and returns the pids still alive. On Windows
// process.kill ends a process at once, with no drain, so each proxy is first
// asked over its listener to shut down; one that does not take the request,
// or is still up after the drain, is then ended the hard way.
export async function endRuntimes(runtimes: LocalRuntime[], platform: NodeJS.Platform = process.platform): Promise<number[]> {
  const owned = runtimes.filter((runtime, index) => runtime.pid !== undefined && alive(runtime.pid)
    && runtimes.findIndex((other) => other.pid === runtime.pid) === index);
  const pids = owned.map((runtime) => runtime.pid!);
  const asked = platform === "win32" ? await Promise.all(owned.map(askToStop)) : [];
  const accepted = pids.filter((_, index) => asked[index]);
  signal(pids.filter((pid) => !accepted.includes(pid)));
  // The proxy drains for up to 5s before it removes its run state and exits.
  await exited(pids, 7000);
  const late = accepted.filter(alive);
  if (late.length) {
    signal(late);
    await exited(late, 2000);
  }
  return pids.filter(alive);
}

export async function stopRuntime(): Promise<void> {
  const runtimes = await moduleHost().localRuntimes();
  const owned = runtimes.filter((runtime) => runtime.pid !== undefined);
  if (!owned.some((runtime) => alive(runtime.pid!))) {
    const foreign = runtimes.find((runtime) => runtime.listening);
    console.log(foreign ? `not running · ${foreign.host}:${foreign.port} is held by something else; left alone` : "not running");
    return;
  }
  const stuck = await endRuntimes(owned);
  if (stuck.length) {
    console.error(`✗ runtime did not stop (pid ${stuck.join(", ")})`);
    process.exitCode = 1;
    return;
  }
  console.log(`✓ stopped · ${owned.map((runtime) => `${runtime.host}:${runtime.port}`).join(", ")}`);
}
