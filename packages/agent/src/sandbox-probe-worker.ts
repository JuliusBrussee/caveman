import { spawnSync } from "node:child_process";
import { resolve4 } from "node:dns/promises";
import { createSocket } from "node:dgram";
import { readFileSync } from "node:fs";
import { installNetworkDeny } from "./sandbox-network.js";

function denied(run: () => unknown): boolean {
  try {
    run();
    return false;
  } catch {
    return true;
  }
}

installNetworkDeny();
let homeReadCode = "";
const homeReadDenied = denied(() => {
  try {
    readFileSync(process.env.CAVE_SANDBOX_HOME_PROBE ?? "");
  } catch (error) {
    homeReadCode = (error as NodeJS.ErrnoException).code ?? "";
    throw error;
  }
});
const childProcessDenied = denied(() => {
  const result = spawnSync(process.execPath, ["--version"]);
  if (result.error) throw result.error;
});
let networkDenied = false;
try {
  await fetch("https://example.com");
} catch {
  networkDenied = true;
}
let dnsDenied = false;
try {
  await resolve4("example.com");
} catch {
  dnsDenied = true;
}
let udpDenied = false;
try {
  const socket = createSocket("udp4");
  await new Promise<void>((accept, reject) => {
    socket.once("error", reject);
    socket.send(Buffer.from("x"), 53, "1.1.1.1", (error) => error ? reject(error) : accept());
  });
  socket.close();
} catch {
  udpDenied = true;
}
process.stdout.write(JSON.stringify({
  home_read_denied: homeReadDenied,
  home_read_code: homeReadCode,
  child_process_denied: childProcessDenied,
  network_denied: networkDenied,
  dns_denied: dnsDenied,
  udp_denied: udpDenied,
}));
