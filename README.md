# DOW Mind

> Forked from [enowdev/enowx-rag](https://github.com/enowdev/enowx-rag) (Apache 2.0) — rebranded, hardened, and extended by [DOW Project](https://dowproject.my.id). See [NOTICE](NOTICE) for attribution.

[![CI](https://github.com/RivaldiMurpia/dow-mind/actions/workflows/ci.yml/badge.svg)](https://github.com/RivaldiMurpia/dow-mind/actions/workflows/ci.yml)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go 1.26+](https://img.shields.io/badge/Go-1.26+-00ADD8.svg?logo=go)](mcp-server/go.mod)

Per-project RAG memory MCP server. Each project gets its own vector collection, so an LLM can index context about a codebase and retrieve it quickly.

![dow-mind dashboard — Overview](docs/screenshots/overview.png)

The server runs in **two modes** from a single binary:

- **MCP stdio mode** (default): `dow-mind` — talks to AI coding tools over stdio via the Model Context Protocol.
- **HTTP serve mode**: `dow-mind --serve` — starts an HTTP server with a REST API, SSE event stream, an embedded React dashboard, **and MCP over HTTP at `/mcp`** so agents can connect to dow-mind as a remote daemon (e.g. on a VPS).

### Remote daemon (MCP over HTTP)

Run `dow-mind --serve` on a host and connect agents remotely. Set `DOWMIND_ADMIN_TOKEN` to secure it — it gates both `/api/*` and `/mcp` with a bearer token (unset = no auth, only for trusted networks). Put a TLS reverse proxy in front for public use.

```bash
DOWMIND_ADMIN_TOKEN=$(openssl rand -hex 32) ./dow-mind --serve --addr :7777
```

Point an MCP client at the daemon:

```json
{
  "mcpServers": {
    "dow-mind": {
      "url": "https://rag.example.com/mcp",
      "headers": { "Authorization": "Bearer <DOWMIND_ADMIN_TOKEN>" }
    }
  }
}
```

All MCP tools work identically to local stdio mode. See the **Docs → Remote / daemon** page for details.

---

## Why dow-mind?

LLM coding agents forget everything between sessions and only see the files you
paste. dow-mind gives an agent a **persistent, per-project memory** it can
search — so it recalls your architecture decisions, gotchas, and conventions
without you re-explaining them every time.

- **Per-project isolation.** Each project is its own vector collection
  (`project_<id>`), so an agent working in one repo never retrieves context
  leaked from another. Index a codebase once; query it in isolation.
- **Retrieval that actually finds things.** Dense semantic search, optional
  **hybrid** (dense + lexical RRF), **reranking** (Voyage `rerank-2.5`), and
  near-duplicate **compression** — tunable per query. See the ranked results,
  scores, and matched snippets live in the Playground.
- **Persistent memory, durable metrics.** Design decisions and facts survive
  across sessions and restarts; query latency (p50/p95), token usage, and
  retrieval breakdown are persisted to a local SQLite DB (pure-Go, no cgo).
- **Bring your own stack.** Vector stores: **Qdrant, pgvector, Chroma**.
  Embedders: **Voyage AI, any OpenAI-compatible `/v1/embeddings` API, or
  self-hosted TEI**. Swap models or move stores later with the built-in
  **Migration** tool (re-embeds stored text — no re-scraping your codebase).
- **One binary, two modes.** Local **MCP stdio** for your editor, or
  `--serve` for a **remote daemon** (REST API + dashboard + MCP over HTTP),
  gated by a bearer token — run it centrally on a VPS for a whole team.
- **No lock-in, no toolchain.** A single self-contained binary with the
  dashboard embedded. Install via `curl | sh`, Homebrew, npm, `go install`, or
  a direct download. CGO-free, so it runs anywhere.

### Dashboard preview

The embedded dashboard (`dow-mind --serve`) — real data from a live instance:

| Overview | Retrieval Playground |
|---|---|
| [![Overview](docs/screenshots/overview.png)](docs/screenshots/overview.png) | [![Playground](docs/screenshots/playground.png)](docs/screenshots/playground.png) |
| **Chunks** | **Migration** |
| [![Chunks](docs/screenshots/chunks.png)](docs/screenshots/chunks.png) | [![Migration](docs/screenshots/migration.png)](docs/screenshots/migration.png) |
| **Docs** | **Settings** |
| [![Docs](docs/screenshots/docs.png)](docs/screenshots/docs.png) | [![Settings](docs/screenshots/settings.png)](docs/screenshots/settings.png) |

---

## Install

dow-mind ships as a single self-contained binary (the dashboard is embedded) —
no runtime dependencies, no toolchain required. Pick whichever fits:

**Install script (macOS/Linux)** — downloads the prebuilt binary for your platform:

```bash
curl -fsSL https://raw.githubusercontent.com/RivaldiMurpia/dow-mind/main/install.sh | sh
```

Pin a version or install location with env vars:
`DOWMIND_VERSION=v0.1.0 DOWMIND_INSTALL_DIR=~/.local/bin`.

**Homebrew (macOS/Linux):**

```bash
brew install RivaldiMurpia/tap/dow-mind
```

**npm** (installs the native binary via a thin wrapper; handy if you already use Node):

```bash
npm install -g dow-mind
```

**Go** (if you have Go 1.26+; builds from source, the dashboard is embedded from a committed build):

```bash
go install github.com/RivaldiMurpia/dow-mind/mcp-server/cmd/mcp-server@latest
```

**Prebuilt binaries** for macOS/Linux (amd64/arm64) and Windows (amd64) are
attached to every [GitHub Release](https://github.com/RivaldiMurpia/dow-mind/releases).

Verify with `dow-mind version`, then run `dow-mind --serve` and open
`http://localhost:7777`.

---

## Quick Start (one-command deploy)

### From source

```bash
git clone https://github.com/RivaldiMurpia/dow-mind.git
cd dow-mind
make build && ./dow-mind --serve
```

This builds the React SPA, compiles the Go binary (with the SPA embedded), and starts the HTTP server on port 7777. Open `http://localhost:7777` in your browser.

Set your embedding provider before starting:

```bash
export DOWMIND_VOYAGE_API_KEY=your-voyage-api-key
make build && ./dow-mind --serve
```

### With Docker (all-in-one)

```bash
export DOWMIND_VOYAGE_API_KEY=your-voyage-api-key
docker compose -f docker-compose.all-in-one.yml up -d
```

The dow-mind container serves both the API and UI on port 7777. Qdrant starts automatically as the vector store. See [Docker all-in-one](#docker-all-in-one) below for details.

---

## Two modes of operation

### MCP stdio mode (default)

Run without `--serve` to use the MCP stdio transport. This is the mode you configure in your AI coding tool (Claude Code, Cursor, Cline, etc.):

```bash
./dow-mind
```

MCP tools available: `mind_create_project`, `mind_delete_project`, `mind_index`, `mind_index_project`, `mind_semantic_search`, `mind_retrieve_context`, `mind_list_projects`, `mind_project_exists`, `mind_list_points`, `mind_delete_points`, `mind_stats`. Logs go to stderr (stdout is the MCP protocol stream).

### HTTP serve mode

Run with `--serve` to start the HTTP API + embedded UI:

```bash
# Default port 7777
./dow-mind --serve

# Custom port
./dow-mind --serve --addr :8080

# With admin token auth (protects /api/* endpoints)
DOWMIND_ADMIN_TOKEN=your-secret ./dow-mind --serve
```

| Flag | Default | Description |
| --- | --- | --- |
| `--serve` | `false` | Run as HTTP server instead of stdio MCP |
| `--addr` | `127.0.0.1:7777` | HTTP listen address (only used with `--serve`). Binds localhost by default; pass `--addr :7777` to expose on the network (only with `DOWMIND_ADMIN_TOKEN` set) |

---

## REST API endpoints

When running in `--serve` mode, the following REST endpoints are available:

| Method | Endpoint | Description |
| --- | --- | --- |
| `GET` | `/api/projects` | List all projects with chunk counts |
| `GET` | `/api/projects/{id}` | Get project detail (404 if not found) |
| `GET` | `/api/projects/{id}/points` | List chunks (supports `?source_file=`, `?offset=`, `?limit=`) |
| `DELETE` | `/api/projects/{id}/points/{pointId}` | Delete a single chunk |
| `POST` | `/api/projects/{id}/reindex` | Re-index a project directory (body: `{"directory": "/path"}`) |
| `DELETE` | `/api/projects/{id}` | Delete a project collection |
| `POST` | `/api/search` | Search (body: `{"project_id", "query", "k", "recall", "hybrid", "rerank", "compress"}`) |
| `GET` | `/api/stats` | Aggregate stats (total projects, chunks, embed model) |
| `GET` | `/api/metrics` | Query metrics: latency (avg/p50/p95), token usage, backend, `persistent` flag |
| `GET` | `/api/events` | SSE stream of realtime events (index, search, etc.) |
| `POST` | `/api/setup/test` | Test connectivity (localhost or admin token required) |
| `POST` | `/api/setup/apply` | Save config to `~/.dow-mind/config.yaml` (localhost or admin token required) |
| `GET` | `/api/setup/status` | Check if config exists |
| `POST` | `/api/setup/install-mcp` | Install the MCP server into a client's config (merge + backup) |
| `GET` | `/api/setup/probe` | Report what's installed (MCP per client, skill, AGENTS.md block) for idempotent setup |
| `POST` | `/api/setup/write-agents-md` | Merge the dow-mind block into a project's AGENTS.md (localhost/admin token) |
| `POST` | `/api/migrate` | Migrate/re-embed a project into a new destination (async, SSE progress) |
| `GET` | `/api/docs/setup` | Markdown setup instructions for an AI agent to follow |

Non-API routes serve the embedded React SPA (client-side routing for `/playground`, `/chunks`, `/migration`, `/setup`, etc.).

**Agent-driven setup.** Paste a short prompt into your AI coding agent telling it
to read `GET /api/docs/setup` and follow it. The agent probes what's already in
place (`GET /api/setup/probe`) and installs only the missing pieces — the MCP
server (`POST /api/setup/install-mcp`), the skill, and the project's AGENTS.md
block (`POST /api/setup/write-agents-md`, merged idempotently). The Install step
of the wizard shows the copy-paste prompt.

**Query metrics** are recorded for every search and exposed at `/api/metrics`:
latency percentiles, Voyage token usage (embed + rerank), and — for hybrid
searches on pgvector — the dense/lexical retrieval breakdown. Metrics are
persisted durably to a local SQLite file (`~/.dow-mind/metrics.db`, pure-Go,
no external service), so they survive restarts on any backend. If the file can't
be opened, metrics fall back to in-memory (`"persistent": false`).

Example:

```bash
curl http://localhost:7777/api/projects
curl -X POST http://localhost:7777/api/search \
  -H 'Content-Type: application/json' \
  -d '{"project_id": "my-project", "query": "how does auth work", "k": 5, "hybrid": true, "rerank": true}'
```

---

## Optional admin token auth

When the `DOWMIND_ADMIN_TOKEN` environment variable is set, all `/api/*` endpoints require an `Authorization: Bearer <token>` header. The SPA and static assets are always served without auth.

```bash
# Enable auth
export DOWMIND_ADMIN_TOKEN=my-secret-token
./dow-mind --serve

# API requests must include the token
curl -H "Authorization: Bearer my-secret-token" http://localhost:7777/api/projects
```

When `DOWMIND_ADMIN_TOKEN` is **not set**, no authentication is required (default behavior). This makes it easy to run locally without auth and enable it only when exposing the server to the internet.

---

## Docker all-in-one

The `docker-compose.all-in-one.yml` file at the repository root starts the dow-mind server (API + UI) alongside Qdrant in a single command:

```bash
export DOWMIND_VOYAGE_API_KEY=your-voyage-api-key
docker compose -f docker-compose.all-in-one.yml up -d
```

- dow-mind container serves API + SPA on port **7777**
- Qdrant starts automatically on port 6333
- PostgreSQL (pgvector) and TEI are available as commented-out services in the compose file
- Set `DOWMIND_ADMIN_TOKEN` in the compose environment to enable auth

To use pgvector instead of Qdrant, uncomment the `postgres` service and switch `DOWMIND_VECTOR_STORE` to `pgvector` in the compose file.

---

## Quick setup for AI agents (copy-paste this to your AI agent)

There are two setup paths. Pick the one that matches your situation:

- **Option A — Full setup from scratch:** you don't have dow-mind installed yet (clone, build, choose embedder, install the MCP server in your tools). Use this the first time.
- **Option B — Onboard a new project:** the dow-mind MCP server is already installed and running in your tools, and you just want to wire a *new* project into RAG memory (create its collection, add AGENTS.md/CLAUDE.md, install the skill). Use this for every project after the first.

Copy the matching prompt below and paste it into Claude Code, Cline, Cursor, OpenCode, Codex, Factory Droid, Roo, Zed, Windsurf, or Continue.

### Option A — Full setup from scratch

```
I want to set up dow-mind, a per-project RAG memory MCP server.

Read the setup guide at https://raw.githubusercontent.com/RivaldiMurpia/dow-mind/main/README.md and the skill at https://raw.githubusercontent.com/RivaldiMurpia/dow-mind/main/skill/dow-mind.md

Then follow these steps:
1. Clone the repo to my machine: git clone https://github.com/RivaldiMurpia/dow-mind.git
2. Build the MCP server: cd dow-mind/mcp-server && go build ./cmd/mcp-server
3. Ask me which embedding option I prefer:
   - **Voyage AI** (recommended): no local setup needed, ask for my Voyage API key (free at voyageai.com, 200M free tokens with voyage-4).
   - **Self-hosted TEI**: start Qdrant + TEI with docker compose. Ask for Qdrant URL if I already have one running.
4. Install the MCP server in every coding tool I use. Use the correct config format per tool (the README has all formats from official docs).
5. For the project I'm currently working in, create or merge AGENTS.md and CLAUDE.md with RAG memory instructions. Use project ID derived from the folder name. Call mind_create_project to create the collection.
6. Do not replace existing AGENTS.md or CLAUDE.md. Append the RAG section with a --- separator.
```

### Option B — Onboard a new project (dow-mind already installed)

Use this when the dow-mind MCP server is already installed and configured in your coding tool. It skips clone/build/backend and only wires the current project into RAG memory.

```
I already have the dow-mind MCP server installed and running. I want to onboard THIS project into RAG memory.

Read the skill at https://raw.githubusercontent.com/RivaldiMurpia/dow-mind/main/skill/dow-mind.md for the AGENTS.md/CLAUDE.md templates.

Then follow these steps:
1. Confirm the dow-mind MCP tools are available (e.g. mind_create_project, mind_retrieve_context). If they are NOT available, stop and tell me to run the full setup (Option A) first.
2. Derive a PROJECT_ID from this project's folder name (or ask me).
3. Call mind_create_project with that PROJECT_ID to create its collection (safe to call if it already exists).
4. Create or merge AGENTS.md and CLAUDE.md in this project root using the skill's templates, with PROJECT_ID filled in. Do NOT replace existing files — append the RAG section below existing content, separated by a --- divider.
5. Install the dow-mind skill for this project. Prefer a project-local skill directory used by my tool (e.g. .agents/skills/dow-mind/, .factory/skills/dow-mind/, or the tool's skill folder) — do not assume ~/.factory. Ask me if you are unsure which directory my tool uses.
6. Call mind_index_project on this project directory to seed the collection with the current files.
```

---

## What agents should do

1. Install the Factory Droid skill.
2. Build the Go MCP server.
3. Start the RAG backend (if local mode).
4. Install the MCP server in every coding tool the user uses.
5. Generate `AGENTS.md` and `CLAUDE.md` in the target project so all agents automatically use the RAG memory.

---

## 1. Install the Factory Droid skill

Copy the skill file into the Droid skill directory:

```bash
mkdir -p ~/.factory/skills/dow-mind
cp ~/Project/dow-mind/skill/dow-mind.md ~/.factory/skills/dow-mind/skill.md
```

If the skill must be reloaded, restart the Droid session or refresh skills.

---

## 2. Build the MCP server

```bash
cd ~/Project/dow-mind/mcp-server
go build ./cmd/mcp-server
```

This produces the binary `~/Project/dow-mind/mcp-server/mcp-server`.

---

## 3. Start the RAG backend (local mode)

If the user does not already have a Qdrant + TEI backend, start the local one:

```bash
cd ~/Project/dow-mind/mcp-server
docker compose up -d qdrant tei-embedding
```

Verify:

```bash
curl -f http://localhost:6333/healthz
curl -f http://localhost:8081/health
```

If using an existing backend, skip this step and set the correct URLs in the tool config below.

---

## 4. Install the MCP server in every coding tool

Each tool has a different config format and file location. Use the correct one per tool.

### Quick reference table

| Tool | Format | Key | Config file |
| --- | --- | --- | --- |
| Claude Code | JSON | `mcpServers` | `~/.claude.json` or `.mcp.json` in project root |
| Claude Desktop | JSON | `mcpServers` | `~/Library/Application Support/Claude/claude_desktop_config.json` |
| Cline | JSON | `mcpServers` | `~/.cline/mcp.json` or IDE MCP settings |
| Cursor | JSON | `mcpServers` | `~/.cursor/mcp.json` or `.cursor/mcp.json` in project root |
| OpenCode | JSON | `mcp` | `~/.opencode/settings.json` or `opencode.json` in project root |
| Codex (OpenAI) | TOML | `[mcp_servers.*]` | `~/.codex/config.toml` |
| Factory Droid | CLI | — | `droid mcp add dow-mind /path/to/mcp-server` |
| Roo Code | JSON | `mcpServers` | global `mcp_settings.json` or `.roo/mcp.json` in project root |
| Zed | JSON | `context_servers` | `~/.config/zed/settings.json` |
| Windsurf | JSON | `mcpServers` | `~/.codeium/windsurf/mcp_config.json` |
| Continue | YAML | `mcpServers` (list) | `~/.continue/config.yaml` |

### Claude Code

**CLI (Voyage AI):**
```bash
claude mcp add --transport stdio dow-mind \
  --env DOWMIND_VECTOR_STORE=qdrant \
  --env DOWMIND_QDRANT_URL=http://localhost:6333 \
  --env DOWMIND_VOYAGE_API_KEY=your-voyage-api-key \
  -- /path/to/dow-mind/mcp-server/mcp-server
```

**CLI (self-hosted TEI):**
```bash
claude mcp add --transport stdio dow-mind \
  --env DOWMIND_VECTOR_STORE=qdrant \
  --env DOWMIND_QDRANT_URL=http://localhost:6333 \
  --env DOWMIND_TEI_URL=http://localhost:8081 \
  -- /path/to/dow-mind/mcp-server/mcp-server
```

**JSON (`~/.claude.json` or `.mcp.json`) — Voyage AI:**
```json
{
  "mcpServers": {
    "dow-mind": {
      "command": "/path/to/dow-mind/mcp-server/mcp-server",
      "env": {
        "DOWMIND_VECTOR_STORE": "qdrant",
        "DOWMIND_QDRANT_URL": "http://localhost:6333",
        "DOWMIND_VOYAGE_API_KEY": "your-voyage-api-key",
        "DOWMIND_VOYAGE_MODEL": "voyage-4"
      }
    }
  }
}
```

**JSON — self-hosted TEI:**
```json
{
  "mcpServers": {
    "dow-mind": {
      "command": "/path/to/dow-mind/mcp-server/mcp-server",
      "env": {
        "DOWMIND_VECTOR_STORE": "qdrant",
        "DOWMIND_QDRANT_URL": "http://localhost:6333",
        "DOWMIND_TEI_URL": "http://localhost:8081"
      }
    }
  }
}
```

All examples below use Voyage AI (recommended). Replace `DOWMIND_VOYAGE_API_KEY` with `DOWMIND_EMBEDDER=tei` + `DOWMIND_TEI_URL` if using self-hosted TEI.

### Claude Desktop

**File:** `~/Library/Application Support/Claude/claude_desktop_config.json`

```json
{
  "mcpServers": {
    "dow-mind": {
      "command": "/path/to/dow-mind/mcp-server/mcp-server",
      "env": {
        "DOWMIND_VECTOR_STORE": "qdrant",
        "DOWMIND_QDRANT_URL": "http://localhost:6333",
        "DOWMIND_VOYAGE_API_KEY": "your-voyage-api-key",
        "DOWMIND_VOYAGE_MODEL": "voyage-4"
      }
    }
  }
}
```

### Cline

**File:** `~/.cline/mcp.json` (CLI) or open Cline panel > MCP Servers > Configure (IDE)

```json
{
  "mcpServers": {
    "dow-mind": {
      "command": "/path/to/dow-mind/mcp-server/mcp-server",
      "env": {
        "DOWMIND_VECTOR_STORE": "qdrant",
        "DOWMIND_QDRANT_URL": "http://localhost:6333",
        "DOWMIND_VOYAGE_API_KEY": "your-voyage-api-key",
        "DOWMIND_VOYAGE_MODEL": "voyage-4"
      },
      "disabled": false,
      "autoApprove": []
    }
  }
}
```

### Cursor

**File:** `~/.cursor/mcp.json` (global) or `.cursor/mcp.json` (project)

```json
{
  "mcpServers": {
    "dow-mind": {
      "type": "stdio",
      "command": "/path/to/dow-mind/mcp-server/mcp-server",
      "env": {
        "DOWMIND_VECTOR_STORE": "qdrant",
        "DOWMIND_QDRANT_URL": "http://localhost:6333",
        "DOWMIND_VOYAGE_API_KEY": "your-voyage-api-key",
        "DOWMIND_VOYAGE_MODEL": "voyage-4"
      }
    }
  }
}
```

### OpenCode

**File:** `~/.opencode/settings.json` or `opencode.json` in project root

OpenCode uses `mcp` key (not `mcpServers`), `command` as array, and `environment` (not `env`).

```json
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "dow-mind": {
      "type": "local",
      "command": ["/path/to/dow-mind/mcp-server/mcp-server"],
      "enabled": true,
      "environment": {
        "DOWMIND_VECTOR_STORE": "qdrant",
        "DOWMIND_QDRANT_URL": "http://localhost:6333",
        "DOWMIND_VOYAGE_API_KEY": "your-voyage-api-key",
        "DOWMIND_VOYAGE_MODEL": "voyage-4"
      }
    }
  }
}
```

### Codex (OpenAI)

**File:** `~/.codex/config.toml` (TOML, not JSON)

**CLI:**
```bash
codex mcp add dow-mind \
  --env DOWMIND_VECTOR_STORE=qdrant \
  --env DOWMIND_QDRANT_URL=http://localhost:6333 \
  --env DOWMIND_VOYAGE_API_KEY=your-voyage-api-key \
  -- /path/to/dow-mind/mcp-server/mcp-server
```

**TOML:**
```toml
[mcp_servers.dow-mind]
command = "/path/to/dow-mind/mcp-server/mcp-server"

[mcp_servers.dow-mind.env]
DOWMIND_VECTOR_STORE = "qdrant"
DOWMIND_QDRANT_URL = "http://localhost:6333"
DOWMIND_VOYAGE_API_KEY = "your-voyage-api-key"
DOWMIND_VOYAGE_MODEL = "voyage-4"
```

### Factory Droid

```bash
droid mcp add dow-mind /path/to/dow-mind/mcp-server/mcp-server
```

### Roo Code

**File:** global `mcp_settings.json` (via Roo Code > MCP > Edit Global MCP) or `.roo/mcp.json` (project)

```json
{
  "mcpServers": {
    "dow-mind": {
      "command": "/path/to/dow-mind/mcp-server/mcp-server",
      "env": {
        "DOWMIND_VECTOR_STORE": "qdrant",
        "DOWMIND_QDRANT_URL": "http://localhost:6333",
        "DOWMIND_VOYAGE_API_KEY": "your-voyage-api-key",
        "DOWMIND_VOYAGE_MODEL": "voyage-4"
      },
      "alwaysAllow": [],
      "disabled": false
    }
  }
}
```

### Zed

**File:** `~/.config/zed/settings.json`

Zed uses `context_servers` key (not `mcpServers`). Can also be added via UI: Command Palette > `agent: add context server`.

```json
{
  "context_servers": {
    "dow-mind": {
      "command": "/path/to/dow-mind/mcp-server/mcp-server",
      "args": [],
      "env": {
        "DOWMIND_VECTOR_STORE": "qdrant",
        "DOWMIND_QDRANT_URL": "http://localhost:6333",
        "DOWMIND_VOYAGE_API_KEY": "your-voyage-api-key",
        "DOWMIND_VOYAGE_MODEL": "voyage-4"
      }
    }
  }
}
```

### Windsurf

**File:** `~/.codeium/windsurf/mcp_config.json`

Can also be added via MCP Marketplace UI in Windsurf.

```json
{
  "mcpServers": {
    "dow-mind": {
      "command": "/path/to/dow-mind/mcp-server/mcp-server",
      "env": {
        "DOWMIND_VECTOR_STORE": "qdrant",
        "DOWMIND_QDRANT_URL": "http://localhost:6333",
        "DOWMIND_VOYAGE_API_KEY": "your-voyage-api-key",
        "DOWMIND_VOYAGE_MODEL": "voyage-4"
      }
    }
  }
}
```

### Continue

**File:** `~/.continue/config.yaml` (YAML, not JSON)

Continue uses `mcpServers` as a list of objects with `name`, `command`, `env`.

```yaml
mcpServers:
  - name: dow-mind
    command: /path/to/dow-mind/mcp-server/mcp-server
    env:
      DOWMIND_VECTOR_STORE: qdrant
      DOWMIND_QDRANT_URL: http://localhost:6333
      DOWMIND_VOYAGE_API_KEY: your-voyage-api-key
      DOWMIND_VOYAGE_MODEL: voyage-4
```

Replace `/path/to/dow-mind/mcp-server/mcp-server` with the actual absolute path to the built binary.

---

## 5. Enable RAG memory for the target project

### Per-project collection

Each project gets its own isolated collection in the vector store: `project_<PROJECT_ID>`. Call `mind_create_project` with the project ID to create it. Multiple projects share the same MCP server and backend, but each has isolated memory.

### AGENTS.md and CLAUDE.md

Create or update these two files in the root of the target project. Replace `PROJECT_ID` with the actual project name or slug.

**Important: merge, do not replace.** If the file already exists, append the RAG section below existing content separated by `---`. Use the templates in `skill/templates/AGENTS.md` and `skill/templates/CLAUDE.md`.

### `AGENTS.md`

```markdown
# Project agent instructions

This project uses the `dow-mind` MCP server for per-project memory.

## Before coding

1. Call `mind_retrieve_context` with the project ID `PROJECT_ID` and the user's query.
2. Read the returned context. If relevant, use it to shape your answer or plan.

## After coding

1. Summarize what you changed.
2. Call `mind_index` with useful new facts, design decisions, gotchas, or patterns under project ID `PROJECT_ID`.
3. Call `mind_index_project` with the project directory to sync all file changes into RAG. Always do this — it handles new files, edits, and deletions automatically.

Keep chunks concise (one idea per chunk). Use metadata tags like `type:architecture`, `type:decision`, `type:api`, `type:bugfix`, `type:howto`, or `type:snippet`.
```

### `CLAUDE.md`

```markdown
# Claude instructions for this project

## RAG memory workflow

### Before making significant changes

1. Call `mind_retrieve_context` with the project ID `PROJECT_ID` and the user's query.
2. Read the returned context. If it is empty or irrelevant, continue as normal.

### After completing work

1. Summarize what you changed and why.
2. Call `mind_index` with useful new facts, design decisions, gotchas, or patterns under project ID `PROJECT_ID`.

Use project ID: `PROJECT_ID`

Each project has its own collection: `project_PROJECT_ID`. Do not mix project memories.
```

---

## Layout

```
dow-mind/
├── AGENTS.md                          # Universal agent install guide (this repo)
├── CLAUDE.md                          # Claude-family quick reference (this repo)
├── README.md                          # This file
├── Makefile                           # Build pipeline: make web, make build, make dev-*
├── docker-compose.all-in-one.yml      # All-in-one: dow-mind + Qdrant (optional PG, TEI)
├── mcp-server/                        # Go server (MCP stdio + HTTP serve modes)
│   ├── cmd/mcp-server/main.go         # Entry point: --serve / --addr flags
│   ├── pkg/rag                        # Provider interface + Qdrant, Chroma, pgvector, Voyage, TEI
│   ├── pkg/core                       # Service layer (shared by MCP + HTTP)
│   ├── pkg/config                     # Config file (~/.dow-mind/config.yaml)
│   ├── pkg/httpapi                    # Chi router, REST handlers, SSE, admin auth
│   ├── pkg/indexer                    # File indexer with content hashing
│   ├── web/                           # React SPA (Vite + React + TypeScript + Tailwind)
│   │   ├── dist/                      # Build output (embedded via go:embed)
│   │   └── embed.go                   # //go:embed all:dist
│   ├── Dockerfile                     # Multi-stage: builds SPA + Go binary
│   └── docker-compose.yml             # Backend-only compose (Qdrant + TEI)
└── skill/                             # Factory Droid skill
    ├── dow-mind.md
    └── templates/
        └── AGENTS.md
```

## Providers

| Vector store | Embedder | Status |
| --- | --- | --- |
| Qdrant | TEI (self-hosted) | Supported |
| Qdrant | Voyage AI | Supported |
| pgvector | TEI (self-hosted) | Supported |
| pgvector | Voyage AI | Supported — recommended (hybrid search + retrieval breakdown) |
| Chroma | TEI / Voyage AI | Experimental — see note below |

**Feature support by backend:**

| Feature | Qdrant | pgvector | Chroma |
| --- | :---: | :---: | :---: |
| Index / semantic search / delete | ✅ | ✅ | ⚠️ |
| Project list + stats (dashboard) | ✅ | ✅ | ⚠️ |
| Hybrid search (dense + lexical RRF) | — | ✅ | — |
| Retrieval breakdown (dense/lexical) | — | ✅ | — |
| Token metrics (Voyage) | ✅ | ✅ | ✅ |

> **Chroma is experimental.** The provider targets Chroma's legacy `/api/v1`
> REST API and has only been tested against mocks, not a live server. Chroma
> ≥ 0.6 moved to `/api/v2` (tenant/database paths, UUID-addressed collections),
> so the current provider may not work against modern Chroma. Use **Qdrant** or
> **pgvector** for a supported setup. Contributions to port Chroma to `/api/v2`
> are welcome.

## Migration

The dashboard's **Migration** page (and `POST /api/migrate`) re-embeds a
project's stored text into a new destination. Because embedding vectors are
model-specific and not portable, migration re-embeds from text (which dow-mind
stores alongside every chunk) rather than copying raw vectors. Use it to:

- **Change embedding model or dimension** — pick a new embedder/model/dim; the
  destination collection is re-embedded from the source text.
  (For pgvector, a new dimension requires a **new table name** — all projects in
  one pgvector table share a fixed vector dimension.)
- **Move between vector stores** — e.g. Qdrant → pgvector.
- **Import from an external cloud vector DB** — **Qdrant Cloud** is verified;
  **Pinecone**, **Weaviate**, and **Chroma Cloud** connectors are *experimental*
  (built from vendor docs, mock-tested only — not verified against a live
  account) and are labelled as such in the UI.

Migrations run asynchronously with live progress over SSE. The source project is
never removed automatically; after a successful migration the UI offers an
explicit "Delete source" action.

## Embedding options

### Option A: Voyage AI (recommended, hosted)

No GPU required. Get a free API key at [voyageai.com](https://voyageai.com). `voyage-4` has 200M free tokens.

Set `DOWMIND_EMBEDDER=voyage` (or just set `DOWMIND_VOYAGE_API_KEY` — it auto-detects):

```json
"env": {
  "DOWMIND_VECTOR_STORE": "qdrant",
  "DOWMIND_QDRANT_URL": "http://localhost:6333",
  "DOWMIND_EMBEDDER": "voyage",
  "DOWMIND_VOYAGE_API_KEY": "your-voyage-api-key",
  "DOWMIND_VOYAGE_MODEL": "voyage-4"
}
```

### Option B: OpenAI-compatible (any `/v1/embeddings` API)

Works with OpenAI, Together, Jina, Mistral, DeepInfra, LiteLLM, a local Ollama, and anything else that speaks the OpenAI embeddings protocol. Point `DOWMIND_OPENAI_BASE_URL` at the provider and set the model. Leave the API key empty for a local/no-auth endpoint. `DOWMIND_OPENAI_DIM` is optional (`0` = auto-detect; models like `text-embedding-3-*` honor it).

```json
"env": {
  "DOWMIND_VECTOR_STORE": "qdrant",
  "DOWMIND_QDRANT_URL": "http://localhost:6333",
  "DOWMIND_EMBEDDER": "openai",
  "DOWMIND_OPENAI_BASE_URL": "https://api.openai.com/v1",
  "DOWMIND_OPENAI_API_KEY": "sk-...",
  "DOWMIND_OPENAI_MODEL": "text-embedding-3-small"
}
```

Local Ollama example (no API key needed):

```json
"env": {
  "DOWMIND_EMBEDDER": "openai",
  "DOWMIND_OPENAI_BASE_URL": "http://localhost:11434/v1",
  "DOWMIND_OPENAI_MODEL": "nomic-embed-text"
}
```

### Option C: TEI (self-hosted)

Runs a local Text Embeddings Inference container — serve **any** local embedding model (BGE, GTE, E5, nomic-embed, …); dow-mind only needs its URL. Requires Docker and ~1 GB of RAM.

```bash
cd mcp-server && docker compose up -d qdrant tei-embedding
```

```json
"env": {
  "DOWMIND_VECTOR_STORE": "qdrant",
  "DOWMIND_QDRANT_URL": "http://localhost:6333",
  "DOWMIND_EMBEDDER": "tei",
  "DOWMIND_TEI_URL": "http://localhost:8081"
}
```

## Environment variables

All configuration is via environment variables (or config file at `~/.dow-mind/config.yaml`). Priority: **env var > config file > default**.

| Variable | Default | Description |
| --- | --- | --- |
| `DOWMIND_VECTOR_STORE` | `qdrant` | Vector store: `qdrant`, `chroma`, or `pgvector` |
| `DOWMIND_EMBEDDER` | `voyage` | Embedding provider: `voyage`, `openai` (any OpenAI-compatible API), or `tei` (auto-detects: falls back to `tei` if `DOWMIND_VOYAGE_API_KEY` is not set) |
| `DOWMIND_QDRANT_URL` | `http://localhost:6333` | Qdrant REST URL |
| `DOWMIND_QDRANT_API_KEY` | *(empty)* | Optional Qdrant API key (for Qdrant Cloud) |
| `DOWMIND_CHROMA_URL` | `http://localhost:8000` | Chroma REST URL |
| `DOWMIND_PGVECTOR_DSN` | *(empty)* | PostgreSQL connection string (e.g., `postgresql://user@localhost:5432/dbname`). Required when `DOWMIND_VECTOR_STORE=pgvector` |
| `DOWMIND_TEI_URL` | `http://localhost:8081` | Text Embeddings Inference URL. Used when `DOWMIND_EMBEDDER=tei` |
| `DOWMIND_VOYAGE_API_KEY` | *(empty)* | Voyage AI API key (required when `DOWMIND_EMBEDDER=voyage`). Get a free key at [voyageai.com](https://voyageai.com) (200M free tokens with voyage-4) |
| `DOWMIND_VOYAGE_MODEL` | `voyage-4` | Voyage AI embedding model name |
| `DOWMIND_OPENAI_BASE_URL` | `https://api.openai.com/v1` | OpenAI-compatible embeddings base URL (used when `DOWMIND_EMBEDDER=openai`). Point at OpenAI, Together, Jina, a local Ollama (`http://localhost:11434/v1`), etc. |
| `DOWMIND_OPENAI_API_KEY` | *(empty)* | API key for the OpenAI-compatible endpoint. Leave empty for local/no-auth endpoints |
| `DOWMIND_OPENAI_MODEL` | *(empty)* | Embedding model name (required when `DOWMIND_EMBEDDER=openai`), e.g. `text-embedding-3-small`, `nomic-embed-text` |
| `DOWMIND_OPENAI_DIM` | `0` | Output dimension for the OpenAI embedder. `0` = auto-detect; models like `text-embedding-3-*` honor an explicit value |
| `DOWMIND_VECTOR_DIM` | `1024` | Embedding vector dimension (matches voyage-4 default). Override only if using a different model with a different dimension |
| `DOWMIND_RERANKER_MODEL` | *(empty)* | Reranker model name (e.g., `rerank-2.5`). When set and `DOWMIND_VOYAGE_API_KEY` is available, reranking is enabled for search |
| `DOWMIND_ADMIN_TOKEN` | *(empty)* | Optional admin token. When set, all `/api/*` endpoints require `Authorization: Bearer <token>` header. When unset, no auth is required |
| `DOWMIND_CORS_ORIGIN` | *(empty)* | Optional CORS origin for the SSE event stream (`/api/events`). When set (e.g. `*` or `https://app.example.com`), it becomes the `Access-Control-Allow-Origin` header. When unset, no CORS header is sent and the stream stays same-origin only |

## Tools

- `mind_create_project` — create a project collection
- `mind_delete_project` — delete a project collection
- `mind_index` — index individual documents into a project
- `mind_index_project` — scan a directory and auto-index all code/text files. Handles insertions and deletions (removed files are purged from RAG). Skips node_modules, .git, vendor, dist, build.
- `mind_semantic_search` — semantic search across project memory
- `mind_retrieve_context` — compact context string for LLM consumption

## Notes

- The server has two modes: **MCP stdio** (default, no flags) and **HTTP serve** (`--serve`). Both use the same core service layer.
- In stdio mode, logs go to stderr (stdout is the MCP protocol stream).
- In serve mode, the HTTP server provides a REST API, SSE event stream, and an embedded React dashboard UI.
- `make build` produces a single self-contained binary with the SPA embedded via `embed.FS`.
- Each project gets its own collection/index: `project_<project_id>`.
- Config priority: environment variable > config file (`~/.dow-mind/config.yaml`) > built-in defaults.
- Optional admin token auth (`DOWMIND_ADMIN_TOKEN`) protects `/api/*` endpoints when set. No auth when unset.

