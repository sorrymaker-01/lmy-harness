import { readFile } from "node:fs/promises";
import { createServer, IncomingMessage, ServerResponse } from "node:http";
import { extname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { AgentRuntime } from "@agent/agent-runtime";
import { InMemoryShortMemoryStore } from "@agent/memory";
import { McpRuntime, McpServerConfig } from "@agent/mcp-runtime";
import { createDefaultModel } from "@agent/model-router";
import { createDefaultSkillRegistry } from "@agent/skill-engine";
import { InMemoryStore } from "./store.js";

const port = Number(process.env.PORT ?? 3000);
const host = process.env.HOST ?? "127.0.0.1";
const publicDir = join(fileURLToPath(new URL(".", import.meta.url)), "../public");

const store = new InMemoryStore();
const memory = new InMemoryShortMemoryStore();
const skills = createDefaultSkillRegistry();
const mcp = new McpRuntime();
const model = createDefaultModel();
const agent = new AgentRuntime({ store, memory, skills, mcp, model });

const defaultConversation = await store.createConversation("MCP Skill Agent MVP");

const server = createServer(async (req, res) => {
  try {
    await route(req, res);
  } catch (error) {
    sendJson(res, 500, { error: error instanceof Error ? error.message : String(error) });
  }
});

server.listen(port, host, () => {
  console.log(`Agent MVP listening on http://${host}:${port}`);
});

async function route(req: IncomingMessage, res: ServerResponse): Promise<void> {
  const url = new URL(req.url ?? "/", `http://${req.headers.host ?? "localhost"}`);

  if (req.method === "GET" && url.pathname === "/health") {
    sendJson(res, 200, { ok: true });
    return;
  }

  if (req.method === "GET" && url.pathname === "/api/conversations") {
    sendJson(res, 200, { conversations: await store.listConversations(), defaultConversationId: defaultConversation.id });
    return;
  }

  if (req.method === "POST" && url.pathname === "/api/conversations") {
    const body = await readJson<{ title?: string }>(req);
    sendJson(res, 201, { conversation: await store.createConversation(body.title) });
    return;
  }

  const messagesMatch = url.pathname.match(/^\/api\/conversations\/([^/]+)\/messages$/);
  if (req.method === "GET" && messagesMatch) {
    sendJson(res, 200, { messages: await store.getMessages(messagesMatch[1]) });
    return;
  }

  const chatMatch = url.pathname.match(/^\/api\/conversations\/([^/]+)\/chat$/);
  if (req.method === "POST" && chatMatch) {
    const body = await readJson<{ message: string; requireConfirmation?: boolean }>(req);
    const output = await agent.run({
      conversationId: chatMatch[1],
      userMessage: body.message,
      requireConfirmation: Boolean(body.requireConfirmation)
    });
    sendJson(res, 200, output);
    return;
  }

  const tracesMatch = url.pathname.match(/^\/api\/conversations\/([^/]+)\/traces$/);
  if (req.method === "GET" && tracesMatch) {
    sendJson(res, 200, { traces: await store.getTraces(tracesMatch[1]) });
    return;
  }

  if (req.method === "GET" && url.pathname === "/api/tools") {
    sendJson(res, 200, { tools: await agent.listTools() });
    return;
  }

  if (req.method === "GET" && url.pathname === "/api/mcp/servers") {
    sendJson(res, 200, { servers: mcp.listServers() });
    return;
  }

  if (req.method === "POST" && url.pathname === "/api/mcp/servers") {
    const body = await readJson<McpServerConfig>(req);
    mcp.registerServer(body);
    sendJson(res, 201, { server: body, tools: await mcp.refreshTools().catch((error) => ({ error: String(error) })) });
    return;
  }

  if (req.method === "GET" && (url.pathname === "/" || !url.pathname.startsWith("/api/"))) {
    await serveStatic(url.pathname === "/" ? "/index.html" : url.pathname, res);
    return;
  }

  sendJson(res, 404, { error: "Not found" });
}

async function readJson<T>(req: IncomingMessage): Promise<T> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
  }
  if (!chunks.length) return {} as T;
  return JSON.parse(Buffer.concat(chunks).toString("utf8")) as T;
}

function sendJson(res: ServerResponse, status: number, data: unknown): void {
  res.writeHead(status, { "content-type": "application/json; charset=utf-8" });
  res.end(JSON.stringify(data, null, 2));
}

async function serveStatic(pathname: string, res: ServerResponse): Promise<void> {
  const safePath = pathname.replace(/\.\./g, "");
  const filePath = join(publicDir, safePath);
  const content = await readFile(filePath);
  const contentType = extname(filePath) === ".css" ? "text/css" : extname(filePath) === ".js" ? "text/javascript" : "text/html";
  res.writeHead(200, { "content-type": `${contentType}; charset=utf-8` });
  res.end(content);
}
