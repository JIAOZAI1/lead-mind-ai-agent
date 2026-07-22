// Package session manages conversation session identity and metadata:
// minting/resolving session IDs, and (in store.go) the durable
// per-session record (title, pinned/archived state, activity
// timestamps) backing the session list UI. It does not hold conversation
// content itself — that's internal/memory/shortterm's job.
package session

import "github.com/google/uuid"

// New mints a new server-side session ID.
func New() string {
	return uuid.NewString()
}

// Resolve returns clientSuppliedID as-is if non-empty (a client-driven
// resume of an existing session), or mints a new one otherwise. A
// resumed ID that has no matching history/metadata yet behaves like a
// fresh session — that's not treated as an error anywhere in this
// package.
func Resolve(clientSuppliedID string) string {
	if clientSuppliedID != "" {
		return clientSuppliedID
	}
	return New()
}
