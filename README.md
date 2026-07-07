# Lmy' Harness Agent

Monorepo for a local task-execution and troubleshooting agent:

- Frontend: TypeScript chat UI in `apps/web`
- Backend: Go + CloudWeGo Hertz in `apps/server`

The backend runs an agentic loop with model calls, tool calls, prompt-only skills, project context loading, MCP configuration discovery, SQLite-backed sessions, short memory, working memory, and trace streaming.

## Requirements

- Go 1.22.5+
- Node.js 20+
- Poppler `pdftotext` for PDF knowledge import
- CGO-capable C toolchain for the embedded sqlite-vec binding

Install Poppler locally before importing PDFs:

```bash
brew install poppler
pdftotext -v
```

## Scripts

- `npm install`
- `npm run build`
- `npm run dev`
- `npm run check`

The Go server serves the built frontend and API at `http://127.0.0.1:3000`.

Model credentials are stored in SQLite and can be managed from the model configuration page. Each model config has a type:

- `reasoning`: chat/completions model used by conversations
- `embedding`: embeddings model used by sqlite-vec vector indexing

Reasoning and embedding configs use separate API keys and base URLs. On first startup the default reasoning row is seeded from environment variables, which also remain as a fallback:

```bash
export OPENAI_API_KEY=...
export OPENAI_BASE_URL=https://example.com/api/v3
export OPENAI_MODEL=...
```

An optional default embedding row is seeded separately:

```bash
export OPENAI_EMBEDDING_API_KEY=...
export OPENAI_EMBEDDING_BASE_URL=https://example.com/api/v3
export OPENAI_EMBEDDING_MODEL=...
npm run dev
```

`ARK_API_KEY` is accepted as a reasoning API key fallback, and `ARK_EMBEDDING_API_KEY` is accepted as an embedding API key fallback. Persistent state lives in `apps/server/data/state.db`, imported knowledge files live under `apps/server/data/knowledge/files`, and parsed knowledge text lives under `apps/server/data/knowledge/parsed`.

## Local RAG

The RAG path uses:

- SQLite tables in `apps/server/data/state.db` for knowledge bases, documents, document versions, chunks, ingestion jobs, index outbox, and retrieval logs
- SQLite FTS5 virtual table `document_chunks_fts` for keyword recall
- sqlite-vec virtual table `document_chunk_vectors` for vector recall when an embedding model is configured
- `document_chunk_vector_rows` for vector payload, deletion state, and knowledge-base/document filters
- `vector_index_state` for sqlite-vec backend metadata such as vector dimension and distance metric
- sqlite-vec is statically linked into the Go server through CGO; no separate vector database process is required
- Parent-child chunks: child chunks are used for recall, parent chunks are injected into the agent prompt for fuller context
- PDF files are extracted through Poppler `pdftotext`; text-like formats such as Markdown, TXT, JSON, CSV, YAML, and XML are indexed directly
- Knowledge import accepts files up to 256MiB; the Hertz request body limit is set to 320MiB to allow multipart upload overhead

The Go server is built with the `sqlite_fts5` tag by the npm scripts. If running Go commands directly, include the same tag and keep the module/build caches under `/tmp`:

```bash
GOTOOLCHAIN=go1.25.11 GOMODCACHE=/tmp/lmy-gomod-cache GOCACHE=/tmp/lmy-go-cache GOTMPDIR=/tmp go test -tags sqlite_fts5 ./apps/server/...
GOTOOLCHAIN=go1.25.11 GOMODCACHE=/tmp/lmy-gomod-cache GOCACHE=/tmp/lmy-go-cache GOTMPDIR=/tmp go run -tags sqlite_fts5 ./apps/server
```

## Structure

- `apps/web`: TypeScript chat UI, built to `apps/web/dist`
- `apps/server`: Go backend entrypoint
- `apps/server/internal/http`: Hertz HTTP/SSE transport and composition root
- `apps/server/internal/agent`: multi-round agent loop, prompt construction, compaction, skill loading
- `apps/server/internal/claudecode`: startup context discovery for CLAUDE.md, rules, MCP config, and skill directories
- `apps/server/internal/runtime`: local tool registry, schema export, invocation dispatch, risk policy
- `apps/server/internal/tools`: CoreCoder-derived tools plus generic and web tools
- `apps/server/internal/skills`: prompt-only skill registry, file skill loader, skill configuration
- `apps/server/internal/memory`: in-memory state plus SQLite-backed conversation persistence
- `apps/server/internal/model`: OpenAI-compatible chat completions adapter
- `apps/server/internal/knowledge`: document import, SQLite chunk/FTS5 indexing, sqlite-vec vector indexing, hybrid retrieval
- `apps/server/internal/contracts`: backend DTOs and stream/trace schemas

## Agent Behavior

- Multi-round loop: `user -> model -> tool calls -> tool results -> model ... -> final answer`
- Frontend SSE stream shows intermediate model output as collapsible thinking text and final answer separately
- Startup context loads `CLAUDE.md`, `.claude/CLAUDE.md`, `CLAUDE.local.md`, unscoped `.claude/rules/*.md`, user rules, settings, auto memory, MCP config, and skill directories
- Sessions, messages, agent traces, model config, tool config, skill config, MCP server config, and imported knowledge files are persisted under `apps/server/data`
- Skills are prompt packages, not tools. Project skills are discovered from `skills/**/SKILL.md` and `.claude/skills/**/SKILL.md`; personal skills are discovered from `~/.claude/skills/**/SKILL.md`
- Skill metadata is visible up front; full `SKILL.md` content and support resources are loaded progressively after slash selection, lightweight matching, or model `<load_skill ...>` request
- MCP config is discovered from `.mcp.json` and compatible config paths and is kept separate from skills
- Context compaction first trims older tool outputs, then summarizes older messages, then preserves recent turns and loaded skill context
- Imported knowledge is retrieved before each model loop and injected as ordinary prompt context, not as a tool result and not as a skill

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
- `GET /api/knowledge-bases`
- `POST /api/knowledge-bases`
- `DELETE /api/knowledge-bases/:id`
- `GET /api/knowledge`
- `POST /api/knowledge/import`
- `POST /api/knowledge/search`
- `DELETE /api/knowledge/:id`
