// Package tenantdb 提供 internal/session 与 internal/memory/longterm
// 所需的最小化按租户 MySQL 连接路由能力。根据 PROJECT.md §4.2/§6.2，
// 租户数据库连接信息绝不硬编码或静态配置——一律按需从 sso-service
// 获取并缓存，本包是获取租户 *sql.DB 的唯一合法途径。
package tenantdb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// internalTokenHeader 是携带内部调用 token 的请求头，sso-service 用它来
// 区分集群内部调用方与外部流量（PROJECT.md §4.2）。
const internalTokenHeader = "X-Internal-Token"

// DBInfo 是 sso-service 返回的某个租户的 MySQL 连接信息。
type DBInfo struct {
	Host     string `json:"dbHost"`
	Port     int    `json:"dbPort"`
	Database string `json:"dbName"`
	Username string `json:"dbUsername"`
	Password string `json:"dbPassword"`
}

// 预定义的哨兵错误，方便调用方区分"租户不存在"、"没有权限查询"和其他
// 一般性失败。
var (
	ErrTenantNotFound = fmt.Errorf("tenant not found")
	ErrUnauthorized   = fmt.Errorf("unauthorized internal call")
)

// SSOClient 用于从 sso-service 获取租户数据库连接信息。
type SSOClient struct {
	baseURL       string
	internalToken string
	httpClient    *http.Client
}

// NewSSOClient 基于 baseURL（例如
// http://sso-service.default.svc.cluster.local）构建客户端，
// internalToken 用于内部调用鉴权。
func NewSSOClient(baseURL, internalToken string) *SSOClient {
	return &SSOClient{
		baseURL:       baseURL,
		internalToken: internalToken,
		httpClient:    &http.Client{Timeout: 5 * time.Second},
	}
}

// FetchDBInfo 查询 tenantCode 对应的 MySQL 连接信息。
func (c *SSOClient) FetchDBInfo(ctx context.Context, tenantCode string) (DBInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	reqURL := fmt.Sprintf("%s/internal/tenants/%s/db-info", c.baseURL, tenantCode)
	log.Printf("fetching db-info for tenant %s from url: %s", tenantCode, reqURL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return DBInfo{}, fmt.Errorf("build db-info request: %w", err)
	}
	req.Header.Set(internalTokenHeader, c.internalToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return DBInfo{}, fmt.Errorf("fetch db-info for tenant %s: %w", tenantCode, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return DBInfo{}, fmt.Errorf("read db-info response for tenant %s: %w", tenantCode, err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var info DBInfo
		if err := json.Unmarshal(body, &info); err != nil {
			log.Printf("decode db-info failed for tenant %s: status=%d body_len=%d", tenantCode, resp.StatusCode, len(body))
			return DBInfo{}, fmt.Errorf("decode db-info for tenant %s: %w", tenantCode, err)
		}
		log.Printf("fetched db-info for tenant %s: status=%d host=%s db=%s", tenantCode, resp.StatusCode, info.Host, info.Database)
		return info, nil
	case http.StatusNotFound:
		return DBInfo{}, fmt.Errorf("tenant %s: %w", tenantCode, ErrTenantNotFound)
	case http.StatusUnauthorized, http.StatusForbidden:
		return DBInfo{}, fmt.Errorf("tenant %s: %w", tenantCode, ErrUnauthorized)
	default:
		return DBInfo{}, fmt.Errorf("fetch db-info for tenant %s: unexpected status %d, body=%s", tenantCode, resp.StatusCode, body)
	}
}
