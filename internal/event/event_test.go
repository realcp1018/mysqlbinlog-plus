package event

import (
	"strings"
	"testing"
	"time"
)

var quoteStringBenchmarkResult string

// BenchmarkQuoteString measures SQL string escaping time and allocations.
func BenchmarkQuoteString(b *testing.B) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{name: "plain", value: "ordinary text value"},
		{name: "escaped", value: "O'Reilly\\path\x00\n\r中文"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				quoteStringBenchmarkResult = quoteString(tc.value)
			}
		})
	}
}

func TestOriginalInsertSQL(t *testing.T) {
	sql, err := (&RowChange{
		Schema: "app",
		Table:  "users",
		Type:   Insert,
		Cols: []Column{
			{Name: "id", PrimaryKey: true},
			{Name: "name"},
			{Name: "note"},
			{Name: "payload"},
			{Name: "created_at"},
		},
		After: []any{
			int64(1),
			"O'Reilly",
			"中文",
			[]byte{0xde, 0xad},
			time.Date(2026, 6, 30, 10, 0, 1, 0, time.UTC),
		},
	}).ToOriginalSQL(SQLOptions{})
	if err != nil {
		t.Fatalf("ToOriginalSQL returned error: %v", err)
	}

	want := "INSERT INTO `app`.`users` (`id`, `name`, `note`, `payload`, `created_at`) VALUES (1, 'O''Reilly', '中文', X'DEAD', '2026-06-30 10:00:01');"
	if sql != want {
		t.Fatalf("sql = %q, want %q", sql, want)
	}
}

func TestOriginalInsertOmitPrimaryKey(t *testing.T) {
	sql, err := (&RowChange{
		Schema: "app",
		Table:  "users",
		Type:   Insert,
		Cols: []Column{
			{Name: "id", PrimaryKey: true},
			{Name: "name"},
		},
		After: []any{int64(1), "alice"},
	}).ToOriginalSQL(SQLOptions{NoPrimaryKey: true})
	if err != nil {
		t.Fatalf("ToOriginalSQL returned error: %v", err)
	}

	want := "INSERT INTO `app`.`users` (`name`) VALUES ('alice');"
	if sql != want {
		t.Fatalf("sql = %q, want %q", sql, want)
	}
}

func TestOriginalUpdateSQL(t *testing.T) {
	sql, err := (&RowChange{
		Schema: "app",
		Table:  "users",
		Type:   Update,
		Cols: []Column{
			{Name: "id", PrimaryKey: true},
			{Name: "name"},
			{Name: "age"},
		},
		Before: []any{int64(1), "alice", int64(20)},
		After:  []any{int64(1), "alice", int64(21)},
	}).ToOriginalSQL(SQLOptions{})
	if err != nil {
		t.Fatalf("ToOriginalSQL returned error: %v", err)
	}

	want := "UPDATE `app`.`users` SET `age` = 21 WHERE `id` = 1;"
	if sql != want {
		t.Fatalf("sql = %q, want %q", sql, want)
	}
}

func TestRollbackSQL(t *testing.T) {
	sql, err := (&RowChange{
		Schema: "app",
		Table:  "users",
		Type:   Update,
		Cols: []Column{
			{Name: "id", PrimaryKey: true},
			{Name: "name"},
		},
		Before: []any{int64(1), "before"},
		After:  []any{int64(1), "after"},
	}).ToRollbackSQL(SQLOptions{})
	if err != nil {
		t.Fatalf("ToRollbackSQL returned error: %v", err)
	}

	want := "UPDATE `app`.`users` SET `name` = 'before' WHERE `id` = 1;"
	if sql != want {
		t.Fatalf("sql = %q, want %q", sql, want)
	}
}

func TestRollbackRejectsGeneratedColumnNames(t *testing.T) {
	_, err := (&RowChange{
		Schema: "app",
		Table:  "users",
		Type:   Delete,
		Cols: []Column{
			{Name: "column_1", GeneratedName: true},
			{Name: "column_2", GeneratedName: true},
		},
		Before: []any{int64(1), "alice"},
	}).ToRollbackSQL(SQLOptions{})
	if err == nil {
		t.Fatal("ToRollbackSQL returned nil error, want generated column name error")
	}
	if !strings.Contains(err.Error(), "generated column_N") {
		t.Fatalf("error = %v, want generated column_N hint", err)
	}
}

func TestOriginalUpdateBinaryValueComparison(t *testing.T) {
	sql, err := (&RowChange{
		Schema: "app",
		Table:  "files",
		Type:   Update,
		Cols: []Column{
			{Name: "id", PrimaryKey: true},
			{Name: "payload"},
			{Name: "name"},
		},
		Before: []any{int64(1), []byte{0xde, 0xad}, "old"},
		After:  []any{int64(1), []byte{0xde, 0xad}, "new"},
	}).ToOriginalSQL(SQLOptions{})
	if err != nil {
		t.Fatalf("ToOriginalSQL returned error: %v", err)
	}

	want := "UPDATE `app`.`files` SET `name` = 'new' WHERE `id` = 1;"
	if sql != want {
		t.Fatalf("sql = %q, want %q", sql, want)
	}
}

func TestOriginalUpdateChangedBinaryValue(t *testing.T) {
	sql, err := (&RowChange{
		Schema: "app",
		Table:  "files",
		Type:   Update,
		Cols: []Column{
			{Name: "id", PrimaryKey: true},
			{Name: "payload"},
		},
		Before: []any{int64(1), []byte{0xde, 0xad}},
		After:  []any{int64(1), []byte{0xbe, 0xef}},
	}).ToOriginalSQL(SQLOptions{})
	if err != nil {
		t.Fatalf("ToOriginalSQL returned error: %v", err)
	}

	want := "UPDATE `app`.`files` SET `payload` = X'BEEF' WHERE `id` = 1;"
	if sql != want {
		t.Fatalf("sql = %q, want %q", sql, want)
	}
}

func TestOriginalUpdateTextBytes(t *testing.T) {
	sql, err := (&RowChange{
		Schema: "app",
		Table:  "jobs",
		Type:   Update,
		Cols: []Column{
			{Name: "id", PrimaryKey: true},
			{Name: "message", Charset: "utf8mb4"},
		},
		Before: []any{int64(1), []byte("running")},
		After:  []any{int64(1), []byte("done")},
	}).ToOriginalSQL(SQLOptions{})
	if err != nil {
		t.Fatalf("ToOriginalSQL returned error: %v", err)
	}

	want := "UPDATE `app`.`jobs` SET `message` = 'done' WHERE `id` = 1;"
	if sql != want {
		t.Fatalf("sql = %q, want %q", sql, want)
	}
}

func TestDeleteFallsBackToAllColumnsWithoutPrimaryKey(t *testing.T) {
	sql, err := (&RowChange{
		Schema: "app",
		Table:  "logs",
		Type:   Delete,
		Cols: []Column{
			{Name: "message"},
			{Name: "deleted_at"},
		},
		Before: []any{"gone", nil},
	}).ToOriginalSQL(SQLOptions{})
	if err != nil {
		t.Fatalf("ToOriginalSQL returned error: %v", err)
	}

	want := "DELETE FROM `app`.`logs` WHERE `message` = 'gone' AND `deleted_at` IS NULL;"
	if sql != want {
		t.Fatalf("sql = %q, want %q", sql, want)
	}
}

func TestValidateChangeBeforeMismatchHintsRowImage(t *testing.T) {
	_, err := (&RowChange{
		Schema: "app",
		Table:  "users",
		Type:   Delete,
		Cols: []Column{
			{Name: "id", PrimaryKey: true},
			{Name: "name"},
		},
		Before: []any{int64(1)},
	}).ToOriginalSQL(SQLOptions{})
	if err == nil {
		t.Fatal("ToOriginalSQL returned nil error, want before mismatch")
	}
	if !strings.Contains(err.Error(), "binlog_row_image") {
		t.Fatalf("error = %v, want binlog_row_image hint", err)
	}
}

func TestValidateChangeAfterMismatchHintsRowImage(t *testing.T) {
	_, err := (&RowChange{
		Schema: "app",
		Table:  "users",
		Type:   Insert,
		Cols: []Column{
			{Name: "id", PrimaryKey: true},
			{Name: "name"},
		},
		After: []any{int64(1)},
	}).ToOriginalSQL(SQLOptions{})
	if err == nil {
		t.Fatal("ToOriginalSQL returned nil error, want after mismatch")
	}
	if !strings.Contains(err.Error(), "binlog_row_image") {
		t.Fatalf("error = %v, want binlog_row_image hint", err)
	}
}
