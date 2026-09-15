import assert from "node:assert/strict";
import test from "node:test";
import { nextDevicePollIntervalMs, runCavemanDeviceFlow } from "../dist/index.js";

test("slow_down adds five seconds", () => {
  assert.equal(nextDevicePollIntervalMs(5000, "slow_down"), 10000);
});

test("grant defers durable acknowledgement until caller persists", async () => {
  const requests = [];
  const grant = await runCavemanDeviceFlow({
    baseURL: "https://control.example",
    client: "test",
    sleep: async () => {},
    fetch: async (input) => {
      const url = String(input);
      requests.push(url);
      if (url.endsWith("/code")) return Response.json({
        device_code: "device",
        user_code: "CODE",
        verification_uri: "https://control.example/activate",
        expires_in: 60,
        interval: 0,
      });
      if (url.endsWith("/token")) return Response.json({
        access_token: "access",
        refresh_token: "refresh",
        delivery_ack_token: "ack",
      });
      return Response.json({}, { status: 200 });
    },
  });
  assert.equal(requests.some((url) => url.endsWith("/ack")), false);
  await grant.acknowledge();
  await grant.acknowledge();
  assert.equal(requests.filter((url) => url.endsWith("/ack")).length, 1);
});

test("missing durable ACK token fails closed", async () => {
  const grant = await runCavemanDeviceFlow({
    baseURL: "https://control.example",
    client: "test",
    sleep: async () => {},
    fetch: async (input) => String(input).endsWith("/code")
      ? Response.json({ device_code: "d", user_code: "u", verification_uri: "https://v", expires_in: 60, interval: 0 })
      : Response.json({ access_token: "access", refresh_token: "refresh" }),
  });
  await assert.rejects(grant.acknowledge(), /did not provide a delivery acknowledgement token/);
});
