package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

// The provider can retain a prefix for 24 hours. Our batched LRU timestamp may
// trail its latest reuse by another ten minutes, so capacity pressure must not
// discard decisions in that window, including raw and pixel decisions.
func TestPrefixReplacementEvictionRetainsProviderCacheWindow(t *testing.T) {
	t.Parallel()
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stamp := time.Now().UTC().Add(-24*time.Hour - 5*time.Minute).Format(storeTSLayout)
	if _, err := s.db.Exec(
		`WITH RECURSIVE n(v) AS (SELECT 0 UNION ALL SELECT v+1 FROM n WHERE v < ?)
		 INSERT INTO prefix_replacements (original_sha256, handle, replacement, created_at, last_used_at)
		 SELECT
		   printf('warm-%06d', v),
		   CASE WHEN v < ? THEN ? WHEN v % 2 = 0 THEN ? ELSE 'ccr_test' END,
		   CASE WHEN v >= ? AND v % 2 = 0 THEN zeroblob(0) ELSE ? END,
		   ?, ?
		 FROM n`,
		prefixCacheMaxEntries+4,
		prefixCachePixelMaxEntries+5, gateway.PixelHandle, gateway.RawDecisionHandle,
		prefixCachePixelMaxEntries+5, []byte("COMPRESSED"),
		stamp, stamp,
	); err != nil {
		t.Fatal(err)
	}
	s.evictPrefixReplacements()
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM prefix_replacements`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != prefixCacheMaxEntries+5 {
		t.Fatalf("capacity eviction discarded %d decisions while provider prefixes may still be cached", prefixCacheMaxEntries+5-count)
	}
}
