// `caveman stop`: end the local runtime `caveman start` or `caveman <agent>`
// started. Only a proxy whose run state answers for its own listener is
// signalled; anything else on the port is left alone. Idempotent.
import { moduleHost } from "./apply.js";

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return (error as NodeJS.ErrnoException).code === "EPERM";
  }
}

export async function stopRuntime(): Promise<void> {
  const runtimes = await moduleHost().localRuntimes();
  const owned = runtimes.filter((runtime) => runtime.pid !== undefined);
  const pids = [...new Set(owned.map((runtime) => runtime.pid!))].filter(alive);
  if (pids.length === 0) {
    const foreign = runtimes.find((runtime) => runtime.listening);
    console.log(foreign ? `not running · ${foreign.host}:${foreign.port} is held by something else; left alone` : "not running");
    return;
  }
  for (const pid of pids) {
    try { process.kill(pid, "SIGTERM"); } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ESRCH") throw error;
    }
  }
  // The proxy drains for up to 5s before it removes its run state and exits.
  const deadline = Date.now() + 7000;
  while (pids.some(alive) && Date.now() < deadline) await new Promise((resolve) => setTimeout(resolve, 100));
  const stuck = pids.filter(alive);
  if (stuck.length) {
    console.error(`✗ runtime did not stop (pid ${stuck.join(", ")})`);
    process.exitCode = 1;
    return;
  }
  console.log(`✓ stopped · ${owned.map((runtime) => `${runtime.host}:${runtime.port}`).join(", ")}`);
}
