package mysql

import (
	"context"
	"errors"
	"testing"
	"time"

	"mysqlbinlog-plus/internal/config"
	"mysqlbinlog-plus/internal/event"
)

// TestResolveReturnsContextError verifies canceled queries do not fall back to binlog metadata.
func TestResolveReturnsContextError(t *testing.T) {
	client, err := Open(config.Config{Host: "127.0.0.1", Port: 1, User: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	for _, ctx := range []context.Context{canceled, expired} {
		resolver := NewSchemaResolver(client)
		columns, _, err := resolver.Resolve(ctx, "app", "users", []event.Column{{Name: "id"}})
		if !errors.Is(err, ctx.Err()) {
			t.Fatalf("Resolve error = %v, want %v", err, ctx.Err())
		}
		if columns != nil {
			t.Fatalf("Resolve returned fallback columns after cancellation: %v", columns)
		}
	}
}

func TestGenerateServerID(t *testing.T) {
	if got := GenerateServerID(); got == 0 {
		t.Fatal("GenerateServerID returned 0")
	}
}
