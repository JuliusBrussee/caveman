export interface DeviceCode {
  device_code: string;
  user_code: string;
  verification_uri: string;
  verification_uri_complete?: string;
  expires_in: number;
  interval?: number;
  gateway_url?: string;
}

export interface DeviceCredentials {
  access_token: string;
  refresh_token?: string;
  gateway_api_key?: string;
  gateway_key_id?: string;
  project_id?: string;
  delivery_ack_token?: string;
  [key: string]: unknown;
}

export interface DeviceGrant {
  code: DeviceCode;
  credentials: DeviceCredentials;
  acknowledge(): Promise<void>;
}

export function nextDevicePollIntervalMs(currentMs: number, errorCode?: string): number {
  const current = Math.max(0, Number.isFinite(currentMs) ? currentMs : 0);
  return errorCode === "slow_down" ? current + 5000 : current;
}

function defaultSleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function requestSignal(signal: AbortSignal | undefined, timeoutMs: number): AbortSignal {
  const timeout = AbortSignal.timeout(timeoutMs);
  return signal === undefined ? timeout : AbortSignal.any([signal, timeout]);
}

async function acknowledge(
  options: {
    baseURL: string;
    client: string;
    code: DeviceCode;
    credentials: DeviceCredentials;
    fetcher: typeof globalThis.fetch;
    signal?: AbortSignal;
    sleep: (ms: number) => Promise<void>;
  },
): Promise<void> {
  const durable = Boolean(options.credentials.refresh_token || options.credentials.gateway_api_key ||
    options.credentials.gateway_key_id || options.credentials.project_id);
  if (!durable) return;
  const ackToken = options.credentials.delivery_ack_token;
  if (typeof ackToken !== "string" || ackToken === "") {
    throw new Error("device login failed: server did not provide a delivery acknowledgement token");
  }
  let lastError = "unknown error";
  for (let attempt = 0; attempt < 5; attempt++) {
    try {
      const response = await options.fetcher(`${options.baseURL}/api/v1/auth/device/ack`, {
        method: "POST",
        headers: {
          authorization: `Bearer ${options.credentials.access_token}`,
          "content-type": "application/json",
          "x-cave-client": options.client,
        },
        body: JSON.stringify({ device_code: options.code.device_code, ack_token: ackToken }),
        signal: requestSignal(options.signal, 5000),
      });
      if (response.ok) return;
      const body = await response.json().catch(() => null) as { error?: { code?: unknown } } | null;
      const code = typeof body?.error?.code === "string" ? body.error.code : `HTTP ${response.status}`;
      lastError = code;
      if (response.status < 500 && response.status !== 429) break;
    } catch (error) {
      lastError = error instanceof Error ? error.message : String(error);
    }
    if (attempt < 4) await options.sleep(Math.min(2000, 200 * 2 ** attempt));
  }
  throw new Error(`device credential delivery acknowledgement failed (${lastError}); credentials were persisted locally but the server may revoke them after the delivery window`);
}

export async function runCavemanDeviceFlow(options: {
  baseURL: string;
  client: string;
  fetch?: typeof globalThis.fetch;
  signal?: AbortSignal;
  sleep?: (ms: number) => Promise<void>;
  onCode?: (code: DeviceCode) => void | Promise<void>;
}): Promise<DeviceGrant> {
  const fetcher = options.fetch ?? globalThis.fetch;
  const wait = options.sleep ?? defaultSleep;
  const baseURL = options.baseURL.replace(/\/$/, "");
  const codeResponse = await fetcher(`${baseURL}/api/v1/auth/device/code`, {
    method: "POST",
    headers: { "content-type": "application/json", "x-cave-client": options.client },
    body: "{}",
    signal: requestSignal(options.signal, 5000),
  });
  if (!codeResponse.ok) throw new Error(`device authorization failed: HTTP ${codeResponse.status}`);
  const rawCode = await codeResponse.json().catch(() => null) as Partial<DeviceCode> | null;
  if (rawCode === null || typeof rawCode.device_code !== "string" || rawCode.device_code === "" ||
    typeof rawCode.user_code !== "string" || typeof rawCode.verification_uri !== "string" ||
    typeof rawCode.expires_in !== "number" || !Number.isFinite(rawCode.expires_in) || rawCode.expires_in <= 0) {
    throw new Error(`device authorization failed: ${JSON.stringify(rawCode)}`);
  }
  const code = rawCode as DeviceCode;
  await options.onCode?.(structuredClone(code));
  let intervalMs = Math.max(0, Number(code.interval ?? 5)) * 1000;
  const deadline = Date.now() + code.expires_in * 1000;
  while (Date.now() < deadline) {
    let payload: Record<string, unknown>;
    let status = 0;
    let retryAfterMs = 0;
    try {
      const response = await fetcher(`${baseURL}/api/v1/auth/device/token`, {
        method: "POST",
        headers: { "content-type": "application/json", "x-cave-client": options.client },
        body: JSON.stringify({ device_code: code.device_code }),
        signal: requestSignal(options.signal, 5000),
      });
      status = response.status;
      const retryAfter = response.headers.get("retry-after");
      if (retryAfter !== null) {
        const seconds = Number(retryAfter);
        if (Number.isFinite(seconds) && seconds >= 0) retryAfterMs = seconds * 1000;
      }
      payload = await response.json() as Record<string, unknown>;
    } catch (error) {
      if (options.signal?.aborted) throw options.signal.reason;
      if (Date.now() >= deadline) {
        throw new Error(`device login polling failed: ${error instanceof Error ? error.message : String(error)}`);
      }
      await wait(Math.max(intervalMs, retryAfterMs, 200));
      continue;
    }
    if (status === 429) {
      await wait(Math.max(intervalMs, retryAfterMs, 200));
      continue;
    }
    const accessToken = typeof payload.access_token === "string" ? payload.access_token : "";
    if (accessToken !== "") {
      const credentials = { ...payload, access_token: accessToken } as DeviceCredentials;
      let acknowledged = false;
      return {
        code: structuredClone(code),
        credentials: structuredClone(credentials),
        async acknowledge() {
          if (acknowledged) return;
          await acknowledge({
            baseURL,
            client: options.client,
            code,
            credentials,
            fetcher,
            ...(options.signal === undefined ? {} : { signal: options.signal }),
            sleep: wait,
          });
          acknowledged = true;
        },
      };
    }
    const errorCode = typeof payload.error === "string" ? payload.error : "";
    if (errorCode === "slow_down") intervalMs = nextDevicePollIntervalMs(intervalMs, errorCode);
    else if (errorCode !== "" && errorCode !== "authorization_pending") {
      throw new Error(`device login failed: ${errorCode}`);
    }
    await wait(Math.max(intervalMs, 200));
  }
  throw new Error("device login timed out before approval");
}
