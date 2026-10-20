package redact

import "testing"

// pinnedCatalogSHA256 changes only with a conscious re-vendor (make sync-catalog).
const pinnedCatalogSHA256 = "0b5d630bf9286616e6ec0d3ec0e29464c79d73d0f21a5eec8f8c4eaf456136a9"

func TestCatalogIsPinned(t *testing.T) {
	if got := CatalogSHA256(); got != pinnedCatalogSHA256 {
		t.Fatalf("catalog.json changed: sha256 %s, pinned %s. If the re-vendor is intended, update the pin and re-run the golden generator", got, pinnedCatalogSHA256)
	}
}

func TestCatalogLoads(t *testing.T) {
	if cat.minLen != 8 || cat.maxDetect != 262144 || len(cat.headers) != 3 || len(cat.known) != 12 {
		t.Fatalf("unexpected catalog: %+v", cat)
	}
}
