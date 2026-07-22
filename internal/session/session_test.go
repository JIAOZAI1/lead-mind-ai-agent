package session

import "testing"

func TestResolve_ClientSuppliedIDIsReused(t *testing.T) {
	got := Resolve("existing-session-id")
	if got != "existing-session-id" {
		t.Fatalf("Resolve should return the client-supplied ID as-is, got %q", got)
	}
}

func TestResolve_EmptyMintsNewID(t *testing.T) {
	got := Resolve("")
	if got == "" {
		t.Fatal("Resolve(\"\") should mint a non-empty ID")
	}

	other := Resolve("")
	if got == other {
		t.Fatalf("Resolve(\"\") should mint distinct IDs across calls, got %q twice", got)
	}
}

func TestNew_ProducesDistinctIDs(t *testing.T) {
	a := New()
	b := New()
	if a == b {
		t.Fatalf("New() should produce distinct IDs, got %q twice", a)
	}
	if a == "" || b == "" {
		t.Fatal("New() should never return an empty ID")
	}
}
