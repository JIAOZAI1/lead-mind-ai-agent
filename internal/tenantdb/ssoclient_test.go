package tenantdb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchDBInfo_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(internalTokenHeader) != "secret-token" {
			t.Errorf("expected X-Internal-Token header to be set")
		}
		if r.URL.Path != "/internal/tenants/tenant-a/db-info" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(DBInfo{
			Host: "db-a.internal", Port: 3306, Database: "tenant_a", Username: "u", Password: "p",
		})
	}))
	defer srv.Close()

	client := NewSSOClient(srv.URL, "secret-token")
	info, err := client.FetchDBInfo(context.Background(), "tenant-a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Host != "db-a.internal" || info.Database != "tenant_a" {
		t.Fatalf("unexpected db info: %+v", info)
	}
}

func TestFetchDBInfo_TenantNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := NewSSOClient(srv.URL, "token")
	_, err := client.FetchDBInfo(context.Background(), "unknown-tenant")
	if !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %v", err)
	}
}

func TestFetchDBInfo_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := NewSSOClient(srv.URL, "wrong-token")
	_, err := client.FetchDBInfo(context.Background(), "tenant-a")
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
}

func TestFetchDBInfo_UnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewSSOClient(srv.URL, "token")
	_, err := client.FetchDBInfo(context.Background(), "tenant-a")
	if err == nil {
		t.Fatal("expected an error for 500 response")
	}
}
