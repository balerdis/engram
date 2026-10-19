---
name: engram-architecture-guardrails
description: >
  Architecture guardrails for Engram across local store, server, sync,
  and plugins. Trigger: Any change that affects system boundaries, ownership,
  state flow, or cross-package responsibilities.
license: Apache-2.0
metadata:
  author: gentleman-programming
  version: "1.0"
---

## When to Use

Use this skill when:
- Adding a new subsystem or major package
- Moving responsibilities between local store, server, sync, or plugins
- Changing sync flow, source-of-truth rules, or persistence boundaries

---

## Core Guardrails

1. Local SQLite is the source of truth.
2. Keep plugin/adaptor layers thin; real behavior belongs in Go packages.
3. Prefer explicit boundaries: store, server, sync.
4. New features must fit the existing local-first mental model before they fit the UI.
5. Do not hide cross-system coupling inside helpers.

---

## Decision Rules

- Local-only concern -> `internal/store`
- HTTP contract or enforcement -> `internal/server`
- Chunk export/import -> `internal/sync`
---

## Validation

- Add regression tests for every boundary change.
- Verify local behavior still tells the same product story.
- If the change touches sync, test both push and pull paths.
