import { ShortMemoryStore } from "@agent/memory";
import { McpRuntime } from "@agent/mcp-runtime";
import { ChatModel } from "@agent/model-router";
import { SkillRegistry } from "@agent/skill-engine";
import {
  AgentTrace,
  createId,
  Message,
  nowIso,
  RuntimeTool,
  ToolCall,
  ToolPolicyDecision,
  ToolResult
} from "@agent/shared";

export interface ConversationStore {
  addMessage(message: Message): Promise<void>;
  getRecentMessages(conversationId: string, limit: number): Promise<Message[]>;
  addTrace(trace: AgentTrace): Promise<void>;
  updateTrace(trace: AgentTrace): Promise<void>;
}

export type AgentRunInput = {
  conversationId: string;
  userMessage: string;
  requireConfirmation?: boolean;
};

export type AgentRunOutput = {
  userMessage: Message;
  assistantMessage: Message;
  trace: AgentTrace;
};

export class AgentRuntime {
  constructor(
    private readonly deps: {
      store: ConversationStore;
      memory: ShortMemoryStore;
      skills: SkillRegistry;
      mcp: McpRuntime;
      model: ChatModel;
    }
  ) {}

  async run(input: AgentRunInput): Promise<AgentRunOutput> {
    const userMessage: Message = {
      id: createId("msg"),
      conversationId: input.conversationId,
      role: "user",
      content: input.userMessage,
      createdAt: nowIso()
    };
    await this.deps.store.addMessage(userMessage);

    const memory = await this.deps.memory.get(input.conversationId);
    const recentMessages = await this.deps.store.getRecentMessages(input.conversationId, 12);
    const availableTools = await this.listTools();
    const toolCalls = decideToolCalls(input.userMessage, availableTools);
    const trace: AgentTrace = {
      id: createId("trace"),
      conversationId: input.conversationId,
      userMessageId: userMessage.id,
      startedAt: nowIso(),
      memorySnapshot: memory,
      availableTools,
      toolCalls,
      toolResults: []
    };
    await this.deps.store.addTrace(trace);

    try {
      const toolResults: ToolResult[] = [];
      for (const call of toolCalls) {
        const tool = availableTools.find((candidate) => candidate.id === call.toolId);
        if (!tool) {
          toolResults.push({ toolId: call.toolId, ok: false, output: null, error: "Tool not found." });
          continue;
        }
        const policy = decidePolicy(tool);
        if (policy.action === "deny") {
          toolResults.push({ toolId: call.toolId, ok: false, output: null, error: policy.reason });
          continue;
        }
        if (policy.action === "confirm" && !input.requireConfirmation) {
          toolResults.push({
            toolId: call.toolId,
            ok: false,
            output: null,
            error: `Confirmation required: ${policy.reason}`
          });
          continue;
        }
        toolResults.push(await this.invokeTool(call, input.conversationId));
      }

      const answer = await this.deps.model.complete({
        system: systemPrompt(),
        memory,
        messages: recentMessages,
        userMessage: input.userMessage,
        tools: availableTools,
        toolResults
      });

      const assistantMessage: Message = {
        id: createId("msg"),
        conversationId: input.conversationId,
        role: "assistant",
        content: answer.text,
        createdAt: nowIso(),
        metadata: { toolResults }
      };
      await this.deps.store.addMessage(assistantMessage);
      await this.deps.memory.updateFromTurn({
        conversationId: input.conversationId,
        userMessage: input.userMessage,
        assistantAnswer: answer.text,
        recentMessages,
        toolResults
      });

      trace.toolResults = toolResults;
      trace.finalAnswer = answer.text;
      trace.completedAt = nowIso();
      await this.deps.store.updateTrace(trace);

      return { userMessage, assistantMessage, trace };
    } catch (error) {
      trace.error = error instanceof Error ? error.message : String(error);
      trace.completedAt = nowIso();
      await this.deps.store.updateTrace(trace);
      throw error;
    }
  }

  async listTools(): Promise<RuntimeTool[]> {
    const mcpTools = await this.deps.mcp.refreshTools().catch(() => []);
    return [...this.deps.skills.listTools(), ...mcpTools];
  }

  private async invokeTool(call: ToolCall, conversationId: string): Promise<ToolResult> {
    if (call.toolId.startsWith("skill:")) {
      return this.deps.skills.invoke(call.toolId, call.input, { conversationId });
    }
    if (call.toolId.startsWith("mcp:")) {
      return this.deps.mcp.invoke(call.toolId, call.input);
    }
    return { toolId: call.toolId, ok: false, output: null, error: "Unsupported tool source." };
  }
}

function decidePolicy(tool: RuntimeTool): ToolPolicyDecision {
  if (tool.risk === "low") return { action: "allow" };
  if (tool.risk === "medium") return { action: "confirm", reason: `${tool.name} is a medium-risk tool.` };
  return { action: "deny", reason: `${tool.name} is high-risk and disabled in MVP.` };
}

function decideToolCalls(userMessage: string, tools: RuntimeTool[]): ToolCall[] {
  const lower = userMessage.toLowerCase();
  const calls: ToolCall[] = [];

  const datetime = tools.find((tool) => tool.id === "skill:datetime");
  if (datetime && /(几点|时间|日期|today|time|date)/i.test(userMessage)) {
    calls.push({ id: createId("call"), toolId: datetime.id, input: { timezone: "Asia/Shanghai" } });
  }

  const calculator = tools.find((tool) => tool.id === "skill:calculator");
  const expression = extractArithmeticExpression(userMessage);
  if (calculator && expression) {
    calls.push({ id: createId("call"), toolId: calculator.id, input: { expression } });
  }

  const transform = tools.find((tool) => tool.id === "skill:text_transform");
  if (transform && /(总结|bullet|要点|markdown|大写|小写|uppercase|lowercase|summary)/i.test(userMessage)) {
    calls.push({
      id: createId("call"),
      toolId: transform.id,
      input: {
        text: userMessage,
        mode: lower.includes("markdown") ? "markdown" : lower.includes("uppercase") || userMessage.includes("大写") ? "uppercase" : lower.includes("lowercase") || userMessage.includes("小写") ? "lowercase" : userMessage.includes("要点") || lower.includes("bullet") ? "bullets" : "summary"
      }
    });
  }

  if (/mcp|工具|tool/i.test(userMessage)) {
    const mcpTool = tools.find((tool) => tool.source === "mcp");
    if (mcpTool) {
      calls.push({ id: createId("call"), toolId: mcpTool.id, input: {} });
    }
  }

  return calls.slice(0, 3);
}

function extractArithmeticExpression(text: string): string | undefined {
  const matches = text.match(/[0-9][0-9\s+\-*/().%]{2,}[0-9)]/);
  return matches?.[0]?.trim();
}

function systemPrompt(): string {
  return [
    "You are a conversational Agent MVP.",
    "Use short-term memory and tool results when relevant.",
    "Do not claim access to RAG or long-term memory.",
    "If a tool result is unavailable because confirmation is required, explain that clearly."
  ].join("\n");
}

