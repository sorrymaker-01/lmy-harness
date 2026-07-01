import { RuntimeTool, ToolResult, RiskLevel } from "@agent/shared";
import { z, ZodTypeAny } from "zod";

export type SkillContext = {
  conversationId: string;
};

export interface Skill<I = unknown, O = unknown> {
  name: string;
  description: string;
  inputSchema: ZodTypeAny;
  risk: RiskLevel;
  execute(input: I, ctx: SkillContext): Promise<O>;
}

export class SkillRegistry {
  private readonly skills = new Map<string, Skill>();

  register(skill: Skill): void {
    if (this.skills.has(skill.name)) {
      throw new Error(`Skill already registered: ${skill.name}`);
    }
    this.skills.set(skill.name, skill);
  }

  listTools(): RuntimeTool[] {
    return [...this.skills.values()].map((skill) => ({
      id: `skill:${skill.name}`,
      source: "skill",
      name: skill.name,
      description: skill.description,
      inputSchema: zodToJsonLike(skill.inputSchema),
      risk: skill.risk
    }));
  }

  async invoke(toolId: string, input: unknown, ctx: SkillContext): Promise<ToolResult> {
    const name = toolId.replace(/^skill:/, "");
    const skill = this.skills.get(name);
    if (!skill) {
      return { toolId, ok: false, output: null, error: `Unknown skill: ${name}` };
    }

    const parsed = skill.inputSchema.safeParse(input);
    if (!parsed.success) {
      return {
        toolId,
        ok: false,
        output: null,
        error: parsed.error.issues.map((issue) => issue.message).join("; ")
      };
    }

    try {
      const output = await skill.execute(parsed.data, ctx);
      return { toolId, ok: true, output };
    } catch (error) {
      return { toolId, ok: false, output: null, error: error instanceof Error ? error.message : String(error) };
    }
  }
}

export function createDefaultSkillRegistry(): SkillRegistry {
  const registry = new SkillRegistry();

  registry.register({
    name: "datetime",
    description: "Return the current date and time.",
    risk: "low",
    inputSchema: z.object({ timezone: z.string().optional() }),
    async execute(input: { timezone?: string }) {
      const now = new Date();
      return {
        iso: now.toISOString(),
        local: input.timezone
          ? new Intl.DateTimeFormat("zh-CN", { dateStyle: "full", timeStyle: "long", timeZone: input.timezone }).format(now)
          : now.toLocaleString()
      };
    }
  });

  registry.register({
    name: "calculator",
    description: "Evaluate a simple arithmetic expression containing numbers and + - * / ( ).",
    risk: "low",
    inputSchema: z.object({ expression: z.string().min(1) }),
    async execute(input: { expression: string }) {
      if (!/^[\d\s+\-*/().%]+$/.test(input.expression)) {
        throw new Error("Expression contains unsupported characters.");
      }
      // The character whitelist above limits this to arithmetic.
      const result = Function(`"use strict"; return (${input.expression});`)() as unknown;
      if (typeof result !== "number" || !Number.isFinite(result)) {
        throw new Error("Expression did not produce a finite number.");
      }
      return { result };
    }
  });

  registry.register({
    name: "text_transform",
    description: "Transform text into summary, bullet points, uppercase, lowercase, or markdown.",
    risk: "low",
    inputSchema: z.object({
      text: z.string().min(1),
      mode: z.enum(["summary", "bullets", "uppercase", "lowercase", "markdown"])
    }),
    async execute(input: { text: string; mode: "summary" | "bullets" | "uppercase" | "lowercase" | "markdown" }) {
      if (input.mode === "uppercase") return { text: input.text.toUpperCase() };
      if (input.mode === "lowercase") return { text: input.text.toLowerCase() };
      const sentences = input.text.split(/(?<=[。.!?])\s+/).filter(Boolean);
      if (input.mode === "summary") return { text: sentences.slice(0, 2).join(" ") || input.text.slice(0, 240) };
      if (input.mode === "bullets") return { text: sentences.slice(0, 5).map((line) => `- ${line}`).join("\n") };
      return { text: `## Result\n\n${input.text}` };
    }
  });

  return registry;
}

function zodToJsonLike(schema: ZodTypeAny): unknown {
  const shape = (schema as z.AnyZodObject).shape;
  if (!shape) return { type: "object" };
  return {
    type: "object",
    properties: Object.fromEntries(Object.entries(shape).map(([key, value]) => [key, { type: (value as ZodTypeAny)._def.typeName }])),
    required: Object.entries(shape)
      .filter(([, value]) => !(value as ZodTypeAny).isOptional())
      .map(([key]) => key)
  };
}

