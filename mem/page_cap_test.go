package mem

import "testing"

// The page cap belongs to the CCR store only. The memories database must keep
// the SQLite default.
func TestMemoriesDatabaseHasNoPageCap(t *testing.T) {
	s := newStore(t)
	var pages int64
	if err := s.db.QueryRow(`PRAGMA max_page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if pages < 1<<30-1 {
		t.Fatalf("max_page_count = %d, want the SQLite default", pages)
	}
}
