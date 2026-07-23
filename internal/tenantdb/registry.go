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

// poolEntry 将一个存活的连接池与产生它的 db-info 绑定在一起，并记录本
// registry 所使用的两种独立过期机制所需的信息：dbInfoExpiresAt 决定何时
// 必须重新向 sso-service 询问（凭据可能已轮换），lastUsedAt 决定一个
// 空闲连接池（以及它随之驻留在内存中的明文凭据）何时被淘汰。
type poolEntry struct {
	db              *sql.DB
	dbInfoExpiresAt time.Time
	lastUsedAt      time.Time
}

// Registry 将 tenant_code 解析为一个存活的 *sql.DB：首次使用时（或缓存
// 的连接信息过期后）从 sso-service 拉取连接信息，并缓存生成的连接池。
// 根据 PROJECT.md §6.2，这是本服务获取租户数据库连接的唯一合法方式
// ——调用方不得自行直接构造。
//
// 空闲连接池会在 idleEvictAfter 无活动后被淘汰，而不是在进程生命周期内
// 一直持有：每个 entry 在内存中保存着该租户的明文数据库凭据，因此限制
// 一个不活跃租户的凭据在内存中驻留的时长，能把进程被攻破后的影响范围
// 限定在"近期活跃的租户"，而不是"曾经服务过的所有租户"。
type Registry struct {
	client         *SSOClient
	dbInfoCacheTTL time.Duration
	idleEvictAfter time.Duration

	mu      sync.Mutex
	entries map[string]*poolEntry

	stopEvictor chan struct{}
	evictorDone chan struct{}
}

// NewRegistry 构建一个 Registry。dbInfoCacheTTL 限定一次从 sso-service
// 查到的 db-info 在被重新拉取之前可信任多久；idleEvictAfter 限定一个
// 未被使用的租户连接池（及其缓存的凭据）在内存中驻留多久。后台 goroutine
// 每隔 idleEvictAfter/2（下限 1 秒）扫描一次空闲 entry——调用 Close
// 可停止该 goroutine。
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

// Get 返回 tenantCode 对应的 *sql.DB；如果没有缓存或缓存的 db-info
// 已过期，则会新建一个连接池。
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

// Close 关闭所有缓存的连接池，并停止空闲淘汰 goroutine。
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
