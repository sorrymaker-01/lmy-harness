export type Role = "system" | "user" | "assistant" | "tool";

export type RiskLevel = "low" | "medium" | "high";

export type Conversation = {
  id: string;
  title: string;
  createdAt: string;
  updatedAt: string;
};

export type Message = {
  id: string;
  conversationId: string;
  role: Role;
  content: string;
  createdAt: string;
  metadata?: Record<string, unknown>;
};

export type ShortMemory = {
  id: string;
  conversationId: string;
  summary: string;
  recentFacts: string[];
  activeTask?: string;
  updatedAt: string;
};

export type RuntimeToolSource = "skill" | "mcp";

export type RuntimeTool = {
  id: string;
  source: RuntimeToolSource;
  name: string;
  description: string;
  inputSchema: unknown;
  risk: RiskLevel;
};

export type ToolCall = {
  id: string;
  toolId: string;
  input: unknown;
};

export type ToolResult = {
  toolId: string;
  output: unknown;
  ok: boolean;
  error?: string;
};

export type AgentTrace = {
  id: string;
  conversationId: string;
  userMessageId: string;
  startedAt: string;
  completedAt?: string;
  memorySnapshot?: ShortMemory;
  availableTools: RuntimeTool[];
  toolCalls: ToolCall[];
  toolResults: ToolResult[];
  finalAnswer?: string;
  error?: string;
};

export type ToolPolicyDecision =
  | { action: "allow" }
  | { action: "confirm"; reason: string }
  | { action: "deny"; reason: string };

export function nowIso(): string {
  return new Date().toISOString();
}

export function createId(prefix: string): string {
  return `${prefix}_${crypto.randomUUID().replaceAll("-", "")}`;
}

