# Lmy' Harness Agent

Lmy' Harness Agent 是一个本地运行的智能助手系统，用于对话式任务执行、工具调用、知识库问答、多模型对比和深度排疑解惑。项目采用前后端一体的 monorepo 结构：前端提供聊天和配置界面，后端负责 Agent 循环、模型调用、工具运行、知识库检索、MCP 集成和持久化。

## 核心功能

- 对话式 Agent：支持多轮 `用户 -> 模型 -> 工具调用 -> 工具结果 -> 模型 -> 最终回答` 的执行循环。
- 实时流式输出：通过 SSE 推送模型过程、工具事件、最终回答和错误信息。
- 模型配置：支持多个 OpenAI-compatible 推理模型配置，并区分推理模型与向量模型。
- 多模型回答：可选择主模型和最多两个副模型并行回答，再选择用于后续上下文的 canonical answer。
- 本地工具调用：内置 shell、文件读取/编辑、搜索、子 Agent、WebFetch、WebSearch、向用户提问等工具。
- Skill 提示包：支持从 `skills/**/SKILL.md`、`.claude/skills/**/SKILL.md`、用户目录加载 prompt-only skill。
- 本地知识库 RAG：支持知识库创建、文件导入、文本切块、关键词召回、向量召回和混合检索。
- MCP 集成：从 `.mcp.json` 和兼容配置中发现 MCP server，并将 MCP tool 注册到运行时。
- 记忆与追踪：会话、消息、短期记忆、工作记忆、trace、模型配置、工具配置、skill 配置和知识库数据均可持久化。

## 架构概览

```text
apps/web
  TypeScript 单页应用
  聊天页 / Skill 管理 / 知识库管理 / 模型配置 / Markdown 预览

apps/server
  Go + CloudWeGo Hertz HTTP 服务
  静态资源托管 + REST API + SSE 流式接口

apps/server/internal/agent
  多轮 Agent 循环、system prompt 构造、上下文压缩、skill 渐进加载、知识库上下文注入

apps/server/internal/model
  OpenAI-compatible chat completions 与 embeddings 适配器

apps/server/internal/runtime
  工具注册、工具 schema 导出、工具调用分发、风险策略

apps/server/internal/tools
  本地工具实现，包括 shell、文件、grep/glob、子 Agent、WebFetch、WebSearch、AskUserQuestion

apps/server/internal/knowledge
  文件导入、文本抽取、切块、SQLite/FTS5 索引、sqlite-vec 向量索引、混合检索

apps/server/internal/memory
  内存态会话与 SQLite 持久化会话、消息、短期记忆、工作记忆、trace

apps/server/internal/state
  SQLite 状态表：模型配置、工具配置、MCP 配置、知识库元数据、多模型回答记录

apps/server/internal/skills
  Skill 注册、配置、启用/禁用、详情读取

apps/server/internal/mcp
  MCP stdio client、server 初始化、tool 注册

apps/server/internal/claudecode
  启动上下文发现：CLAUDE.md、规则、MCP 配置、skill 目录、settings、auto memory
```

## 关键组件

| 组件 | 作用 |
| --- | --- |
| Web UI | 用户入口，负责对话、选择模型/知识库、管理 skill、知识库和模型配置 |
| HTTP Server | 提供 REST API、SSE 流式回答和前端静态资源托管 |
| Agent Loop | 组织模型输入、工具调用、工具结果、知识库上下文和最终回答 |
| Model Adapter | 将系统内部消息转换为 OpenAI-compatible `/chat/completions` 和 embeddings 请求 |
| Tool Runtime | 管理本地工具和 MCP 工具，输出工具 schema 给模型 |
| Skill Registry | 管理 prompt-only skill，按用户选择、轻量匹配或模型请求渐进加载 |
| Knowledge Store | 管理知识库、文件导入、切块、FTS5 召回、sqlite-vec 召回和检索日志 |
| State Store | 使用 SQLite 保存配置、会话、多模型响应、知识库元数据和运行状态 |
| Memory Store | 保存短期记忆、工作记忆和 trace，支撑跨轮对话上下文 |

## 数据与存储

默认持久化目录在 `apps/server/data`：

- `apps/server/data/state.db`：SQLite 主状态库。
- `apps/server/data/knowledge/files`：导入的原始文件。
- `apps/server/data/knowledge/parsed`：解析后的文本文件。

知识库检索使用：

- SQLite 普通表保存知识库、文档、版本、chunk、ingestion job、检索日志。
- SQLite FTS5 表 `document_chunks_fts` 做关键词召回。
- sqlite-vec 表 `document_chunk_vectors` 做向量召回。
- 父子 chunk 结构：子 chunk 用于召回，父 chunk 用于注入更完整上下文。

## 需要安装的依赖

### 必需

- Go `1.22.5+`
- Node.js `20+`
- npm
- 支持 CGO 的 C/C++ 编译工具链
- SQLite/FTS5/sqlite-vec 相关 Go 依赖会通过 Go module 下载，但需要 CGO 工具链完成编译

macOS：

```bash
xcode-select --install
```

Linux Debian/Ubuntu：

```bash
sudo apt-get update
sudo apt-get install -y build-essential
```

### SQLite、FTS5、sqlite-vec 与 reranker

知识库检索依赖 SQLite、FTS5 和 sqlite-vec：

- SQLite 驱动：`github.com/mattn/go-sqlite3`
- SQLite FTS5：通过 `go-sqlite3` 的 `sqlite_fts5` build tag 启用，用于关键词召回。
- sqlite-vec：`github.com/asg017/sqlite-vec-go-bindings`，用于本地向量索引和向量召回。
- reranker：当前是项目内置重排逻辑，会合并关键词召回、向量召回和元数据召回结果，展开父 chunk，并做多样性排序；不需要额外安装 reranker 服务或模型。

正常启动不需要单独安装 SQLite 或 sqlite-vec 动态库，执行 `npm run dev`、`npm run build`、`npm run check`、`npm run test` 时会使用已配置的 `sqlite_fts5` tag 编译后端：

```bash
npm run dev
npm run build
npm run check
npm run test
```

如果直接运行 Go 命令，必须带上 `sqlite_fts5` tag：

```bash
go test -tags sqlite_fts5 ./apps/server/...
go run -tags sqlite_fts5 ./apps/server
```

可选：如果需要手工检查本地数据库，可以安装 SQLite CLI，但它不是运行服务的必需依赖。

macOS：

```bash
brew install sqlite
sqlite3 --version
```

Linux Debian/Ubuntu：

```bash
sudo apt-get install -y sqlite3
sqlite3 --version
```

### 知识库 PDF 导入需要

PDF 文件导入依赖 Poppler 的 `pdftotext`。

macOS：

```bash
brew install poppler
pdftotext -v
```

Linux Debian/Ubuntu：

```bash
sudo apt-get install -y poppler-utils
pdftotext -v
```

### 模型服务

系统默认使用 OpenAI-compatible API。至少需要配置一个推理模型 API key。向量检索需要额外配置 embedding 模型。

推理模型环境变量：

```bash
export OPENAI_API_KEY=...
export OPENAI_BASE_URL=https://example.com/api/v3
export OPENAI_MODEL=...
```

向量模型环境变量：

```bash
export OPENAI_EMBEDDING_API_KEY=...
export OPENAI_EMBEDDING_BASE_URL=https://example.com/api/v3
export OPENAI_EMBEDDING_MODEL=...
```

兼容变量：

- `ARK_API_KEY` 可作为推理模型 API key fallback。
- `ARK_EMBEDDING_API_KEY` 可作为 embedding API key fallback。

模型配置也可以在系统启动后通过“模型配置”页面维护，配置会写入 SQLite。

## 启动方式

1. 安装 Node 依赖：

```bash
npm install
```

2. 配置模型环境变量，或准备启动后在页面中配置模型。

3. 启动开发服务：

```bash
npm run dev
```

4. 打开浏览器：

```text
http://127.0.0.1:3000/
```

服务默认监听 `127.0.0.1:3000`。可以通过环境变量修改：

```bash
ADDR=127.0.0.1:3001 npm run dev
```

如需指定前端构建产物目录：

```bash
WEB_DIST_DIR=apps/web/dist npm run dev
```

## 构建与检查

构建前端和后端：

```bash
npm run build
```

仅构建前端：

```bash
npm run build:web
```

仅构建后端：

```bash
npm run build:server
```

运行检查：

```bash
npm run check
```

运行后端测试：

```bash
npm run test
```

清理构建产物：

```bash
npm run clean
```

如果直接运行 Go 命令，需要带上 `sqlite_fts5` tag，并建议把缓存放到 `/tmp`：

```bash
GOTOOLCHAIN=go1.25.11 GOMODCACHE=/tmp/lmy-gomod-cache GOCACHE=/tmp/lmy-go-cache GOTMPDIR=/tmp go test -tags sqlite_fts5 ./apps/server/...
GOTOOLCHAIN=go1.25.11 GOMODCACHE=/tmp/lmy-gomod-cache GOCACHE=/tmp/lmy-go-cache GOTMPDIR=/tmp go run -tags sqlite_fts5 ./apps/server
```

## 常用 API

- `GET /health`
- `GET /api/conversations`
- `POST /api/conversations`
- `GET /api/conversations/:id/messages`
- `POST /api/conversations/:id/chat`
- `POST /api/conversations/:id/chat/stream`
- `GET /api/conversations/:id/traces`
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

## 目录结构速查

```text
.
├── apps
│   ├── server
│   │   ├── main.go
│   │   └── internal
│   │       ├── agent
│   │       ├── claudecode
│   │       ├── contracts
│   │       ├── http
│   │       ├── knowledge
│   │       ├── mcp
│   │       ├── memory
│   │       ├── model
│   │       ├── runtime
│   │       ├── skills
│   │       ├── state
│   │       └── tools
│   └── web
│       └── src
├── skills
├── package.json
├── go.mod
└── README.md
```
