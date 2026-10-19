[← Codebase Guide](../CODEBASE-GUIDE.md) | [← Previous: Maintainer Playbook](maintainer-playbook.md)

# Reference Map

**Use this appendix when you need to trace documentation and source files to their purpose.** It is a map, not a substitute for source code or [DOCS.md](../../DOCS.md).

## Main documents

| Document | Use |
|---|---|
| `README.md` | Landing, quickstart, product model, main links. |
| `DOCS.md` | Complete technical reference: schema, endpoints, MCP tools, CLI. |
| `CONTRIBUTING.md` | Contribution flow and general standards. |
| `SECURITY.md` | Security reporting. |
| `CHANGELOG.md` | Change history. |
| `docs/ARCHITECTURE.md` | Existing architecture, lifecycle, CLI reference. |
| `docs/AGENT-SETUP.md` | Per-agent setup, project detection, compaction survival. |
| `docs/PLUGINS.md` | OpenCode/Claude plugin details and current limits. |
| `docs/INSTALLATION.md` | Platform installation. |
| `docs/DOCTOR.md` | Operational diagnosis and repair. |
| `docs/COMPARISON.md` | Comparison with alternatives. |
| `docs/intended-usage.md` | Expected usage/product framing. |
| `docs/CODEBASE-GUIDE.md` | Codebase guide landing page and reading path. |
| `docs/codebase/*.md` | Split codebase guide pages by topic. |

## Main source files

| File/directory | Why it matters |
|---|---|
| `cmd/engram/main.go` | Main binary wiring. |
| `cmd/engram/doctor.go` | CLI doctor. |
| `cmd/engram/conflicts.go` | Conflicts CLI. |
| `internal/store/store.go` | Local persistence core. |
| `internal/store/relations.go` | Memory relationships/judgments. |
| `internal/mcp/mcp.go` | MCP tools and profiles. |
| `internal/mcp/activity.go` | MCP activity/session tracking. |
| `internal/server/server.go` | Local JSON API. |
| `internal/sync/sync.go` | Chunks, manifest, import/export, bootstrap. |
| `internal/sync/transport.go` | Sync transport abstraction. |
| `internal/project/detect.go` | Project detection. |
| `internal/project/similar.go` | Name similarity/consolidation. |
| `internal/setup/setup.go` | Integration installation. |
| `internal/tui/` | Bubbletea TUI. |
| `internal/diagnostic/` | Operational checks/repair. |
| `internal/llm/` | Runners for semantic scanning with agent CLIs. |
| `internal/obsidian/` | Obsidian beta export/watch/hub. |
| `plugin/opencode/engram.ts` | OpenCode adapter. |
| `plugin/claude-code/` | Claude Code plugin, hooks, and skill. |
| `plugin/obsidian/` | Experimental Obsidian plugin. |
| `skills/` | Agent contribution rules. |
| `openspec/` | Per-change specs/designs/tasks. |

## Related codebase guide pages

| Page | Purpose |
|---|---|
| [Mental Model](mental-model.md) | What Engram is/is not and the 90-second architecture model. |
| [Repository Map](repository-map.md) | Package ownership and placement rules. |
| [Memory Core](memory-core.md) | Store entities, save/retrieve flow, and invariants. |
| [Interfaces](interfaces.md) | CLI, MCP, local API, and TUI responsibilities. |
| [Sync](sync.md) | Chunk sync: export/import, manifest, transport, chunk IDs. |
| [Integrations](integrations.md) | Agent integrations, thin plugin principle, and setup boundaries. |
| [Maintainer Playbook](maintainer-playbook.md) | Capabilities, navigation, guardrails, checklists, and review playbook. |

---

[← Previous: Maintainer Playbook](maintainer-playbook.md) | [← Codebase Guide](../CODEBASE-GUIDE.md)
