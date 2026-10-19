// Package chunkcodec derives chunk identifiers. A chunk ID is the first 8 hex
// characters of the SHA-256 of the encoded chunk bytes. It must never change:
// the IDs recorded in .engram/manifest.json and in sync_chunks depend on it.
package chunkcodec

import (
	"crypto/sha256"
	"encoding/hex"
)

func ChunkID(payload []byte) string {
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])[:8]
}
