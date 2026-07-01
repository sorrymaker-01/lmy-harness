# Vertical Agent MCP MVP

TypeScript MVP for a conversational Agent system with short-term memory, Skill support, MCP tool integration, tool policy, and trace logging. RAG and long-term memory are intentionally out of scope.

## Scripts

- `npm install`
- `npm run build`
- `npm run dev`

The API server serves the web shell at `http://localhost:3000`.

## Scope

Implemented MVP:

- Conversational Agent loop
- Short-term memory
- Skill Engine with `datetime`, `calculator`, `text_transform`
- MCP Runtime skeleton for stdio MCP servers
- Unified Tool Registry
- Tool policy: low auto-run, medium requires confirmation, high denied
- Trace logging
- Static Web shell for chat, tools, and latest trace

Out of scope for this MVP:

- RAG
- Long-term memory
- Vector DB
- Knowledge Feed
- Multi-model parallel answers
- Complex workflow orchestration

## Useful Endpoints

- `GET /health`
- `GET /api/conversations`
- `POST /api/conversations`
- `GET /api/conversations/:id/messages`
- `POST /api/conversations/:id/chat`
- `GET /api/conversations/:id/traces`
- `GET /api/tools`
- `GET /api/mcp/servers`
- `POST /api/mcp/servers`
