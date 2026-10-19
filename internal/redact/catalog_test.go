package redact

import "testing"

// pinnedCatalogSHA256 changes only with a conscious re-vendor (make sync-catalog).
const pinnedCatalogSHA256 = "be66e239c3a1d0394f9c5db5c1bad61c292b446a3867dba505439c2114790e31"

func TestCatalogIsPinned(t *testing.T) {
	if got := CatalogSHA256(); got != pinnedCatalogSHA256 {
		t.Fatalf("catalog.json changed: sha256 %s, pinned %s. If the re-vendor is intended, update the pin and re-run the golden generator", got, pinnedCatalogSHA256)
	}
}

func TestCatalogLoads(t *testing.T) {
	if cat.minLen != 8 || cat.maxDetect != 262144 || len(cat.headers) != 3 || len(cat.known) != 6 {
		t.Fatalf("unexpected catalog: %+v", cat)
	}
}
