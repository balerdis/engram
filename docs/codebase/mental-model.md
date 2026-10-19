[← Codebase Guide](../CODEBASE-GUIDE.md) | [Next: Repository Map →](repository-map.md)

# Mental Model

**Engram gives coding agents curated, searchable, portable memory after a session ends or a conversation is compacted.** It is local-first: SQLite is authoritative.

## What Engram is

| It is | What that means in the code |
|---|---|
| **Persistent memory for agents** | Agents save structured observations with `mem_save`, `mem_session_summary`, `mem_save_prompt`, and related tools in `internal/mcp`. |
| **Local-first** | `internal/store` persists to SQLite; interfaces read/write there first. |
| **A Go binary** | `cmd/engram` composes store, server, MCP, TUI, setup, and sync. |
| **SQLite + FTS5** | `internal/store/store.go` defines sessions, observations, prompts, FTS, dedupe, topic upserts, and soft deletes. |
| **Agent-agnostic** | Integrations go through MCP, manual MCP configuration, or thin plugins in `plugin/`; `internal/setup` covers only the automated flows that are implemented. |
| **Documented by current behavior** | `DOCS.md`, `docs/ARCHITECTURE.md`, `docs/AGENT-SETUP.md`, and `docs/PLUGINS.md` are living references. |

## What Engram is not

| It is not | Why that matters |
|---|---|
| **It is not a raw tool-call recorder** | The agent decides what is worth saving; Engram does not chase an indiscriminate firehose. |
| **It is not a boundaryless monolith** | Store, server, sync, plugins, and setup have explicit ownership. |
| **It is not a duplicated API reference here** | For complete endpoints, schemas, and parameters, use [DOCS.md](../../DOCS.md). This guide explains where each thing fits. |

## Architecture in 90 seconds

```text
Coding agent
  Claude Code / OpenCode / Gemini CLI / Codex / VS Code / Antigravity / Cursor / Windsurf
        │
        │ MCP stdio, plugin hooks, or local API
        ▼
cmd/engram
  CLI + local runtime
        │
        ├── internal/mcp        mem_* tools for agents
        ├── internal/server     local JSON API: engram serve
        ├── internal/tui        Bubbletea terminal UI
        ├── internal/setup      automated integration installation
        │
        ▼
internal/store
  SQLite + FTS5 + sessions + observations + prompts + relations
        │
        └── internal/sync                   git-friendly chunks / transport
```

## Runtimes

```text
Local runtime: engram serve
  Listens for the local JSON API on 127.0.0.1:7437 by default
  Uses internal/server
  Reads/writes internal/store SQLite
```

| Runtime | Command | Main packages | Data type |
|---|---|---|---|
| Local API | `engram serve` | `cmd/engram`, `internal/server`, `internal/store` | Local JSON, SQLite |
| MCP stdio | `engram mcp` | `cmd/engram`, `internal/mcp`, `internal/store` | MCP tools, SQLite |
| Direct CLI | `engram search`, `engram save`, `engram sync`, etc. | `cmd/engram`, `internal/store`, `internal/sync` | stdout/stderr + SQLite/chunks |
| TUI | `engram tui` | `internal/tui`, `internal/store` | Bubbletea terminal |

For the exact and current endpoint list, use [DOCS.md — HTTP API Endpoints](../../DOCS.md#http-api-endpoints).

## Codebase compass

```text
Local-first.
Real behavior before pretty UI.
Explicit owner before convenient helper.
Thin adapter before smart plugin.
Current docs before promises.
Boundary tests before verbal confidence.
```

Engram stays healthy when each package does one clear thing and every surface tells the same story: **an agent saves curated memories and local SQLite preserves them**.

---

[← Codebase Guide](../CODEBASE-GUIDE.md) | [Next: Repository Map →](repository-map.md)
