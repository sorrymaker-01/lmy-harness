import { createId, Message, nowIso, ShortMemory, ToolResult } from "@agent/shared";

export interface ShortMemoryStore {
  get(conversationId: string): Promise<ShortMemory>;
  updateFromTurn(input: {
    conversationId: string;
    userMessage: string;
    assistantAnswer: string;
    recentMessages: Message[];
    toolResults: ToolResult[];
  }): Promise<ShortMemory>;
}

export class InMemoryShortMemoryStore implements ShortMemoryStore {
  private readonly memories = new Map<string, ShortMemory>();

  async get(conversationId: string): Promise<ShortMemory> {
    const existing = this.memories.get(conversationId);
    if (existing) return existing;

    const created: ShortMemory = {
      id: createId("mem"),
      conversationId,
      summary: "No prior short-term memory.",
      recentFacts: [],
      updatedAt: nowIso()
    };
    this.memories.set(conversationId, created);
    return created;
  }

  async updateFromTurn(input: {
    conversationId: string;
    userMessage: string;
    assistantAnswer: string;
    recentMessages: Message[];
    toolResults: ToolResult[];
  }): Promise<ShortMemory> {
    const previous = await this.get(input.conversationId);
    const toolFacts = input.toolResults
      .filter((result) => result.ok)
      .map((result) => `Tool ${result.toolId} returned ${compact(result.output)}.`);

    const newestFacts = [
      ...previous.recentFacts,
      `User asked: ${trimTo(input.userMessage, 160)}`,
      `Assistant answered: ${trimTo(input.assistantAnswer, 180)}`,
      ...toolFacts
    ].slice(-8);

    const activeTask = inferActiveTask(input.userMessage, previous.activeTask);
    const summary = [
      previous.summary === "No prior short-term memory." ? "" : previous.summary,
      `Latest turn: user asked "${trimTo(input.userMessage, 120)}"; assistant replied "${trimTo(input.assistantAnswer, 140)}".`
    ]
      .filter(Boolean)
      .join("\n")
      .slice(-1400);

    const updated: ShortMemory = {
      ...previous,
      summary,
      recentFacts: newestFacts,
      activeTask,
      updatedAt: nowIso()
    };
    this.memories.set(input.conversationId, updated);
    return updated;
  }
}

function inferActiveTask(userMessage: string, previous?: string): string | undefined {
  const normalized = userMessage.trim();
  if (!normalized) return previous;
  if (/帮我|请|生成|创建|实现|写|查|分析/.test(normalized)) {
    return trimTo(normalized, 180);
  }
  return previous;
}

function compact(value: unknown): string {
  if (typeof value === "string") return trimTo(value, 180);
  try {
    return trimTo(JSON.stringify(value), 180);
  } catch {
    return String(value);
  }
}

function trimTo(value: string, max: number): string {
  return value.length > max ? `${value.slice(0, max - 1)}...` : value;
}

