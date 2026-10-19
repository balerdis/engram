---
name: engram-business-rules
description: >
  Product and business-rule guardrails for Engram. Trigger: Any change that
  affects sync behavior, project controls, permissions, or memory semantics.
license: Apache-2.0
metadata:
  author: gentleman-programming
  version: "1.0"
---

## When to Use

Use this skill when:
- Changing sync, auth, or memory semantics
- Adjusting what data is exported or imported

---

## Product Rules

1. Local-first remains the default mental model.
2. UI controls must map to real business rules, never fake toggles.
3. Data visibility and sync behavior must be deterministic and testable.

---

## Sync Rules

- Local `engram sync` exports one project at a time unless `--all` is given.
- When something blocks sync, fail loudly rather than dropping data silently.
