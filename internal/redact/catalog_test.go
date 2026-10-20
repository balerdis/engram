package redact

import "testing"

// pinnedCatalogSHA256 changes only with a conscious re-vendor (make sync-catalog).
const pinnedCatalogSHA256 = "103802a42d09ffa48970714af54953e287ef81daec6467873140e01dcbc6cd68"

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
