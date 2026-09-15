import { createServer } from "node:http";

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

export async function stubUpstream() {
  const requests = [];
  const server = createServer((request, response) => {
    let rawBody = "";
    request.setEncoding("utf8");
    request.on("data", (chunk) => (rawBody += chunk));
    request.on("end", () => {
      const path = new URL(request.url ?? "/", "http://stub.invalid").pathname;
      if (request.method !== "POST" || path !== "/v1/messages") {
        response.writeHead(404, { "content-type": "application/json" });
        response.end(JSON.stringify({ error: { type: "not_found_error" } }));
        return;
      }

      let body;
      try {
        body = JSON.parse(rawBody);
      } catch {
        response.writeHead(400, { "content-type": "application/json" });
        response.end(JSON.stringify({ error: { type: "invalid_request_error" } }));
        return;
      }
      requests.push({ method: request.method, path, body });
      response.writeHead(200, { "content-type": "application/json" });
      response.end(
        JSON.stringify({
          id: "msg_stub",
          type: "message",
          role: "assistant",
          model: body.model ?? "claude-sonnet-4-6",
          content: [{ type: "text", text: "ok" }],
          stop_reason: "end_turn",
          stop_sequence: null,
          usage: {
            input_tokens: 12,
            cache_creation_input_tokens: 0,
            cache_read_input_tokens: 0,
            output_tokens: 2,
          },
        }),
      );
    });
  });

  const port = await listen(server);
  return {
    baseURL: `http://127.0.0.1:${port}`,
    requests,
    close: () => close(server),
  };
}
