package redact

import "testing"

// pinnedCatalogSHA256 changes only with a conscious re-vendor (make sync-catalog).
const pinnedCatalogSHA256 = "e1ff6e304e985ceb349411c3d6d291019d5107f3301728ff8127b76fc4cfb769"

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
