package tenantdb

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// poolEntry pairs a live connection pool with the db-info that produced
// it and bookkeeping used for the two independent expiries this registry
// applies: dbInfoExpiresAt governs when we must re-ask sso-service for
// possibly-rotated credentials, lastUsedAt governs when an idle pool (and
// the plaintext credentials it implies keeping around) gets evicted.
type poolEntry struct {
	db              *sql.DB
	dbInfoExpiresAt time.Time
	lastUsedAt      time.Time
}

// Registry resolves a tenant_code to a live *sql.DB, fetching connection
// info from sso-service on first use (or after the cached info expires)
// and caching the resulting pool. Per PROJECT.md §6.2, this is the only
// sanctioned way for this service to obtain a tenant database connection
// — callers must never construct one directly.
//
// Idle pools are evicted after idleEvictAfter of inactivity rather than
// held for the process lifetime: the entry holds the tenant's plaintext
// DB credentials in memory, so bounding how long an inactive tenant's
// credentials stay resident bounds the blast radius of a process
// compromise to "recently active tenants" instead of "every tenant ever
// served".
type Registry struct {
	client         *SSOClient
	dbInfoCacheTTL time.Duration
	idleEvictAfter time.Duration

	mu      sync.Mutex
	entries map[string]*poolEntry

	stopEvictor chan struct{}
	evictorDone chan struct{}
}

// NewRegistry builds a Registry. dbInfoCacheTTL bounds how long a
// sso-service db-info lookup is trusted before being re-fetched;
// idleEvictAfter bounds how long an unused tenant's connection pool (and
// its cached credentials) stays resident in memory. A background
// goroutine sweeps for idle entries every idleEvictAfter/2 (floored at
// 1s) — call Close to stop it.
func NewRegistry(client *SSOClient, dbInfoCacheTTL, idleEvictAfter time.Duration) *Registry {
	r := &Registry{
		client:         client,
		dbInfoCacheTTL: dbInfoCacheTTL,
		idleEvictAfter: idleEvictAfter,
		entries:        make(map[string]*poolEntry),
		stopEvictor:    make(chan struct{}),
		evictorDone:    make(chan struct{}),
	}
	go r.runEvictor()
	return r
}

// Get returns tenantCode's *sql.DB, dialing a new pool if none is cached
// or the cached db-info has expired.
func (r *Registry) Get(ctx context.Context, tenantCode string) (*sql.DB, error) {
	now := time.Now()

	r.mu.Lock()
	entry, ok := r.entries[tenantCode]
	if ok && now.Before(entry.dbInfoExpiresAt) {
		entry.lastUsedAt = now
		db := entry.db
		r.mu.Unlock()
		return db, nil
	}
	r.mu.Unlock()

	info, err := r.client.FetchDBInfo(ctx, tenantCode)
	if err != nil {
		return nil, fmt.Errorf("resolve db connection for tenant %s: %w", tenantCode, err)
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true", info.Username, info.Password, info.Host, info.Port, info.Database)
	log.Printf("tenant %s db dsn: %s:***@tcp(%s:%d)/%s?parseTime=true", tenantCode, info.Username, info.Host, info.Port, info.Database)

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db pool for tenant %s: %w", tenantCode, err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping db for tenant %s: %w", tenantCode, err)
	}

	r.mu.Lock()
	if old, ok := r.entries[tenantCode]; ok && old.db != db {
		old.db.Close()
	}
	r.entries[tenantCode] = &poolEntry{
		db:              db,
		dbInfoExpiresAt: now.Add(r.dbInfoCacheTTL),
		lastUsedAt:      now,
	}
	r.mu.Unlock()

	return db, nil
}

// Close shuts down every cached pool and stops the idle evictor.
func (r *Registry) Close() error {
	close(r.stopEvictor)
	<-r.evictorDone

	r.mu.Lock()
	defer r.mu.Unlock()

	var firstErr error
	for tenantCode, entry := range r.entries {
		if err := entry.db.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close db pool for tenant %s: %w", tenantCode, err)
		}
		delete(r.entries, tenantCode)
	}
	return firstErr
}

func (r *Registry) runEvictor() {
	defer close(r.evictorDone)

	interval := r.idleEvictAfter / 2
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopEvictor:
			return
		case now := <-ticker.C:
			r.evictIdle(now)
		}
	}
}

func (r *Registry) evictIdle(now time.Time) {
	r.mu.Lock()
	var toClose []*sql.DB
	for tenantCode, entry := range r.entries {
		if now.Sub(entry.lastUsedAt) >= r.idleEvictAfter {
			toClose = append(toClose, entry.db)
			delete(r.entries, tenantCode)
		}
	}
	r.mu.Unlock()

	for _, db := range toClose {
		db.Close()
	}
}
