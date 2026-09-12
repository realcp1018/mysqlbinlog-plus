package binlog

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	gomysql "github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"

	"mysqlbinlog-plus/internal/config"
	"mysqlbinlog-plus/internal/event"
)

// TestParseBinlogsCancellation stops between events and closes the input file.
func TestParseBinlogsCancellation(t *testing.T) {
	data := append([]byte(nil), replication.BinLogFileHeader...)
	for i := 0; i < 2; i++ {
		query := "CREATE TABLE app.t (id INT)"
		// A query event has a 19-byte event header and a 13-byte post-header,
		// followed by the empty schema's NUL terminator and the query text.
		raw := make([]byte, 19+14+len(query))
		binary.LittleEndian.PutUint32(raw, 1)
		raw[4] = byte(replication.QUERY_EVENT)
		binary.LittleEndian.PutUint32(raw[5:], 1)
		binary.LittleEndian.PutUint32(raw[9:], uint32(len(raw)))
		binary.LittleEndian.PutUint32(raw[13:], uint32(len(data)+len(raw)))
		copy(raw[33:], query)
		data = append(data, raw...)
	}
	path := filepath.Join(t.TempDir(), "mysql-bin.000001")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(config.Config{Mode: "local", Binlogs: []string{path}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	count := 0
	err := reader.ParseBinlogs(ctx, func(RowEvent) error {
		count++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ParseBinlogs error = %v, want context.Canceled", err)
	}
	if count != 1 {
		t.Fatalf("handled %d events, want 1", count)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("input file was not released: %v", err)
	}
}

// TestRestoreUnsignedRows verifies unsigned boundaries in generated original and rollback SQL.
func TestRestoreUnsignedRows(t *testing.T) {
	tests := []struct {
		name       string
		columnType byte
		value      any
		want       string
	}{
		{"tinyint", gomysql.MYSQL_TYPE_TINY, int8(-1), "255"},
		{"smallint", gomysql.MYSQL_TYPE_SHORT, int16(-1), "65535"},
		{"mediumint", gomysql.MYSQL_TYPE_INT24, int32(-1), "16777215"},
		{"int", gomysql.MYSQL_TYPE_LONG, int32(-1), "4294967295"},
		{"bigint", gomysql.MYSQL_TYPE_LONGLONG, int64(-1), "18446744073709551615"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			columns := []event.Column{{Name: "id", PrimaryKey: true, Unsigned: true}}
			rows := &replication.RowsEvent{
				Table: &replication.TableMapEvent{ColumnType: []byte{tt.columnType}},
				Rows:  [][]any{{tt.value}, {int64(0)}},
			}
			restoreUnsignedRows(rows, columns)
			change := event.RowChange{Schema: "app", Table: "t", Type: event.Update, Cols: columns, Before: rows.Rows[0], After: rows.Rows[1]}
			original, err := change.ToOriginalSQL(event.SQLOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if want := "UPDATE `app`.`t` SET `id` = 0 WHERE `id` = " + tt.want + ";"; original != want {
				t.Fatalf("original = %q, want %q", original, want)
			}
			rollback, err := change.ToRollbackSQL(event.SQLOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if want := "UPDATE `app`.`t` SET `id` = " + tt.want + " WHERE `id` = 0;"; rollback != want {
				t.Fatalf("rollback = %q, want %q", rollback, want)
			}
		})
	}
}

// TestRestoreUnsignedRowsPreservesValues verifies signed values, NULL, and binlog signedness take precedence.
func TestRestoreUnsignedRowsPreservesValues(t *testing.T) {
	for _, bitmap := range [][]byte{nil, {0}} {
		columns := []event.Column{{Unsigned: len(bitmap) > 0}, {Unsigned: true}, {Unsigned: true}}
		want := []any{int32(-1), nil, uint64(18446744073709551615)}
		rows := &replication.RowsEvent{
			Table: &replication.TableMapEvent{SignednessBitmap: bitmap, ColumnType: []byte{gomysql.MYSQL_TYPE_LONG, gomysql.MYSQL_TYPE_LONG, gomysql.MYSQL_TYPE_LONGLONG}},
			Rows:  [][]any{append([]any(nil), want...)},
		}
		restoreUnsignedRows(rows, columns)
		if !reflect.DeepEqual(rows.Rows[0], want) {
			t.Fatalf("bitmap %v: values = %v, want %v", bitmap, rows.Rows[0], want)
		}
	}
}

func TestUpdateParserState(t *testing.T) {
	tracker := &parserState{}

	tracker.update(&replication.BinlogEvent{
		Event: &replication.QueryEvent{Query: []byte("BEGIN")},
	}, true)
	if !tracker.inTransaction {
		t.Fatal("tracker is not in transaction after BEGIN")
	}

	tracker.update(&replication.BinlogEvent{
		Event: &replication.QueryEvent{Query: []byte("COMMIT")},
	}, true)
	if tracker.inTransaction {
		t.Fatal("tracker is still in transaction after COMMIT")
	}

	tracker.update(&replication.BinlogEvent{
		Event: &replication.QueryEvent{Query: []byte("BEGIN")},
	}, false)
	if !tracker.transactionBeforeRange {
		t.Fatal("tracker did not mark transaction as started before range")
	}
	tracker.update(&replication.BinlogEvent{
		Event: &replication.XIDEvent{},
	}, true)
	if tracker.inTransaction {
		t.Fatal("tracker is still in transaction after XID")
	}
	if tracker.transactionBeforeRange {
		t.Fatal("tracker still marks transaction before range after XID")
	}
}

func TestProcessEventRejectsMissingHeader(t *testing.T) {
	reader := NewReader(config.Config{}, nil)
	_, err := reader.processEvent(context.Background(), "mysql-bin.000001", &replication.BinlogEvent{}, nil, nil, &parserState{})
	if err == nil {
		t.Fatal("processEvent returned nil error for event without header")
	}
}

func TestEventStartPositionRejectsUnderflow(t *testing.T) {
	if got, ok := eventStartPosition(&replication.EventHeader{LogPos: 10, EventSize: 20}); ok || got != 0 {
		t.Fatalf("eventStartPosition() = (%d, %v), want (0, false)", got, ok)
	}
	if got, ok := eventStartPosition(&replication.EventHeader{LogPos: 120, EventSize: 20}); !ok || got != 100 {
		t.Fatalf("eventStartPosition() = (%d, %v), want (100, true)", got, ok)
	}
}

func TestProcessEventRejectsPositionRangeWithoutStartPosition(t *testing.T) {
	reader := NewReader(config.Config{
		Binlogs: []string{"mysql-bin.000001"},
		FromPos: 4,
	}, nil)
	_, err := reader.processEvent(context.Background(),
		"mysql-bin.000001",
		queryBinlogEvent("BEGIN", 10, 10),
		nil,
		nil,
		&parserState{},
	)
	if err == nil {
		t.Fatal("processEvent returned nil error for an invalid event position")
	}
}

func TestProcessEventAllowsMetadataWithoutStartPosition(t *testing.T) {
	reader := NewReader(config.Config{
		Binlogs: []string{"mysql-bin.000001"},
		FromPos: 4,
	}, nil)
	_, err := reader.processEvent(context.Background(),
		"mysql-bin.000001",
		&replication.BinlogEvent{
			Header: &replication.EventHeader{EventSize: 20, LogPos: 10},
			Event:  &replication.FormatDescriptionEvent{},
		},
		nil,
		nil,
		&parserState{},
	)
	if err != nil {
		t.Fatalf("processEvent returned error for metadata event: %v", err)
	}
}

func TestUpdateParserStateMarksSchemaUnreliableAfterDDL(t *testing.T) {
	tracker := &parserState{}

	tracker.update(&replication.BinlogEvent{
		Event: &replication.QueryEvent{Schema: []byte("app"), Query: []byte("ALTER TABLE t ADD COLUMN age INT")},
	}, true)
	if !tracker.schemaUnreliableFor("app", "t") {
		t.Fatal("tracker did not mark the altered table as unreliable")
	}
	if tracker.schemaUnreliableFor("app", "users") {
		t.Fatal("tracker marked an unrelated table as unreliable")
	}
}

// TestUpdateParserStateIgnoresViewDDL verifies that view changes do not invalidate table metadata.
func TestUpdateParserStateIgnoresViewDDL(t *testing.T) {
	queries := []string{
		"CREATE VIEW v AS SELECT id FROM app.t",
		"ALTER VIEW v AS SELECT id FROM app.t",
		"DROP VIEW v",
	}
	for _, query := range queries {
		tracker := &parserState{}
		tracker.update(&replication.BinlogEvent{
			Event: &replication.QueryEvent{Schema: []byte("app"), Query: []byte(query)},
		}, true)
		if tracker.schemaUnreliableFor("app", "t") {
			t.Errorf("query %q marked table metadata as unreliable", query)
		}
	}
}

// TestUpdateParserStateIgnoresMetadataNeutralDDL verifies that non-column DDL does not trigger fallback.
func TestUpdateParserStateIgnoresMetadataNeutralDDL(t *testing.T) {
	queries := []string{
		"CREATE DATABASE app",
		"CREATE INDEX idx ON app.t (id)",
		"DROP DATABASE app",
		"DROP INDEX idx ON app.t",
	}
	for _, query := range queries {
		tracker := &parserState{}
		tracker.update(&replication.BinlogEvent{
			Event: &replication.QueryEvent{Schema: []byte("app"), Query: []byte(query)},
		}, true)
		if tracker.schemaUnreliableAll || tracker.schemaUnreliableFor("app", "t") {
			t.Errorf("query %q marked metadata as unreliable", query)
		}
	}
}

// TestUpdateParserStateFallsBackGloballyForUnscopedDDL preserves safety when parsing fails.
func TestUpdateParserStateFallsBackGloballyForUnscopedDDL(t *testing.T) {
	tracker := &parserState{}

	tracker.update(&replication.BinlogEvent{
		Event: &replication.QueryEvent{Schema: []byte("app"), Query: []byte("ALTER TABLE")},
	}, true)
	if !tracker.schemaUnreliableFor("app", "users") {
		t.Fatal("tracker did not fall back to global unreliability")
	}
}

func TestUpdateParserStateKeepsSchemaReliableAfterTruncate(t *testing.T) {
	tracker := &parserState{}

	tracker.update(&replication.BinlogEvent{
		Event: &replication.QueryEvent{Query: []byte("TRUNCATE TABLE t")},
	}, true)
	if tracker.schemaUnreliableFor("app", "t") {
		t.Fatal("tracker marked schema as unreliable after TRUNCATE TABLE")
	}
}

func TestUpdateParserStateIgnoresDDLBeforeRange(t *testing.T) {
	tracker := &parserState{}

	tracker.update(&replication.BinlogEvent{
		Event: &replication.QueryEvent{Query: []byte("ALTER TABLE t ADD COLUMN age INT")},
	}, false)
	if tracker.schemaUnreliableFor("app", "t") {
		t.Fatal("tracker marked schema as unreliable before selected range")
	}
}

func TestIsSchemaChangingQuerySkipsLeadingComments(t *testing.T) {
	tests := []string{
		"/* migration */ ALTER TABLE t ADD COLUMN age INT",
		"/*!80000 ALTER TABLE t ADD COLUMN age INT */",
		"CREATE TEMPORARY TABLE tmp (id INT)",
	}
	for _, query := range tests {
		if !isSchemaChangingQuery(query) {
			t.Errorf("isSchemaChangingQuery(%q) = false, want true", query)
		}
	}
}

func TestConvertEmitsDDLByDefault(t *testing.T) {
	reader := NewReader(config.Config{}, nil)
	e := ddlBinlogEvent("ALTER TABLE t ADD COLUMN age INT")
	e.Event.(*replication.QueryEvent).Schema = []byte("app")
	events, err := reader.processEvent(context.Background(), "mysql-bin.000001", e, nil, nil, &parserState{})
	if err != nil {
		t.Fatalf("convert returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("convert returned %d events, want 1", len(events))
	}
	if got, want := events[0].Change.Type, event.DDL; got != want {
		t.Fatalf("event type = %q, want %q", got, want)
	}
	if got, want := events[0].DDLSQLText, "ALTER TABLE `app`.`t` ADD COLUMN `age` INT;"; got != want {
		t.Fatalf("DDLSQLText = %q, want %q", got, want)
	}
	if events[0].DDLSQLText == "" {
		t.Fatal("DDL event was treated as a rows event")
	}
}

// TestConvertEmitsMetadataNeutralDDL verifies that DDL output is independent of metadata fallback rules.
func TestConvertEmitsMetadataNeutralDDL(t *testing.T) {
	reader := NewReader(config.Config{SQLTypes: []string{string(event.DDL)}}, nil)
	e := ddlBinlogEvent("TRUNCATE TABLE t")
	e.Event.(*replication.QueryEvent).Schema = []byte("app")
	events, err := reader.processEvent(context.Background(), "mysql-bin.000001", e, nil, nil, &parserState{})
	if err != nil {
		t.Fatalf("convert returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("convert returned %d events, want 1", len(events))
	}
	if got, want := events[0].DDLSQLText, "TRUNCATE TABLE `app`.`t`;"; got != want {
		t.Fatalf("DDLSQLText = %q, want %q", got, want)
	}
}

// TestConvertSkipsNonDDLQuery verifies that SQL type matching does not turn transaction commands into DDL output.
func TestConvertSkipsNonDDLQuery(t *testing.T) {
	reader := NewReader(config.Config{SQLTypes: []string{string(event.DDL)}}, nil)
	events, err := reader.processEvent(context.Background(), "mysql-bin.000001", ddlBinlogEvent("BEGIN"), nil, nil, &parserState{})
	if err != nil {
		t.Fatalf("convert returned error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("convert returned %d events, want 0", len(events))
	}
}

// TestConvertKeepsDDLSchema verifies that qualified table names are preserved in DDL output.
func TestConvertKeepsDDLSchema(t *testing.T) {
	reader := NewReader(config.Config{}, nil)
	e := ddlBinlogEvent("ALTER TABLE other.t ADD COLUMN age INT")
	e.Event.(*replication.QueryEvent).Schema = []byte("app")
	events, err := reader.processEvent(context.Background(), "mysql-bin.000001", e, nil, nil, &parserState{})
	if err != nil {
		t.Fatalf("convert returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("convert returned %d events, want 1", len(events))
	}
	if got, want := events[0].DDLSQLText, "ALTER TABLE `other`.`t` ADD COLUMN `age` INT;"; got != want {
		t.Fatalf("DDLSQLText = %q, want %q", got, want)
	}
}

// TestConvertFiltersDDLByTablePattern verifies that DDL output follows table patterns.
func TestConvertFiltersDDLByTablePattern(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		wantCount int
	}{
		{name: "unrelated table", query: "ALTER TABLE cert MODIFY COLUMN name VARCHAR(256) NOT NULL", wantCount: 0},
		{name: "matching table", query: "ALTER TABLE redis_mem ADD COLUMN value TEXT", wantCount: 1},
		{name: "multiple tables with one match", query: "RENAME TABLE cert TO cert_new, redis_mem TO redis_mem_new", wantCount: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := NewReader(config.Config{TablePatterns: []string{"dba_admin.redis_mem"}}, nil)
			e := ddlBinlogEvent(tt.query)
			e.Event.(*replication.QueryEvent).Schema = []byte("dba_admin")
			events, err := reader.processEvent(context.Background(), "mysql-bin.000001", e, nil, nil, &parserState{})
			if err != nil {
				t.Fatalf("convert returned error: %v", err)
			}
			if len(events) != tt.wantCount {
				t.Fatalf("convert returned %d events, want %d", len(events), tt.wantCount)
			}
		})
	}
}

func TestConvertFiltersDDLBySQLType(t *testing.T) {
	reader := NewReader(config.Config{SQLTypes: []string{string(event.Insert)}}, nil)
	events, err := reader.processEvent(context.Background(), "mysql-bin.000001", ddlBinlogEvent("ALTER TABLE t ADD COLUMN age INT"), nil, nil, &parserState{})
	if err != nil {
		t.Fatalf("convert returned error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("convert returned %d events, want 0", len(events))
	}
}

func TestConvertDoesNotEmitDDLInRollbackMode(t *testing.T) {
	reader := NewReader(config.Config{
		Rollback: true,
		SQLTypes: []string{string(event.DDL)},
	}, nil)
	events, err := reader.processEvent(context.Background(), "mysql-bin.000001", ddlBinlogEvent("ALTER TABLE t ADD COLUMN age INT"), nil, nil, &parserState{})
	if err != nil {
		t.Fatalf("convert returned error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("convert returned %d events, want 0", len(events))
	}
}

func TestProcessEventCompletesTransactionBeyondToTime(t *testing.T) {
	reader := NewReader(config.Config{}, nil)
	state := &parserState{}
	from := time.Unix(10, 0)
	to := time.Unix(20, 0)

	if _, err := reader.processEvent(context.Background(), "mysql-bin.000001", queryBinlogEvent("BEGIN", 10, 120), &from, &to, state); err != nil {
		t.Fatalf("process BEGIN: %v", err)
	}
	if _, err := reader.processEvent(context.Background(), "mysql-bin.000001", queryBinlogEvent("COMMIT", 21, 140), &from, &to, state); err != nil {
		t.Fatalf("process COMMIT beyond to-time: %v", err)
	}
	if state.inTransaction {
		t.Fatal("transaction remains open after COMMIT")
	}
	_, err := reader.processEvent(context.Background(), "mysql-bin.000001", queryBinlogEvent("BEGIN", 21, 160), &from, &to, state)
	var stop stopParseError
	if !errors.As(err, &stop) {
		t.Fatalf("event after completed transaction error = %v, want stopParseError", err)
	}
}

func TestProcessEventSkipsTransactionStartedBeforeFromTime(t *testing.T) {
	reader := NewReader(config.Config{}, nil)
	state := &parserState{}
	from := time.Unix(10, 0)

	if _, err := reader.processEvent(context.Background(), "mysql-bin.000001", queryBinlogEvent("BEGIN", 9, 120), &from, nil, state); err != nil {
		t.Fatalf("process BEGIN: %v", err)
	}
	events, err := reader.processEvent(context.Background(), "mysql-bin.000001", queryBinlogEvent("ALTER TABLE t ADD COLUMN age INT", 11, 140), &from, nil, state)
	if err != nil {
		t.Fatalf("process event in skipped transaction: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("process event in skipped transaction returned %d events, want 0", len(events))
	}
	if _, err := reader.processEvent(context.Background(), "mysql-bin.000001", queryBinlogEvent("COMMIT", 12, 160), &from, nil, state); err != nil {
		t.Fatalf("process COMMIT: %v", err)
	}
}

func TestProcessEventCompletesTransactionBeyondToPos(t *testing.T) {
	reader := NewReader(config.Config{Binlogs: []string{"mysql-bin.000001"}, FromPos: 100, ToPos: 200}, nil)
	state := &parserState{}

	if _, err := reader.processEvent(context.Background(), "mysql-bin.000001", queryBinlogEvent("BEGIN", 10, 120), nil, nil, state); err != nil {
		t.Fatalf("process BEGIN: %v", err)
	}
	if _, err := reader.processEvent(context.Background(), "mysql-bin.000001", queryBinlogEvent("COMMIT", 11, 220), nil, nil, state); err != nil {
		t.Fatalf("process COMMIT beyond to-pos: %v", err)
	}
	_, err := reader.processEvent(context.Background(), "mysql-bin.000001", queryBinlogEvent("BEGIN", 12, 240), nil, nil, state)
	var stop stopParseError
	if !errors.As(err, &stop) {
		t.Fatalf("event after completed transaction error = %v, want stopParseError", err)
	}
}

func TestSelectRemoteBinlogStartFromLatestUsesAdjacentFileBoundary(t *testing.T) {
	base := time.Unix(100, 0)
	from := base.Add(90 * time.Minute)
	firstTimes := []time.Time{
		base.Add(120 * time.Minute),
		base.Add(60 * time.Minute),
		base,
	}

	if got, want := selectRemoteBinlogStartFromLatest(firstTimes, 2, &from), 0; got != want {
		t.Fatalf("start index = %d, want %d", got, want)
	}
}

func TestSelectRemoteBinlogStartFromLatestIncludesEarlierFileForSafety(t *testing.T) {
	base := time.Unix(100, 0)
	from := base.Add(150 * time.Minute)
	firstTimes := []time.Time{
		base.Add(180 * time.Minute),
		base.Add(120 * time.Minute),
		base.Add(60 * time.Minute),
		base,
	}

	if got, want := selectRemoteBinlogStartFromLatest(firstTimes, 3, &from), 1; got != want {
		t.Fatalf("start index = %d, want %d", got, want)
	}
}

func TestSelectRemoteBinlogStartFromLatestHandlesFirstFileAfterFromTime(t *testing.T) {
	from := time.Unix(100, 0)
	firstTimes := []time.Time{from.Add(time.Minute)}

	if got, want := selectRemoteBinlogStartFromLatest(firstTimes, 0, &from), 0; got != want {
		t.Fatalf("start index = %d, want %d", got, want)
	}
}

func TestSelectRemoteBinlogStartFromLatestHandlesMissingFirstTimes(t *testing.T) {
	from := time.Unix(100, 0)
	firstTimes := []time.Time{time.Time{}, time.Time{}}

	if got, want := selectRemoteBinlogStartFromLatest(firstTimes, 2, &from), 0; got != want {
		t.Fatalf("start index = %d, want %d", got, want)
	}
}

func TestSelectRemoteBinlogStartFromLatestWithoutFromTimeStartsAtOldest(t *testing.T) {
	if got, want := selectRemoteBinlogStartFromLatest(nil, 2, nil), 0; got != want {
		t.Fatalf("start index = %d, want %d", got, want)
	}
}

func TestRemoteEventTimestamp(t *testing.T) {
	tests := []struct {
		name string
		e    *replication.BinlogEvent
		want time.Time
		ok   bool
	}{
		{
			name: "format description uses header time",
			e: &replication.BinlogEvent{
				Header: &replication.EventHeader{Timestamp: 123},
				Event:  &replication.FormatDescriptionEvent{CreateTimestamp: 456},
			},
			want: time.Unix(123, 0),
			ok:   true,
		},
		{
			name: "format description falls back to create time",
			e: &replication.BinlogEvent{
				Header: &replication.EventHeader{},
				Event:  &replication.FormatDescriptionEvent{CreateTimestamp: 456},
			},
			want: time.Unix(456, 0),
			ok:   true,
		},
		{
			name: "format description without time is unusable",
			e: &replication.BinlogEvent{
				Header: &replication.EventHeader{},
				Event:  &replication.FormatDescriptionEvent{},
			},
			ok: false,
		},
		{
			name: "previous gtids has no event time",
			e: &replication.BinlogEvent{
				Header: &replication.EventHeader{Timestamp: 123},
				Event:  &replication.PreviousGTIDsEvent{},
			},
			ok: false,
		},
		{
			name: "regular event uses header time",
			e: &replication.BinlogEvent{
				Header: &replication.EventHeader{Timestamp: 789},
				Event:  &replication.QueryEvent{},
			},
			want: time.Unix(789, 0),
			ok:   true,
		},
		{
			name: "zero header time is unusable",
			e: &replication.BinlogEvent{
				Header: &replication.EventHeader{},
				Event:  &replication.QueryEvent{},
			},
			ok: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := remoteEventTimestamp(tt.e)
			if ok != tt.ok {
				t.Fatalf("remoteEventTimestamp ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Fatalf("event time = %v, want %v", got, tt.want)
			}
		})
	}
}

func ddlBinlogEvent(query string) *replication.BinlogEvent {
	return &replication.BinlogEvent{
		Header: &replication.EventHeader{
			Timestamp: uint32(time.Now().Unix()),
			EventSize: 20,
			LogPos:    120,
		},
		Event: &replication.QueryEvent{Query: []byte(query)},
	}
}

func queryBinlogEvent(query string, timestamp int64, logPos uint32) *replication.BinlogEvent {
	return &replication.BinlogEvent{
		Header: &replication.EventHeader{
			Timestamp: uint32(timestamp),
			EventSize: 20,
			LogPos:    logPos,
		},
		Event: &replication.QueryEvent{Query: []byte(query)},
	}
}
