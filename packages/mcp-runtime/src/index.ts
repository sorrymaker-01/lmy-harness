import { RuntimeTool, ToolResult } from "@agent/shared";

export type McpServerConfig = {
  id: string;
  name: string;
  transport: "stdio" | "http";
  command?: string;
  args?: string[];
  url?: string;
  enabled: boolean;
};

type CachedTool = RuntimeTool & {
  serverId: string;
  originalName: string;
};

export class McpRuntime {
  private readonly servers = new Map<string, McpServerConfig>();
  private readonly cachedTools = new Map<string, CachedTool>();

  registerServer(config: McpServerConfig): void {
    this.servers.set(config.id, config);
  }

  listServers(): McpServerConfig[] {
    return [...this.servers.values()];
  }

  async refreshTools(): Promise<RuntimeTool[]> {
    this.cachedTools.clear();
    for (const server of this.servers.values()) {
      if (!server.enabled) continue;
      const tools = await this.discoverServerTools(server);
      for (const tool of tools) {
        this.cachedTools.set(tool.id, tool);
      }
    }
    return this.listTools();
  }

  listTools(): RuntimeTool[] {
    return [...this.cachedTools.values()].map(({ serverId: _serverId, originalName: _originalName, ...tool }) => tool);
  }

  async invoke(toolId: string, input: unknown): Promise<ToolResult> {
    const tool = this.cachedTools.get(toolId);
    if (!tool) {
      return { toolId, ok: false, output: null, error: `Unknown MCP tool: ${toolId}` };
    }
    const server = this.servers.get(tool.serverId);
    if (!server) {
      return { toolId, ok: false, output: null, error: `Unknown MCP server: ${tool.serverId}` };
    }
    return this.callServerTool(server, tool.originalName, input, toolId);
  }

  private async discoverServerTools(server: McpServerConfig): Promise<CachedTool[]> {
    if (server.transport !== "stdio") {
      return [];
    }
    if (!server.command) {
      return [];
    }

    // Dynamic import keeps the app usable when no MCP dependency is installed yet.
    const [{ Client }, { StdioClientTransport }] = await Promise.all([
      import("@modelcontextprotocol/sdk/client/index.js"),
      import("@modelcontextprotocol/sdk/client/stdio.js")
    ]);
    const client = new Client({ name: "vertical-agent-mvp", version: "0.1.0" });
    const transport = new StdioClientTransport({ command: server.command, args: server.args ?? [] });
    await client.connect(transport);
    try {
      const response = await client.listTools();
      return (response.tools ?? []).map((tool: { name: string; description?: string; inputSchema?: unknown }) => ({
        id: `mcp:${server.id}:${tool.name}`,
        source: "mcp",
        name: `${server.name}.${tool.name}`,
        description: tool.description ?? `MCP tool ${tool.name} from ${server.name}`,
        inputSchema: tool.inputSchema ?? { type: "object" },
        risk: "medium",
        serverId: server.id,
        originalName: tool.name
      }));
    } finally {
      await client.close();
    }
  }

  private async callServerTool(
    server: McpServerConfig,
    toolName: string,
    input: unknown,
    toolId: string
  ): Promise<ToolResult> {
    if (server.transport !== "stdio" || !server.command) {
      return { toolId, ok: false, output: null, error: "Only stdio MCP servers are implemented in MVP." };
    }

    try {
      const [{ Client }, { StdioClientTransport }] = await Promise.all([
        import("@modelcontextprotocol/sdk/client/index.js"),
        import("@modelcontextprotocol/sdk/client/stdio.js")
      ]);
      const client = new Client({ name: "vertical-agent-mvp", version: "0.1.0" });
      const transport = new StdioClientTransport({ command: server.command, args: server.args ?? [] });
      await client.connect(transport);
      try {
        const output = await client.callTool({ name: toolName, arguments: asRecord(input) });
        return { toolId, ok: true, output };
      } finally {
        await client.close();
      }
    } catch (error) {
      return { toolId, ok: false, output: null, error: error instanceof Error ? error.message : String(error) };
    }
  }
}

function asRecord(value: unknown): Record<string, unknown> {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as Record<string, unknown>;
  }
  return {};
}
