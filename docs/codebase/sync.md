[← Codebase Guide](../CODEBASE-GUIDE.md) | [← Previous: Interfaces](interfaces.md) | [Next: Integrations →](integrations.md)

# Sync

**Sync moves local-first memory without changing ownership: local SQLite stays authoritative.** Engram has git-friendly chunk sync: `engram sync` exports to `.engram/` and imports from it.

```text
Local SQLite
   │
   └── engram sync
         └── .engram/manifest.json + gzip JSONL chunks
```

## Git-friendly chunks: `internal/sync`

`internal/sync/sync.go` avoids one large shared JSON file. Each sync creates new chunks and a small manifest. That reduces merge conflicts and lets multiple machines generate memory in parallel.

Project-scoped chunks carry sessions, observations, prompts, and the non-orphaned `memory_relations` graph for observations in that project. Relation rows travel as `relation` mutations inside the chunk so imports reuse the same idempotent relation apply path.

Import goes through `Store.ApplyPulledChunk`, which applies the chunk and records it in `sync_chunks` in one transaction. Export reads the store (`Export`, `ExportProject`, `ExportRelationMutations`); nothing is read from or written to `sync_mutations`.

The chunk ID is the first 8 hex characters of the SHA-256 of the encoded chunk (`internal/sync/chunkcodec`). It must not change: `internal/sync/chunk_pin_test.go` pins the encoded bytes and the ID for a fixed input.

Guardrails:

- Do not modify old chunks to “update” them.
- Do not assume every project is exported unless the command says so.
- Keep imported-chunk tracking to avoid duplicates.
- Do not change the chunk encoding or ID: repos that already exported chunks would re-export their history.

## Sync change checklist

- [ ] Local SQLite remains the source of truth.
- [ ] Export and import round-trip (`internal/sync/roundtrip_test.go`) still passes.
- [ ] The chunk pin test (`internal/sync/chunk_pin_test.go`) still passes without edits.
- [ ] `README.md#git-sync` and `DOCS.md` stay aligned.

---

[← Previous: Interfaces](interfaces.md) | [Next: Integrations →](integrations.md)
