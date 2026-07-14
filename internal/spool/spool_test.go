package spool

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestStoreReadReverse(t *testing.T) {
	ctx := context.Background()
	store := NewStore(t.TempDir() + "/spool")
	if err := store.Init(); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	records := []Record{
		{BinlogFile: "mysql-bin.000010", StartPos: 1, EndPos: 2, EventTime: time.Unix(1, 0), SchemaName: "app", TableName: "users", EventType: "insert", SQLText: "sql-10-1"},
		{BinlogFile: "mysql-bin.000010", StartPos: 2, EndPos: 3, EventTime: time.Unix(2, 0), SchemaName: "app", TableName: "users", EventType: "update", SQLText: "sql-10-2"},
		{BinlogFile: "mysql-bin.000011", StartPos: 1, EndPos: 2, EventTime: time.Unix(3, 0), SchemaName: "app", TableName: "users", EventType: "delete", SQLText: "sql-11-1"},
	}
	for _, record := range records {
		if err := store.Append(ctx, record); err != nil {
			t.Fatalf("Append returned error: %v", err)
		}
	}

	var got []string
	err := store.ReadReverse(ctx, []string{"mysql-bin.000010", "mysql-bin.000011"}, func(record Record) error {
		got = append(got, record.SQLText)
		return nil
	})
	if err != nil {
		t.Fatalf("ReadReverse returned error: %v", err)
	}

	want := []string{"sql-11-1", "sql-10-2", "sql-10-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadReverse SQL = %v, want %v", got, want)
	}
}

func TestStoreCountRecordsAndReadReverseRange(t *testing.T) {
	ctx := context.Background()
	store := NewStore(t.TempDir() + "/spool")
	if err := store.Init(); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	for _, sqlText := range []string{"first", "second", "third", "fourth"} {
		if err := store.Append(ctx, Record{BinlogFile: "mysql-bin.000010", SQLText: sqlText}); err != nil {
			t.Fatalf("Append returned error: %v", err)
		}
	}

	count, err := store.CountRecords(ctx, "mysql-bin.000010")
	if err != nil {
		t.Fatalf("CountRecords returned error: %v", err)
	}
	if count != 4 {
		t.Fatalf("CountRecords = %d, want 4", count)
	}

	var got []string
	err = store.ReadReverseRange(ctx, "mysql-bin.000010", 2, 3, func(record Record) error {
		got = append(got, record.SQLText)
		return nil
	})
	if err != nil {
		t.Fatalf("ReadReverseRange returned error: %v", err)
	}

	want := []string{"third", "second"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadReverseRange SQL = %v, want %v", got, want)
	}
}

func TestStoreSkipsMissingBinlogFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "spool"))
	if err := store.Init(); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	count, err := store.CountRecords(ctx, "mysql-bin.000010")
	if err != nil {
		t.Fatalf("CountRecords returned error: %v", err)
	}
	if count != 0 {
		t.Fatalf("CountRecords = %d, want 0", count)
	}
	if err := store.ReadReverse(ctx, []string{"mysql-bin.000010"}, func(Record) error {
		t.Fatal("ReadReverse emitted a record for a missing binlog file")
		return nil
	}); err != nil {
		t.Fatalf("ReadReverse returned error: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "spool"))
	if err != nil {
		t.Fatalf("ReadDir returned error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("spool contains %d files, want no SQLite file", len(entries))
	}
}

func TestWriterCommitsRecords(t *testing.T) {
	ctx := context.Background()
	store := NewStore(t.TempDir() + "/spool")
	if err := store.Init(); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	writer, err := store.NewWriter(ctx, "mysql-bin.000010")
	if err != nil {
		t.Fatalf("NewWriter returned error: %v", err)
	}
	if err := writer.Append(ctx, Record{SQLText: "first"}); err != nil {
		t.Fatalf("Append first returned error: %v", err)
	}
	if err := writer.Append(ctx, Record{SQLText: "second"}); err != nil {
		t.Fatalf("Append second returned error: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	var got []string
	err = store.ReadReverse(ctx, []string{"mysql-bin.000010"}, func(record Record) error {
		got = append(got, record.SQLText)
		return nil
	})
	if err != nil {
		t.Fatalf("ReadReverse returned error: %v", err)
	}

	want := []string{"second", "first"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadReverse SQL = %v, want %v", got, want)
	}
}

func TestWriterAbortRollsBackRecords(t *testing.T) {
	ctx := context.Background()
	store := NewStore(t.TempDir() + "/spool")
	if err := store.Init(); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	writer, err := store.NewWriter(ctx, "mysql-bin.000010")
	if err != nil {
		t.Fatalf("NewWriter returned error: %v", err)
	}
	if err := writer.Append(ctx, Record{SQLText: "rolled-back"}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	if err := writer.Abort(); err != nil {
		t.Fatalf("Abort returned error: %v", err)
	}

	var got []string
	err = store.ReadReverse(ctx, []string{"mysql-bin.000010"}, func(record Record) error {
		got = append(got, record.SQLText)
		return nil
	})
	if err != nil {
		t.Fatalf("ReadReverse returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ReadReverse SQL = %v, want empty after abort", got)
	}
}

func TestStoreInitAllowsExistingEmptyDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}

	store := NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
}

func TestStoreInitRejectsNonEmptyDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "existing"), []byte("data"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	store := NewStore(dir)
	if err := store.Init(); err == nil {
		t.Fatal("Init returned nil error, want non-empty dir error")
	}
}

func TestStoreCleanupRemovesContentsButKeepsDir(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "spool")
	store := NewStore(dir)
	if err := store.Init(); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}
	if err := store.Append(ctx, Record{BinlogFile: "mysql-bin.000010", SQLText: "sql"}); err != nil {
		t.Fatalf("Append returned error: %v", err)
	}
	if err := store.Cleanup(); err != nil {
		t.Fatalf("Cleanup returned error: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("spool dir was not kept: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir returned error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("spool dir contains %d entries, want empty", len(entries))
	}
}
