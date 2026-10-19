package redact

import (
	"fmt"
	"strings"
	"testing"
)

// Benchmarks of the redactor. They are the performance budget of the package:
// run them with
//
//	go test ./internal/redact -run '^$' -bench . -benchmem
//
// and compare against the budget documented in docs/arquitectura-engram.md.
// Timing is deliberately NOT asserted in a unit test (it would be flaky under
// load). The corpora are generated, so they are deterministic.

// benchProse is ~50 KB of what observations usually hold: markdown prose,
// code, JSON and log lines, with a few credentials mixed in.
func benchProse(size int) string { return benchMix(size, true) }

// benchMix is benchProse; with dense=false only a handful of lines carry
// credentials (the usual observation: a decision or a bug fix with at most
// one pasted secret).
func benchMix(size int, dense bool) string {
	var b strings.Builder
	for i := 0; b.Len() < size; i++ {
		k := i % 6
		if !dense && (k == 2 || k == 4) && i%60 != 2 {
			k = 5
		}
		switch k {
		case 0:
			fmt.Fprintf(&b, "## Decision %d\nWe chose to keep the retry loop in the store layer because the handler cannot know about busy_timeout. See commit %040x.\n\n", i, i*7919)
		case 1:
			fmt.Fprintf(&b, "func handle%d(w http.ResponseWriter, r *http.Request) {\n\tid := r.PathValue(\"id\")\n\tif err := s.Update(id, timeout=5000); err != nil {\n\t\thttp.Error(w, err.Error(), 500)\n\t}\n}\n", i)
		case 2:
			fmt.Fprintf(&b, "{\"id\": %d, \"title\": \"fix auth bug\", \"project\": \"engram\", \"token\": \"%s\", \"scope\": \"project\"}\n", i, "tok_"+strings.Repeat("a1", 12))
		case 3:
			fmt.Fprintf(&b, "2026-10-19T10:%02d:00Z INFO server: GET /observations/%d status=200 took=3ms uuid=123e4567-e89b-12d3-a456-426614174000\n", i%60, i)
		case 4:
			fmt.Fprintf(&b, "export DATABASE_URL=postgres://app:%s@db.internal:5432/app\nAuthorization: Bearer %s\n", "pw"+strings.Repeat("x9", 6), "abc"+strings.Repeat("Z7", 12))
		case 5:
			b.WriteString("The user prefers short answers, wants the tests next to the code, and asked to keep the changelog in Spanish. Config: max_retries: 3, name = engram, mode: strict.\n")
		}
	}
	return b.String()[:size]
}

// fill repeats unit up to exactly size bytes.
func fill(unit string, size int) string {
	return strings.Repeat(unit, size/len(unit)+1)[:size]
}

func benchRedact(b *testing.B, in string) {
	b.SetBytes(int64(len(in)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Redact(in)
	}
}

func BenchmarkRedact(b *testing.B) {
	b.Run("prose_1KB", func(b *testing.B) { benchRedact(b, benchProse(1<<10)) })
	b.Run("prose_50KB", func(b *testing.B) { benchRedact(b, benchProse(50<<10)) })
	b.Run("prose_256KB", func(b *testing.B) { benchRedact(b, benchProse(MaxDetectBytes())) })
	b.Run("sparse_50KB", func(b *testing.B) { benchRedact(b, benchMix(50<<10, false)) })
	// Nothing to find: the common case for a long observation.
	b.Run("clean_50KB", func(b *testing.B) {
		benchRedact(b, fill("The quick brown fox jumps over the lazy dog; see docs and tests for the rest.\n", 50<<10))
	})
	// Dense key=value text: many candidates for the key-context passes.
	b.Run("keyvalue_50KB", func(b *testing.B) {
		benchRedact(b, fill("name = engram, mode: strict, retries=3, timeout: 5000, path=/tmp/x\n", 50<<10))
	})
	// The worst case of the linear-time tests: a long chain of candidates.
	b.Run("worst_token_chain_256KB", func(b *testing.B) {
		benchRedact(b, strings.Repeat("token=", MaxDetectBytes()/6))
	})
	b.Run("worst_chain_eq_256KB", func(b *testing.B) {
		benchRedact(b, strings.Repeat("a=", MaxDetectBytes()/2))
	})
}
