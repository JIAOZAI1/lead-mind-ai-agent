// Package tenantdb provides the minimal per-tenant MySQL connection
// routing required by internal/session and internal/memory/longterm. Per
// PROJECT.md §4.2/§6.2, tenant DB connection info is never hardcoded or
// statically configured — it is fetched from sso-service on demand and
// cached, and this is the only sanctioned path to a tenant's *sql.DB.
package tenantdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// Header carrying the internal-call token sso-service uses to
// distinguish cluster-internal callers from external traffic (PROJECT.md
// §4.2).
const internalTokenHeader = "X-Internal-Token"

// DBInfo is a tenant's MySQL connection info as returned by sso-service.
type DBInfo struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Sentinel errors so callers can distinguish "tenant doesn't exist" from
// "we're not authorized to ask" from generic failures.
var (
	ErrTenantNotFound = fmt.Errorf("tenant not found")
	ErrUnauthorized   = fmt.Errorf("unauthorized internal call")
)

// SSOClient fetches tenant DB connection info from sso-service.
type SSOClient struct {
	baseURL       string
	internalToken string
	httpClient    *http.Client
}

// NewSSOClient builds a client against baseURL (e.g.
// http://sso-service.default.svc.cluster.local), authenticating internal
// calls with internalToken.
func NewSSOClient(baseURL, internalToken string) *SSOClient {
	return &SSOClient{
		baseURL:       baseURL,
		internalToken: internalToken,
		httpClient:    &http.Client{Timeout: 5 * time.Second},
	}
}

// FetchDBInfo looks up tenantCode's MySQL connection info.
func (c *SSOClient) FetchDBInfo(ctx context.Context, tenantCode string) (DBInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	url := fmt.Sprintf("%s/internal/tenants/%s/db-info", c.baseURL, tenantCode)
	log.Printf("fetching db-info for tenant %s from url: %s", tenantCode, url)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	log.Printf("building db-info request for tenant %s", tenantCode)

	if err != nil {
		return DBInfo{}, fmt.Errorf("build db-info request: %w", err)
	}
	req.Header.Set(internalTokenHeader, c.internalToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return DBInfo{}, fmt.Errorf("fetch db-info for tenant %s: %w", tenantCode, err)
	}
	defer resp.Body.Close()

	log.Printf("response body for tenant %s: %v", tenantCode, resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		var info DBInfo
		if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
			return DBInfo{}, fmt.Errorf("decode db-info for tenant %s: %w", tenantCode, err)
		}
		return info, nil
	case http.StatusNotFound:
		return DBInfo{}, fmt.Errorf("tenant %s: %w", tenantCode, ErrTenantNotFound)
	case http.StatusUnauthorized, http.StatusForbidden:
		return DBInfo{}, fmt.Errorf("tenant %s: %w", tenantCode, ErrUnauthorized)
	default:
		return DBInfo{}, fmt.Errorf("fetch db-info for tenant %s: unexpected status %d", tenantCode, resp.StatusCode)
	}
}
