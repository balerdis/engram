package sync

import (
	"encoding/json"
	"testing"

	"github.com/Gentleman-Programming/engram/internal/store"
	"github.com/Gentleman-Programming/engram/internal/sync/chunkcodec"
)

// fixedChunkForPin is a fixed export input. Its encoded bytes and ChunkID are
// pinned so that moving or editing the chunk codec cannot silently change the
// IDs of chunks already exported to .engram/ directories (which would make
// every repo re-export its history).
func fixedChunkForPin() ChunkData {
	ended := "2026-04-10T12:30:00Z"
	summary := "resumen"
	tool := "bash"
	project := "proj-a"
	topic := "arch/pin"
	seen := "2026-04-10T11:30:00Z"
	return ChunkData{
		Sessions: []store.Session{{ID: "sess-1", Project: "proj-a", Directory: "/tmp/sess-1", StartedAt: "2026-04-10T12:00:00Z", EndedAt: &ended, Summary: &summary}},
		Observations: []store.Observation{{
			ID: 7, SyncID: "obs-1", SessionID: "sess-1", Type: "note", Title: "titulo", Content: "contenido",
			ToolName: &tool, Project: &project, Scope: "project", TopicKey: &topic, RevisionCount: 2, DuplicateCount: 1,
			LastSeenAt: &seen, CreatedAt: "2026-04-09T10:00:00Z", UpdatedAt: "2026-04-10T11:00:00Z",
		}},
		Prompts: []store.Prompt{{ID: 3, SyncID: "prompt-1", SessionID: "sess-1", Content: "prompt", Project: "proj-a", CreatedAt: "2026-04-08T09:00:00Z"}},
		Mutations: []store.SyncMutation{{
			Seq: 1, TargetKey: "local", Entity: "relation", EntityKey: "rel-1", Op: "upsert",
			Payload: `{"sync_id":"rel-1"}`, Source: "local", Project: "proj-a", OccurredAt: "2026-04-10T12:00:00Z",
		}},
	}
}

const (
	pinnedChunkJSON = `{"sessions":[{"id":"sess-1","project":"proj-a","directory":"/tmp/sess-1","started_at":"2026-04-10T12:00:00Z","ended_at":"2026-04-10T12:30:00Z","summary":"resumen"}],"observations":[{"id":7,"sync_id":"obs-1","session_id":"sess-1","type":"note","title":"titulo","content":"contenido","tool_name":"bash","project":"proj-a","scope":"project","topic_key":"arch/pin","revision_count":2,"duplicate_count":1,"last_seen_at":"2026-04-10T11:30:00Z","created_at":"2026-04-09T10:00:00Z","updated_at":"2026-04-10T11:00:00Z"}],"prompts":[{"id":3,"sync_id":"prompt-1","session_id":"sess-1","content":"prompt","project":"proj-a","created_at":"2026-04-08T09:00:00Z"}],"mutations":[{"seq":1,"target_key":"local","entity":"relation","entity_key":"rel-1","op":"upsert","payload":"{\"sync_id\":\"rel-1\"}","source":"local","project":"proj-a","occurred_at":"2026-04-10T12:00:00Z"}]}`
	pinnedChunkID   = "e94ef304"
)

func TestChunkEncodingAndIDArePinned(t *testing.T) {
	got, err := json.Marshal(fixedChunkForPin())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != pinnedChunkJSON {
		t.Fatalf("encoded chunk changed:\n got: %s\nwant: %s", got, pinnedChunkJSON)
	}
	if id := chunkcodec.ChunkID(got); id != pinnedChunkID {
		t.Fatalf("chunk id changed: got %s want %s", id, pinnedChunkID)
	}
}
