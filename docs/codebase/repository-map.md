[← Codebase Guide](../CODEBASE-GUIDE.md) | [← Previous: Mental Model](mental-model.md) | [Next: Memory Core →](memory-core.md)

# Repository Map

**Use this page to place behavior in the right owner before writing code.** Most Engram mistakes start when SQL, HTTP, UI, sync, and plugin responsibilities drift into the same file.

## Quick map

| If you need to... | Open first | Then check |
|---|---|---|
| Understand the product in 5 minutes | `README.md` | `docs/ARCHITECTURE.md` |
| See the full technical reference | `DOCS.md` | This guide for ownership and guardrails |
| Change MCP tools | `internal/mcp/mcp.go` | `internal/mcp/*_test.go`, `docs/AGENT-SETUP.md` |
| Change local persistence | `internal/store/store.go` | `internal/store/*_test.go`, `DOCS.md#database-schema` |
| Change the local API | `internal/server/server.go` | `internal/server/*_test.go`, `DOCS.md#http-api-endpoints` |
| Change chunk sync | `internal/sync/sync.go` | `internal/sync/*_test.go`, `README.md#git-sync` |
| Change agent setup | `internal/setup/registry.go`, `internal/setup/agents.go`, `internal/setup/setup.go` | `plugin/`, `docs/AGENT-SETUP.md`, `docs/PLUGINS.md` |
| Change project detection | `internal/project/detect.go` | `internal/project/similar.go`, `docs/AGENT-SETUP.md` |
| Change the TUI | `internal/tui/model.go` | `internal/tui/update.go`, `internal/tui/view.go`, `internal/tui/styles.go` |
| Change Obsidian | `internal/obsidian/` | `plugin/obsidian/`, `docs/beta/obsidian-brain.md` |
| Prepare a large feature | `openspec/changes/*` | `openspec/specs/*`, `CONTRIBUTING.md` |

## Package ownership

| Area | Responsibility | Should not do |
|---|---|---|
| `cmd/engram` | CLI parsing, runtime composition, package wiring, flags, user commands. | Put SQL or deep business rules here. |
| `internal/store` | Local source of truth: SQLite, FTS5, sessions, observations, prompts, relations, dedupe, topic upserts, local diagnostics. | Render HTML, decide UX. |
| `internal/mcp` | Expose MCP tools, resolve project by cwd, profile tools (`agent`, `admin`, `all`), translate agent calls into store operations. | Persist outside the store or duplicate store logic. |
| `internal/server` | Local JSON API (`engram serve`) and its local endpoints. | Render a browser UI. |
| `internal/sync` | Chunk export/import, manifest, file transport, chunk IDs (`internal/sync/chunkcodec`). | Decide auth or render a UI. |
| `internal/project` | Project detection and normalization. | Access the store to correct data. |
| `internal/setup` | Registry-driven agent installation: declarative adapters in `agents.go`, generic registry/injection machinery in `registry.go`, and custom installers in `setup.go`. | Orchestrate automatic cloud enrollment/login if it is not implemented and documented. |
| `plugin/` | Thin adapters per agent/host. | Contain core behavior that should live in Go. |
| `skills/` | Guardrails for contributor agents. | Replace specs, tests, or code. |
| `docs/` | Usage, architecture, plugin, installation, and doctor guides. | Document unimplemented aspirations. |
| `openspec/` | Per-change proposals, specs, designs, and tasks. | Act as end-user documentation. |

## Golden placement rule

```text
Is it local persistence?         → internal/store
Is it a local HTTP contract?     → internal/server
Is it a tool for agents?         → internal/mcp
Is it chunk/export/import?       → internal/sync
Is it agent installation?        → internal/setup
```

If a change needs two areas, separate the contract and add tests that cross the boundary.

---

[← Previous: Mental Model](mental-model.md) | [Next: Memory Core →](memory-core.md)
