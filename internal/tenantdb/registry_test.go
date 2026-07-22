package tenantdb

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// fakeDB returns a *sql.DB that hasn't actually connected to anything —
// sql.Open is lazy, so this is safe to use in tests that only exercise
// the registry's bookkeeping (map membership, eviction timing) without
// touching the network.
func fakeDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", "user:pass@tcp(127.0.0.1:3306)/db")
	if err != nil {
		t.Fatalf("sql.Open should not fail eagerly: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestEvictIdle_RemovesOnlyStaleEntries(t *testing.T) {
	r := &Registry{
		idleEvictAfter: time.Minute,
		entries:        make(map[string]*poolEntry),
	}

	now := time.Now()
	r.entries["stale-tenant"] = &poolEntry{db: fakeDB(t), lastUsedAt: now.Add(-2 * time.Minute)}
	r.entries["fresh-tenant"] = &poolEntry{db: fakeDB(t), lastUsedAt: now}

	r.evictIdle(now)

	if _, ok := r.entries["stale-tenant"]; ok {
		t.Error("expected stale-tenant to be evicted")
	}
	if _, ok := r.entries["fresh-tenant"]; !ok {
		t.Error("expected fresh-tenant to remain cached")
	}
}

func TestEvictIdle_NoEntriesToEvict(t *testing.T) {
	r := &Registry{
		idleEvictAfter: time.Minute,
		entries:        make(map[string]*poolEntry),
	}
	r.entries["active"] = &poolEntry{db: fakeDB(t), lastUsedAt: time.Now()}

	r.evictIdle(time.Now())

	if _, ok := r.entries["active"]; !ok {
		t.Error("expected active entry to remain cached")
	}
}

func TestNewRegistry_CloseStopsEvictorAndClearsEntries(t *testing.T) {
	client := NewSSOClient("http://example.invalid", "token")
	r := NewRegistry(client, time.Minute, time.Minute)
	r.entries["t"] = &poolEntry{db: fakeDB(t), lastUsedAt: time.Now()}

	if err := r.Close(); err != nil {
		t.Fatalf("unexpected error closing registry: %v", err)
	}
	if len(r.entries) != 0 {
		t.Errorf("expected entries to be cleared after Close, got %d", len(r.entries))
	}
}
