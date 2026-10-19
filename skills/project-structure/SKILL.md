---
name: engram-project-structure
description: >
  Repository structure and placement rules for Engram. Trigger: Creating files,
  packages, handlers, templates, styles, or tests in this repo.
license: Apache-2.0
metadata:
  author: gentleman-programming
  version: "1.0"
---

## When to Use

Use this skill when:
- Creating a new package, file, or directory
- Deciding where code belongs
- Adding tests, templates, assets, or docs

---

## Placement Rules

1. Put behavior near its domain, not near the caller that happens to use it.
2. Put HTTP handlers in server packages, not in store packages.
3. Put persistence queries in the store, not in handlers.

---

## File Creation Rules

- New route -> handler + tests in the server package
- New DB behavior -> store code + focused tests
- New contributor guidance -> update docs/catalog in same change

---

## Anti-Patterns

- Do not mix SQL, HTML, and transport logic in one file.
- Do not create utility packages for one-off helpers.
- Do not add package layers unless they remove real coupling.
