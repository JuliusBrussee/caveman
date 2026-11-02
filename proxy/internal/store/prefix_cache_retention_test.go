package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/JuliusBrussee/caveman/proxy/internal/gateway"
)

// The provider can retain a prefix for 24 hours. Our batched LRU timestamp may
// trail its latest reuse by another ten minutes, so capacity pressure must not
// discard decisions in that window, including raw and pixel decisions.
func TestPrefixReplacementEvictionRetainsProviderCacheWindow(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "caveman.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stamp := time.Now().UTC().Add(-24*time.Hour - 5*time.Minute).Format(storeTSLayout)
	for i := 0; i < prefixCacheMaxEntries+5; i++ {
		handle, replacement := "ccr_test", []byte("COMPRESSED")
		if i < prefixCachePixelMaxEntries+5 {
			handle = gateway.PixelHandle
		} else if i%2 == 0 {
			handle, replacement = gateway.RawDecisionHandle, []byte{}
		}
		if _, err := tx.Exec(`INSERT INTO prefix_replacements
			(original_sha256, handle, replacement, created_at, last_used_at) VALUES (?,?,?,?,?)`,
			fmt.Sprintf("warm-%06d", i), handle, replacement, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
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
