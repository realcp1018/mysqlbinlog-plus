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
		Event: &replication.QueryEvent{Query: []byte("ALTER TABLE t ADD COLUMN age INT")},
	}, true)
	if !tracker.schemaUnreliable {
		t.Fatal("tracker did not mark schema as unreliable after DDL")
	}
}

func TestUpdateParserStateKeepsSchemaReliableAfterTruncate(t *testing.T) {
	tracker := &parserState{}

	tracker.update(&replication.BinlogEvent{
		Event: &replication.QueryEvent{Query: []byte("TRUNCATE TABLE t")},
	}, true)
	if tracker.schemaUnreliable {
		t.Fatal("tracker marked schema as unreliable after TRUNCATE TABLE")
	}
}

func TestUpdateParserStateIgnoresDDLBeforeRange(t *testing.T) {
	tracker := &parserState{}

	tracker.update(&replication.BinlogEvent{
		Event: &replication.QueryEvent{Query: []byte("ALTER TABLE t ADD COLUMN age INT")},
	}, false)
	if tracker.schemaUnreliable {
		t.Fatal("tracker marked schema as unreliable before selected range")
	}
}

func TestConvertEmitsDDLByDefault(t *testing.T) {
	reader := NewReader(config.Config{}, nil)
	events, err := reader.processEvent("mysql-bin.000001", ddlBinlogEvent("ALTER TABLE t ADD COLUMN age INT"), nil, nil, &parserState{})
	if err != nil {
		t.Fatalf("convert returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("convert returned %d events, want 1", len(events))
	}
	if got, want := events[0].Change.Type, event.DDL; got != want {
		t.Fatalf("event type = %q, want %q", got, want)
	}
	if got, want := events[0].DDLSQLText, "ALTER TABLE t ADD COLUMN age INT;"; got != want {
		t.Fatalf("DDLSQLText = %q, want %q", got, want)
	}
	if events[0].DDLSQLText == "" {
		t.Fatal("DDL event was treated as a rows event")
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
