import { Message, RuntimeTool, ShortMemory, ToolResult } from "@agent/shared";

export type ModelInput = {
  system: string;
  memory: ShortMemory;
  messages: Message[];
  userMessage: string;
  tools: RuntimeTool[];
  toolResults: ToolResult[];
};

export type ModelOutput = {
  text: string;
};

export interface ChatModel {
  complete(input: ModelInput): Promise<ModelOutput>;
}

export class EchoModel implements ChatModel {
  async complete(input: ModelInput): Promise<ModelOutput> {
    const toolSummary = input.toolResults.length
      ? `\n\nTool results:\n${input.toolResults.map((result) => `- ${result.toolId}: ${JSON.stringify(result.output)}`).join("\n")}`
      : "";
    return {
      text: `I received: ${input.userMessage}\n\nShort memory: ${input.memory.summary}${toolSummary}`
    };
  }
}

export class OpenAICompatibleModel implements ChatModel {
  constructor(
    private readonly config: {
      apiKey: string;
      baseUrl: string;
      model: string;
    }
  ) {}

  async complete(input: ModelInput): Promise<ModelOutput> {
    const response = await fetch(`${this.config.baseUrl.replace(/\/$/, "")}/chat/completions`, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        authorization: `Bearer ${this.config.apiKey}`
      },
      body: JSON.stringify({
        model: this.config.model,
        messages: [
          { role: "system", content: input.system },
          {
            role: "system",
            content: `Short memory:\n${input.memory.summary}\nRecent facts:\n${input.memory.recentFacts.join("\n")}`
          },
          ...input.messages.slice(-10).map((message) => ({
            role: message.role === "tool" ? "system" : message.role,
            content: message.content
          })),
          ...(input.toolResults.length
            ? [{ role: "system", content: `Tool results:\n${JSON.stringify(input.toolResults, null, 2)}` }]
            : []),
          { role: "user", content: input.userMessage }
        ],
        temperature: 0.2
      })
    });

    if (!response.ok) {
      throw new Error(`Model request failed: ${response.status} ${await response.text()}`);
    }
    const body = (await response.json()) as { choices?: Array<{ message?: { content?: string } }> };
    return { text: body.choices?.[0]?.message?.content ?? "" };
  }
}

export function createDefaultModel(): ChatModel {
  const apiKey = process.env.OPENAI_API_KEY;
  const baseUrl = process.env.OPENAI_BASE_URL ?? "https://api.openai.com/v1";
  const model = process.env.OPENAI_MODEL ?? "gpt-4.1-mini";
  if (!apiKey) return new EchoModel();
  return new OpenAICompatibleModel({ apiKey, baseUrl, model });
}

