package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"mysqlbinlog-plus/internal/binlog"
	"mysqlbinlog-plus/internal/config"
	"mysqlbinlog-plus/internal/event"
	"mysqlbinlog-plus/internal/spool"
)

// TestWriteRollbackChunksCanceled preserves existing output and cached records.
func TestWriteRollbackChunksCanceled(t *testing.T) {
	dir := t.TempDir()
	store := spool.NewStore(filepath.Join(dir, "spool"))
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	appendRollbackRecords(t, context.Background(), store, []spool.Record{
		{BinlogFile: "mysql-bin.000001", SQLText: "SELECT 1;"},
	})
	output := filepath.Join(dir, "rollback.sql")
	if err := os.WriteFile(output+".000001", []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := writeRollbackChunks(ctx, store, config.Config{
		Binlogs: []string{"mysql-bin.000001"}, Output: output, OutputChunkSize: 1,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("writeRollbackChunks error = %v, want context.Canceled", err)
	}
	assertFileContent(t, output+".000001", "existing")
	count, err := store.CountRecords(context.Background(), "mysql-bin.000001")
	if err != nil || count != 1 {
		t.Fatalf("cached record count = %d, error = %v; want 1, nil", count, err)
	}
	if err := writeRollbackChunk(ctx, store, output, rollbackChunkPlan{index: 2}); !errors.Is(err, context.Canceled) {
		t.Fatalf("writeRollbackChunk error = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(output + ".000002"); !os.IsNotExist(err) {
		t.Fatalf("unexpected second output file: %v", err)
	}
}

func TestSQLWriterSplitOutput(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "t.sql")
	writer, err := newSQLWriter(config.Config{
		Output:          output,
		OutputChunkSize: 2,
	})
	if err != nil {
		t.Fatalf("newSQLWriter returned error: %v", err)
	}
	defer writer.Close()

	for _, sqlText := range []string{"one", "two", "three"} {
		if err := writer.Write(sqlText); err != nil {
			t.Fatalf("Write returned error: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	first, err := os.ReadFile(output + ".000001")
	if err != nil {
		t.Fatalf("ReadFile first output: %v", err)
	}
	second, err := os.ReadFile(output + ".000002")
	if err != nil {
		t.Fatalf("ReadFile second output: %v", err)
	}
	if got, want := string(first), "SET NAMES utf8mb4;\nSET SESSION sql_mode = REPLACE(@@SESSION.sql_mode, 'NO_BACKSLASH_ESCAPES', '');\none\ntwo\n"; got != want {
		t.Fatalf("first output = %q, want %q", got, want)
	}
	if got, want := string(second), "SET NAMES utf8mb4;\nSET SESSION sql_mode = REPLACE(@@SESSION.sql_mode, 'NO_BACKSLASH_ESCAPES', '');\nthree\n"; got != want {
		t.Fatalf("second output = %q, want %q", got, want)
	}
}

func TestSQLWriterSplitOutputDoesNotCreateEmptyFile(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "t.sql")
	writer, err := newSQLWriter(config.Config{
		Output:          output,
		OutputChunkSize: 2,
	})
	if err != nil {
		t.Fatalf("newSQLWriter returned error: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir returned error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("output dir contains %d entries, want empty", len(entries))
	}
}

func TestRollbackEventHandlerCommitsOnBinlogChange(t *testing.T) {
	ctx := context.Background()
	store := spool.NewStore(filepath.Join(t.TempDir(), "spool"))
	if err := store.Init(); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	handler := newRollbackEventHandler(ctx, store, false)
	for _, rowEvent := range []binlog.RowEvent{
		rollbackInsertEvent("mysql-bin.000010", 1),
		rollbackInsertEvent("mysql-bin.000011", 2),
	} {
		if err := handler.Handle(rowEvent); err != nil {
			t.Fatalf("Handle returned error: %v", err)
		}
	}
	if err := handler.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	var got []string
	err := store.ReadReverse(ctx, []string{"mysql-bin.000010", "mysql-bin.000011"}, func(record spool.Record) error {
		got = append(got, record.SQLText)
		return nil
	})
	if err != nil {
		t.Fatalf("ReadReverse returned error: %v", err)
	}
	want := []string{
		"DELETE FROM `app`.`users` WHERE `id` = 2;",
		"DELETE FROM `app`.`users` WHERE `id` = 1;",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rollback SQL = %v, want %v", got, want)
	}
}

func TestWriteRollbackChunksSplitsGlobalReverseOrder(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := spool.NewStore(filepath.Join(dir, "spool"))
	if err := store.Init(); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	records := []spool.Record{
		{BinlogFile: "mysql-bin.000010", SQLText: "sql-10-1"},
		{BinlogFile: "mysql-bin.000010", SQLText: "sql-10-2"},
		{BinlogFile: "mysql-bin.000010", SQLText: "sql-10-3"},
		{BinlogFile: "mysql-bin.000011", SQLText: "sql-11-1"},
		{BinlogFile: "mysql-bin.000011", SQLText: "sql-11-2"},
	}
	appendRollbackRecords(t, ctx, store, records)

	output := filepath.Join(dir, "rollback.sql")
	err := writeRollbackChunks(ctx, store, config.Config{
		Binlogs:         []string{"mysql-bin.000010", "mysql-bin.000011"},
		Output:          output,
		OutputChunkSize: 2,
	})
	if err != nil {
		t.Fatalf("writeRollbackChunks returned error: %v", err)
	}

	assertFileContent(t, output+".000001", "SET NAMES utf8mb4;\nSET SESSION sql_mode = REPLACE(@@SESSION.sql_mode, 'NO_BACKSLASH_ESCAPES', '');\nsql-11-2\nsql-11-1\n")
	assertFileContent(t, output+".000002", "SET NAMES utf8mb4;\nSET SESSION sql_mode = REPLACE(@@SESSION.sql_mode, 'NO_BACKSLASH_ESCAPES', '');\nsql-10-3\nsql-10-2\n")
	assertFileContent(t, output+".000003", "SET NAMES utf8mb4;\nSET SESSION sql_mode = REPLACE(@@SESSION.sql_mode, 'NO_BACKSLASH_ESCAPES', '');\nsql-10-1\n")
}

func TestWriteRollbackChunksCreatesEmptyFirstChunk(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store := spool.NewStore(filepath.Join(dir, "spool"))
	if err := store.Init(); err != nil {
		t.Fatalf("Init returned error: %v", err)
	}

	output := filepath.Join(dir, "rollback.sql")
	if err := writeRollbackChunks(ctx, store, config.Config{
		Binlogs:         []string{"mysql-bin.000010"},
		Output:          output,
		OutputChunkSize: 2,
	}); err != nil {
		t.Fatalf("writeRollbackChunks returned error: %v", err)
	}
	assertFileContent(t, output+".000001", "")
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s returned error: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, string(got), want)
	}
}

func appendRollbackRecords(t *testing.T, ctx context.Context, store *spool.Store, records []spool.Record) {
	t.Helper()

	var writer *spool.Writer
	binlogFile := ""
	for _, record := range records {
		if record.BinlogFile != binlogFile {
			if writer != nil {
				if err := writer.Close(); err != nil {
					t.Fatalf("Close returned error: %v", err)
				}
			}
			var err error
			writer, err = store.NewWriter(ctx, record.BinlogFile)
			if err != nil {
				t.Fatalf("NewWriter returned error: %v", err)
			}
			binlogFile = record.BinlogFile
		}
		if err := writer.Append(ctx, record); err != nil {
			t.Fatalf("Append returned error: %v", err)
		}
	}
	if writer != nil {
		if err := writer.Close(); err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	}
}

func rollbackInsertEvent(file string, id int64) binlog.RowEvent {
	return binlog.RowEvent{
		File:      file,
		StartPos:  4,
		EndPos:    120,
		EventTime: time.Unix(id, 0),
		Change: event.RowChange{
			Schema: "app",
			Table:  "users",
			Type:   event.Insert,
			Cols:   []event.Column{{Name: "id", PrimaryKey: true}},
			After:  []any{id},
		},
	}
}
