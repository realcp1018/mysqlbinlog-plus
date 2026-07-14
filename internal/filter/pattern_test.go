package filter

import (
	"reflect"
	"testing"
)

func TestNormalizePatterns(t *testing.T) {
	got, err := NormalizePatterns([]string{"users", "*", "app.orders"})
	if err != nil {
		t.Fatalf("NormalizePatterns returned error: %v", err)
	}

	want := []string{"*.users", "*.*", "app.orders"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizePatterns = %v, want %v", got, want)
	}
}

func TestNormalizePatternsRejectsInvalidPattern(t *testing.T) {
	if _, err := NormalizePatterns([]string{"app."}); err == nil {
		t.Fatal("NormalizePatterns returned nil error, want invalid pattern error")
	}
}

func TestMatchAny(t *testing.T) {
	patterns, err := NormalizePatterns([]string{"app.user?", "audit"})
	if err != nil {
		t.Fatalf("NormalizePatterns returned error: %v", err)
	}

	if !MatchAny(patterns, "app", "users") {
		t.Fatal("MatchAny did not match app.users")
	}
	if !MatchAny(patterns, "any", "audit") {
		t.Fatal("MatchAny did not match any.audit")
	}
	if MatchAny(patterns, "app", "orders") {
		t.Fatal("MatchAny matched app.orders unexpectedly")
	}
}
