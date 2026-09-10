package binlog

import (
	"errors"
	"testing"
	"time"

	"github.com/go-mysql-org/go-mysql/replication"

	"mysqlbinlog-plus/internal/config"
	"mysqlbinlog-plus/internal/event"
)

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
	_, err := reader.processEvent("mysql-bin.000001", &replication.BinlogEvent{}, nil, nil, &parserState{})
	if err == nil {
		t.Fatal("processEvent returned nil error for event without header")
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
	events, err := reader.processEvent("mysql-bin.000001", e, nil, nil, &parserState{})
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

// TestConvertKeepsDDLSchema verifies that qualified table names are preserved in DDL output.
func TestConvertKeepsDDLSchema(t *testing.T) {
	reader := NewReader(config.Config{}, nil)
	e := ddlBinlogEvent("ALTER TABLE other.t ADD COLUMN age INT")
	e.Event.(*replication.QueryEvent).Schema = []byte("app")
	events, err := reader.processEvent("mysql-bin.000001", e, nil, nil, &parserState{})
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
			events, err := reader.processEvent("mysql-bin.000001", e, nil, nil, &parserState{})
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
	events, err := reader.processEvent("mysql-bin.000001", ddlBinlogEvent("ALTER TABLE t ADD COLUMN age INT"), nil, nil, &parserState{})
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
	events, err := reader.processEvent("mysql-bin.000001", ddlBinlogEvent("ALTER TABLE t ADD COLUMN age INT"), nil, nil, &parserState{})
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

	if _, err := reader.processEvent("mysql-bin.000001", queryBinlogEvent("BEGIN", 10, 120), &from, &to, state); err != nil {
		t.Fatalf("process BEGIN: %v", err)
	}
	if _, err := reader.processEvent("mysql-bin.000001", queryBinlogEvent("COMMIT", 21, 140), &from, &to, state); err != nil {
		t.Fatalf("process COMMIT beyond to-time: %v", err)
	}
	if state.inTransaction {
		t.Fatal("transaction remains open after COMMIT")
	}
	_, err := reader.processEvent("mysql-bin.000001", queryBinlogEvent("BEGIN", 21, 160), &from, &to, state)
	var stop stopParseError
	if !errors.As(err, &stop) {
		t.Fatalf("event after completed transaction error = %v, want stopParseError", err)
	}
}

func TestProcessEventSkipsTransactionStartedBeforeFromTime(t *testing.T) {
	reader := NewReader(config.Config{}, nil)
	state := &parserState{}
	from := time.Unix(10, 0)

	if _, err := reader.processEvent("mysql-bin.000001", queryBinlogEvent("BEGIN", 9, 120), &from, nil, state); err != nil {
		t.Fatalf("process BEGIN: %v", err)
	}
	events, err := reader.processEvent("mysql-bin.000001", queryBinlogEvent("ALTER TABLE t ADD COLUMN age INT", 11, 140), &from, nil, state)
	if err != nil {
		t.Fatalf("process event in skipped transaction: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("process event in skipped transaction returned %d events, want 0", len(events))
	}
	if _, err := reader.processEvent("mysql-bin.000001", queryBinlogEvent("COMMIT", 12, 160), &from, nil, state); err != nil {
		t.Fatalf("process COMMIT: %v", err)
	}
}

func TestProcessEventCompletesTransactionBeyondToPos(t *testing.T) {
	reader := NewReader(config.Config{Binlogs: []string{"mysql-bin.000001"}, FromPos: 100, ToPos: 200}, nil)
	state := &parserState{}

	if _, err := reader.processEvent("mysql-bin.000001", queryBinlogEvent("BEGIN", 10, 120), nil, nil, state); err != nil {
		t.Fatalf("process BEGIN: %v", err)
	}
	if _, err := reader.processEvent("mysql-bin.000001", queryBinlogEvent("COMMIT", 11, 220), nil, nil, state); err != nil {
		t.Fatalf("process COMMIT beyond to-pos: %v", err)
	}
	_, err := reader.processEvent("mysql-bin.000001", queryBinlogEvent("BEGIN", 12, 240), nil, nil, state)
	var stop stopParseError
	if !errors.As(err, &stop) {
		t.Fatalf("event after completed transaction error = %v, want stopParseError", err)
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
