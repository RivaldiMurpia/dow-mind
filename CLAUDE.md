# Claude instructions for this project

This project is `dow-mind`, a per-project RAG memory MCP server.

## Setup command

If the user asks to install the skill or MCP server, run:

```bash
mkdir -p ~/.factory/skills/dow-mind
cp ~/Project/dow-mind/skill/dow-mind.md ~/.factory/skills/dow-mind/skill.md
cd ~/Project/dow-mind/mcp-server
go build ./cmd/mcp-server
```

For local backend (TEI fallback):

```bash
cd ~/Project/dow-mind/mcp-server
docker compose up -d qdrant tei-embedding
```

## MCP server config

**Voyage AI (recommended):**

```json
{
  "mcpServers": {
    "dow-mind": {
      "command": "~/Project/dow-mind/mcp-server/mcp-server",
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

**Self-hosted TEI:**

```json
{
  "mcpServers": {
    "dow-mind": {
      "command": "~/Project/dow-mind/mcp-server/mcp-server",
      "env": {
        "DOWMIND_VECTOR_STORE": "qdrant",
        "DOWMIND_EMBEDDER": "tei",
        "DOWMIND_QDRANT_URL": "http://localhost:6333",
        "DOWMIND_TEI_URL": "http://localhost:8081"
      }
    }
  }
}
```

## Always use RAG memory

- Before coding: call `mind_retrieve_context` with project ID `dow-mind` and the user's query.
- After coding: call `mind_index` to save new facts, design decisions, and patterns under project ID `dow-mind`.

Keep index chunks concise and tag metadata such as `type:architecture`, `type:decision`, `type:howto`, or `type:snippet`.

## Reference

- Setup guide: https://github.com/RivaldiMurpia/dow-mind/blob/main/README.md
- Skill guide: https://github.com/RivaldiMurpia/dow-mind/blob/main/skill/dow-mind.md
- Agent install guide: https://github.com/RivaldiMurpia/dow-mind/blob/main/AGENTS.md
