import { spawn } from "node:child_process";

export function runCli(cli, argv, { env = process.env, cwd, timeoutMs = 10_000 } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn("node", [cli, ...argv], { env, cwd });
    let stdout = "";
    let stderr = "";
    let settled = false;
    let timedOut = false;
    let deadlineTimer;
    let forceTimer;
    let hardStopTimer;

    const clearTimers = () => {
      clearTimeout(deadlineTimer);
      clearTimeout(forceTimer);
      clearTimeout(hardStopTimer);
    };
    const timeoutError = () => {
      const error = new Error(
        [
          `CLI timed out after ${timeoutMs}ms: ${argv.join(" ")}`,
          stdout ? `stdout:\n${stdout}` : "",
          stderr ? `stderr:\n${stderr}` : "",
        ]
          .filter(Boolean)
          .join("\n"),
      );
      error.stdout = stdout;
      error.stderr = stderr;
      return error;
    };
    const finish = (callback) => {
      if (settled) return;
      settled = true;
      clearTimers();
      callback();
    };

    child.stdout.on("data", (chunk) => (stdout += chunk));
    child.stderr.on("data", (chunk) => (stderr += chunk));
    child.on("error", (error) => finish(() => reject(error)));
    child.on("exit", (code) => {
      finish(() => {
        if (timedOut) reject(timeoutError());
        else resolve({ code, stdout, stderr });
      });
    });

    deadlineTimer = setTimeout(() => {
      timedOut = true;
      child.kill("SIGTERM");
      forceTimer = setTimeout(() => child.kill("SIGKILL"), 500);
      hardStopTimer = setTimeout(() => {
        child.unref();
        finish(() => reject(timeoutError()));
      }, 1_500);
    }, timeoutMs);
  });
}
