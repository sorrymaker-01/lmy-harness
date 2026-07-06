# Local Claude Code

Monorepo for a local Claude Code style agent:

- Frontend: TypeScript chat UI in `apps/web`
- Backend: Go + CloudWeGo Hertz in `apps/server`

The backend runs an agentic loop with model calls, tool calls, prompt-only skills, project context loading, MCP configuration discovery, SQLite-backed sessions, short memory, working memory, and trace streaming.

## Scripts

- `npm install`
- `npm run build`
- `npm run dev`
- `npm run check`

The Go server serves the built frontend and API at `http://127.0.0.1:3000`.

Model credentials are stored in SQLite and can be managed from the model configuration page. On first startup the default row is seeded from environment variables, which also remain as a fallback:

```bash
export OPENAI_API_KEY=...
export OPENAI_BASE_URL=https://example.com/api/v3
export OPENAI_MODEL=...
npm run dev
```

`ARK_API_KEY` is also accepted as an API key fallback. Persistent state lives in `apps/server/data/state.db`.

## Structure

- `apps/web`: TypeScript chat UI, built to `apps/web/dist`
- `apps/server`: Go backend entrypoint
- `apps/server/internal/http`: Hertz HTTP/SSE transport and composition root
- `apps/server/internal/agent`: multi-round agent loop, prompt construction, compaction, skill loading
- `apps/server/internal/claudecode`: Claude Code style startup context discovery
- `apps/server/internal/runtime`: local tool registry, schema export, invocation dispatch, risk policy
- `apps/server/internal/tools`: CoreCoder-derived tools plus generic and web tools
- `apps/server/internal/skills`: prompt-only skill registry, file skill loader, skill configuration
- `apps/server/internal/memory`: in-memory state plus SQLite-backed conversation persistence
- `apps/server/internal/model`: OpenAI-compatible chat completions adapter
- `apps/server/internal/contracts`: backend DTOs and stream/trace schemas

## Claude Code Style Behavior

- Multi-round loop: `user -> model -> tool calls -> tool results -> model ... -> final answer`
- Frontend SSE stream shows intermediate model output as collapsible thinking text and final answer separately
- Startup context loads `CLAUDE.md`, `.claude/CLAUDE.md`, `CLAUDE.local.md`, unscoped `.claude/rules/*.md`, user rules, settings, auto memory, MCP config, and skill directories
- Sessions, messages, agent traces, model config, tool config, skill config, and MCP server config are persisted in `apps/server/data/state.db`
- Skills are prompt packages, not tools. Project skills are discovered from `skills/**/SKILL.md` and `.claude/skills/**/SKILL.md`; personal skills are discovered from `~/.claude/skills/**/SKILL.md`
- Skill metadata is visible up front; full `SKILL.md` content and support resources are loaded progressively after slash selection, lightweight matching, or model `<load_skill ...>` request
- MCP config is discovered from `.mcp.json` and Claude config paths and is kept separate from skills
- Context compaction first trims older tool outputs, then summarizes older messages, then preserves recent turns and loaded skill context

## Useful Endpoints

- `GET /health`
- `GET /api/conversations`
- `POST /api/conversations`
- `GET /api/conversations/:id/messages`
- `POST /api/conversations/:id/chat`
- `POST /api/conversations/:id/chat/stream`
- `GET /api/conversations/:id/traces`
- `GET /api/model/config`
- `PUT /api/model/config`
- `GET /api/model/configs`
- `PUT /api/model/configs/:id`
- `DELETE /api/model/configs/:id`
- `GET /api/tools`
- `GET /api/tools/config`
- `PUT /api/tools/config`
- `GET /api/mcp/servers`
- `GET /api/mcp/servers/config`
- `PUT /api/mcp/servers/config`
- `GET /api/skills`
- `PUT /api/skills/config`
- `GET /api/skills/:name`
- `DELETE /api/skills/:name`
- `GET /api/knowledge`
- `POST /api/knowledge/import`
- `DELETE /api/knowledge/:id`
