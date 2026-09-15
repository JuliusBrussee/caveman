import { createServer } from "node:http";

function makeToken(org = "org-test") {
  const payload = Buffer.from(
    JSON.stringify({
      uid: "user-test",
      oid: org,
      email: "operator@example.test",
      role: "owner",
      exp: Math.floor(Date.now() / 1000) + 3600,
    }),
  ).toString("base64url");
  return `${payload}.sig`;
}

function listen(server) {
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      server.off("error", reject);
      resolve(server.address().port);
    });
  });
}

function close(server) {
  return new Promise((resolve, reject) => {
    server.close((error) => (error ? reject(error) : resolve()));
  });
}

export async function stubControlApi({ seatWalled = false, organization = "org-test" } = {}) {
  const token = makeToken(organization);
  const requests = [];
  let meAuthHeader = null;

  const server = createServer((request, response) => {
    let rawBody = "";
    request.setEncoding("utf8");
    request.on("data", (chunk) => (rawBody += chunk));
    request.on("end", () => {
      const path = new URL(request.url ?? "/", "http://stub.invalid").pathname;
      const authorization = request.headers.authorization ?? null;
      requests.push({ method: request.method, path, body: rawBody, authorization });

      const send = (status, body) => {
        response.writeHead(status, { "content-type": "application/json" });
        response.end(JSON.stringify(body));
      };

      if (request.method === "POST" && path === "/api/v1/auth/device/code") {
        send(200, {
          device_code: "device-test",
          user_code: "CAVE-TEST",
          verification_uri: "http://stub.invalid/activate",
          verification_uri_complete: "http://stub.invalid/activate?user_code=CAVE-TEST",
          expires_in: 60,
          interval: 0,
        });
        return;
      }
      if (request.method === "POST" && path === "/api/v1/auth/device/token") {
        send(200, { access_token: token, token_type: "Bearer", expires_in: 900 });
        return;
      }
      if (request.method === "GET" && path === "/api/v1/auth/me") {
        if (authorization !== `Bearer ${token}`) {
          send(401, { error: { code: "cave_unauthorized" } });
          return;
        }
        meAuthHeader = authorization;
        send(200, { user: { email: "operator@example.test" } });
        return;
      }
      if (request.method === "POST" && path === "/api/v1/me/wrap-entitlement") {
        if (authorization !== `Bearer ${token}`) {
          send(401, { error: { code: "cave_unauthorized" } });
          return;
        }
        let body;
        try {
          body = JSON.parse(rawBody);
        } catch {
          send(400, { error: { code: "cave_invalid_json" } });
          return;
        }
        if (
          typeof body?.device_hash !== "string" ||
          !/^[a-f0-9]{64}$/.test(body.device_hash) ||
          typeof body?.device_name !== "string" ||
          body.device_name.trim() === ""
        ) {
          send(400, { error: { code: "cave_invalid_device" } });
          return;
        }
        if (seatWalled) {
          send(403, {
            error: {
              code: "cave_seats_exhausted",
              organization,
              plan: "free",
              seats_used: 1,
              seats_limit: 1,
            },
          });
          return;
        }
        send(200, {
          entitled: true,
          plan: "free",
          telemetry_level: "metadata",
          seats_used: 1,
          seats_limit: 1,
          devices_used: 1,
          devices_limit: 3,
          evicted_device_hash: null,
          expires_at: new Date(Date.now() + 72 * 3600 * 1000).toISOString(),
        });
        return;
      }
      send(404, { error: { code: "cave_not_found" } });
    });
  });

  const port = await listen(server);
  return {
    baseURL: `http://127.0.0.1:${port}`,
    token,
    requests,
    capturedAuth: () => meAuthHeader,
    close: () => close(server),
  };
}
